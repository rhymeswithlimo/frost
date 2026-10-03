package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"

	"github.com/rhymeswithlimo/frost/internal/engine"
	"github.com/rhymeswithlimo/frost/internal/snapshot"
)

func newBackupCmd() *cobra.Command {
	var (
		paths     []string
		exclude   []string
		dryRun    bool
		noVerify  bool
		scheduled bool
		logFile   string
	)
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Back up now",
		Long: `Backs up the directories in your config. Only data that changed since the
last run is uploaded. Use flags to override the config for this run only.`,
		Example: `  frost backup
  frost backup --dry-run
  frost backup --path ~/Pictures --exclude "*.raw"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) (runErr error) {
			out := cmd.OutOrStdout()
			if logFile != "" && !scheduled {
				return errors.New("--log-file requires --scheduled")
			}
			if scheduled {
				var log *os.File
				if logFile != "" || runtime.GOOS == "windows" {
					if logFile == "" {
						logFile = logPath()
					}
					var err error
					log, err = openScheduledLog(logFile)
					if err != nil {
						fmt.Fprintln(cmd.ErrOrStderr(), "couldn't open scheduled run log:", errorText(err))
					} else {
						defer log.Close()
						out = io.MultiWriter(plainLogWriter{log}, out)
					}
				} else {
					trimLog()
				}
				fmt.Fprintf(out, "[%s] scheduled backup starting\n", time.Now().Format(time.RFC3339))
				// Runs last, whether or not the backup worked: a newer
				// release might be the fix.
				defer autoUpdate(cmd.Context(), out)
				defer func() {
					if log != nil && runErr != nil {
						fmt.Fprintln(log, "error:", errorText(runErr))
					}
				}()
			}

			a, err := openApp(cmd.Context())
			if err != nil {
				if isStorageError(err) {
					rememberFailure(err) // for `status`, which can't open it either
				}
				return err
			}
			defer a.Close()

			opts := engine.BackupOptions{
				Paths:   a.cfg.ExpandedPaths(),
				Exclude: append(a.cfg.ExpandedExclude(), exclude...),
				DryRun:  dryRun,
			}
			if len(paths) > 0 {
				opts.Paths = paths
			}
			live := !scheduled && liveOutput()
			if live {
				opts.Progress = progressPrinter(out)
			}

			res, err := a.engine.Backup(cmd.Context(), opts)
			if live {
				clearStatus(out)
			}
			if err != nil {
				if errors.Is(err, fs.ErrPermission) && runtime.GOOS == "darwin" {
					err = fmt.Errorf("%w\n\nmacOS blocks access to some folders until you allow it. Open System Settings > Privacy & Security > Full Disk Access and add %s", err, executable())
					if v, ok := recentlyUpdated(); ok {
						err = fmt.Errorf("%w\n\nfrost updated itself to %s, and macOS may not recognise the new binary. If frost is already on the list, turn it off and on again", err, v)
					}
				}
				return err
			}
			if dryRun {
				printDryRun(out, res)
				return nil
			}
			printBackup(out, res, snapshot.Shorten(slices.Collect(maps.Values(a.engine.Manifest.Snapshots()))))

			if n := a.cfg.Verify.Sample; n > 0 && !noVerify {
				v, err := a.engine.Verify(cmd.Context(), n, false)
				switch {
				case err != nil:
					fmt.Fprintln(out, kv("verified", errStyle("couldn't run: ")+err.Error()))
					return fmt.Errorf("verification couldn't complete: %w", err)
				case v.OK():
					fmt.Fprintln(out, kv("verified", good("ok")+dim(fmt.Sprintf(", %d random objects re-downloaded and checked", v.Checked))))
				default:
					fmt.Fprintln(out, kv("verified", errStyle(fmt.Sprintf("%d of %d checks FAILED", len(v.Failures), v.Checked))))
					for _, f := range v.Failures {
						fmt.Fprintln(out, "    "+f)
					}
					return fmt.Errorf("verification failed, see `frost status`")
				}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringArrayVar(&paths, "path", nil, "back up this directory instead of the configured ones (repeatable)")
	f.StringArrayVar(&exclude, "exclude", nil, "also skip files matching this pattern (repeatable)")
	f.BoolVarP(&dryRun, "dry-run", "n", false, "show what would be uploaded without uploading anything")
	f.BoolVar(&noVerify, "no-verify", false, "skip the spot check after the backup")
	f.BoolVar(&scheduled, "scheduled", false, "log-friendly output for scheduled runs")
	f.MarkHidden("scheduled")
	f.StringVar(&logFile, "log-file", "", "append scheduled output to this file")
	f.MarkHidden("log-file")
	return cmd
}

// openScheduledLog opens and trims the same file, without following a symlink.
func openScheduledLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	name := filepath.Base(path)
	before, err := r.Lstat(name)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if before != nil && !before.Mode().IsRegular() {
		return nil, errors.New("scheduled run log must be a regular file")
	}
	f, err := r.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	current, err := f.Stat()
	after, statErr := r.Lstat(name)
	if err != nil || statErr != nil || !after.Mode().IsRegular() || !os.SameFile(after, current) || (before != nil && !os.SameFile(before, current)) {
		f.Close()
		return nil, errors.New("scheduled run log changed while opening it")
	}
	if current.Size() > 1<<20 {
		if err := truncateLog(r, name, current); err != nil {
			f.Close()
			return nil, err
		}
	}
	return f, nil
}

type plainLogWriter struct{ io.Writer }

func (w plainLogWriter) Write(p []byte) (int, error) {
	_, err := io.WriteString(w.Writer, ansi.Strip(string(p)))
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func progressPrinter(out io.Writer) func(engine.Progress) {
	last := time.Time{}
	return func(p engine.Progress) {
		if time.Since(last) < 100*time.Millisecond {
			return
		}
		last = time.Now()
		name := printable(tildify(filepath.FromSlash(p.Path)))
		if w := ansi.StringWidth(name); w > 40 {
			name = "..." + ansi.TruncateLeft(name, w-37, "")
		}
		statusLine(out, fmt.Sprintf("  %s files, %s scanned, %s new  %s",
			humanCount(p.Files), humanBytes(p.Bytes), humanBytes(p.NewBytes), dim(name)))
	}
}

// printBackup prints what a backup saved. short holds the snapshots its ID
// is told apart from, the new one included.
func printBackup(out io.Writer, res engine.BackupResult, short snapshot.ShortIDs) {
	s := res.Snapshot
	fmt.Fprintf(out, "%s %s\n", heading("snapshot "+short.Of(s.ID)), dim(when(s.Time)))
	fmt.Fprintln(out, kv("files", fmt.Sprintf("%s (%s)", humanCount(s.Stats.Files), humanBytes(s.Stats.Bytes))))
	if s.Stats.NewChunks == 0 {
		fmt.Fprintln(out, kv("new data", "none, everything was already backed up"))
	} else {
		fmt.Fprintln(out, kv("new data", fmt.Sprintf("%s in %s chunks %s", humanBytes(s.Stats.NewBytes),
			humanCount(s.Stats.NewChunks), dim("("+humanBytes(s.Stats.UploadedBytes)+" uploaded after compression)"))))
	}
	if len(s.Missing) > 0 {
		fmt.Fprintln(out, kv("not found", caution(missingList(s.Missing))))
		fmt.Fprintln(out, kv("", dim("Skipped until they're back. If one moved, update it with `frost init`.")))
	}
	if s.Stats.Skipped > 0 {
		fmt.Fprintln(out, kv("skipped", caution(fmt.Sprintf("%d items couldn't be read:", s.Stats.Skipped))))
		printSome(out, s.Warnings, s.Stats.Skipped)
	}
	if s.Stats.Kept > 0 {
		fmt.Fprintln(out, kv("kept", caution(fmt.Sprintf("%d files kept changing while they were read, so the snapshot has their previous copy:", s.Stats.Kept))))
		var names []string
		for _, p := range s.Kept {
			names = append(names, tildify(filepath.FromSlash(p)))
		}
		printSome(out, names, s.Stats.Kept)
	}
}

// printSome prints the first ten of items, of which there are total.
func printSome(out io.Writer, items []string, total int) {
	for _, it := range items[:min(len(items), 10)] {
		fmt.Fprintln(out, "    "+printable(it))
	}
	if more := total - min(len(items), 10); more > 0 {
		fmt.Fprintf(out, "    ... and %d more\n", more)
	}
}

func printDryRun(out io.Writer, res engine.BackupResult) {
	s := res.Snapshot
	fmt.Fprintln(out, heading("dry run")+dim(" nothing was uploaded"))
	if len(s.Missing) > 0 {
		fmt.Fprintln(out, kv("not found", caution(missingList(s.Missing))))
	}
	if len(res.Planned) == 0 {
		fmt.Fprintf(out, "\nNothing to upload. All %s files (%s) are already backed up.\n",
			humanCount(s.Stats.Files), humanBytes(s.Stats.Bytes))
		return
	}
	planned := slices.Clone(res.Planned)
	slices.SortFunc(planned, func(a, b engine.PlannedFile) int { return strings.Compare(a.Path, b.Path) })
	fmt.Fprintf(out, "\nWould upload new data from %s files:\n\n", humanCount(len(planned)))
	for _, p := range planned {
		fmt.Fprintf(out, "  %10s  %s\n", humanBytes(p.NewBytes), printable(tildify(filepath.FromSlash(p.Path))))
	}
	fmt.Fprintf(out, "\n%s\n", kv("total", fmt.Sprintf("%s new, in %s chunks, out of %s scanned",
		bold(humanBytes(s.Stats.NewBytes)), humanCount(s.Stats.NewChunks), humanBytes(s.Stats.Bytes))))
	fmt.Fprintln(out, kv("", dim("Run without --dry-run to upload.")))
}

// trimLog keeps the scheduled-run log from growing forever.
func trimLog() {
	r, err := os.OpenRoot(filepath.Dir(logPath()))
	if err != nil {
		return
	}
	defer r.Close()
	name := filepath.Base(logPath())
	fi, err := r.Lstat(name)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() <= 1<<20 {
		return
	}
	truncateLog(r, name, fi)
}

// Windows append handles can't truncate, so trimming needs a separate handle.
func truncateLog(r *os.Root, name string, fi os.FileInfo) error {
	f, err := r.OpenFile(name, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	current, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(fi, current) {
		return errors.New("scheduled run log changed while trimming it")
	}
	return f.Truncate(0)
}

func executable() string {
	p, err := os.Executable()
	if err != nil {
		return "the frost binary"
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return p
}

// missingList names paths that weren't found, the way you'd type them.
func missingList(paths []string) string {
	names := make([]string, len(paths))
	for i, p := range paths {
		names[i] = printable(tildify(filepath.FromSlash(p)))
	}
	return strings.Join(names, ", ")
}
