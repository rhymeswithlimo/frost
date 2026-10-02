package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"

	"github.com/rhymeswithlimo/frost/internal/engine"
)

func newBackupCmd() *cobra.Command {
	var (
		paths     []string
		exclude   []string
		dryRun    bool
		noVerify  bool
		scheduled bool
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
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			if scheduled {
				trimLog()
				fmt.Fprintf(out, "[%s] scheduled backup starting\n", time.Now().Format(time.RFC3339))
				// Runs last, whether or not the backup worked: a newer
				// release might be the fix.
				defer autoUpdate(cmd.Context(), out)
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
			printBackup(out, res)

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
	return cmd
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

func printBackup(out io.Writer, res engine.BackupResult) {
	s := res.Snapshot
	fmt.Fprintf(out, "%s %s\n", heading("snapshot "+s.ID), dim(when(s.Time)))
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
	f, err := r.OpenFile(name, os.O_WRONLY, 0)
	if err != nil {
		return
	}
	defer f.Close()
	current, err := f.Stat()
	if err == nil && os.SameFile(fi, current) {
		f.Truncate(0)
	}
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
