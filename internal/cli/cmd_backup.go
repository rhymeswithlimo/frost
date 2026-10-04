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
		Args:  cobra.NoArgs,
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
						fmt.Fprintln(log, ansi.Strip(errorLine(runErr, blockOpen))+"\n")
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
			b := newBlock(out)
			if dryRun {
				b.open("dry run", a.engine.Repo.Backend.String())
			} else {
				b.open("backup", a.engine.Repo.Backend.String())
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
			short := snapshot.Shorten(slices.Collect(maps.Values(a.engine.Manifest.Snapshots())))
			if dryRun {
				printDryRun(b, res, short)
				return nil
			}
			b.gap()
			printBackup(b, res)

			if n := a.cfg.Verify.Sample; n > 0 && !noVerify {
				// A run that saved nothing new only checks once a day.
				if last, ok := a.engine.LastVerify(); ok && res.Unchanged && !a.engine.VerifyDue() {
					b.row("verified", good("ok ")+fmt.Sprintf("%s, %d objects checked", ago(last.Time), last.Checked))
					b.gap()
					b.close(alreadyBackedUp(res, short))
					return nil
				}
				v, err := a.engine.Verify(cmd.Context(), n, false)
				switch {
				case err != nil:
					b.failRow("verified", "couldn't run: "+err.Error())
					return fmt.Errorf("verification couldn't complete: %w", err)
				case v.OK():
					b.row("verified", good("ok")+dim(fmt.Sprintf(", %d random objects re-downloaded and checked", v.Checked)))
				default:
					b.failRow("verified", errStyle("failed")+fmt.Sprintf(", %d of %d checks didn't pass:", len(v.Failures), v.Checked))
					for _, f := range v.Failures {
						b.row("", "  "+printable(f))
					}
					return fmt.Errorf("verification failed, see `frost status`")
				}
			}
			b.gap()
			if res.Unchanged {
				b.close(alreadyBackedUp(res, short))
			} else {
				b.close(good("Saved") + " snapshot " + bold(short.Of(res.Snapshot.ID)))
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVarP(&dryRun, "dry-run", "n", false, "show what would be uploaded without uploading anything")
	f.StringArrayVar(&paths, "path", nil, "back up this directory instead of the configured ones (repeatable)")
	f.StringArrayVar(&exclude, "exclude", nil, "also skip files matching this pattern (repeatable)")
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
		statusLine(out, railed(fmt.Sprintf("%s files, %s scanned, %s new  %s",
			humanCount(p.Files), humanBytes(p.Bytes), humanBytes(p.NewBytes), dim(name))))
	}
}

// printBackup prints what a backup saved, as rows of b.
func printBackup(b *block, res engine.BackupResult) {
	s := res.Snapshot
	if res.Compared && !res.Unchanged && !res.Changes.None() {
		b.row("changes", changesText(res.Changes))
	}
	b.row("files", fmt.Sprintf("%s (%s)", humanCount(s.Stats.Files), humanBytes(s.Stats.Bytes)))
	uploaded := fmt.Sprintf("%s in %s chunks %s", humanBytes(s.Stats.NewBytes),
		humanCount(s.Stats.NewChunks), dim("("+humanBytes(s.Stats.UploadedBytes)+" uploaded after compression)"))
	switch {
	case res.Unchanged && s.Stats.NewChunks > 0 && s.Stats.Kept == 0:
		// Nothing changed, so these were missing from storage.
		b.row("new data", uploaded+"\n"+dim("Uploaded again because storage was missing them."))
	case res.Unchanged && s.Stats.NewChunks > 0:
		// Read from a busy file before it changed. The kept row explains.
		b.row("new data", uploaded)
	case res.Unchanged:
	case s.Stats.NewChunks == 0:
		b.row("new data", "none")
	default:
		b.row("new data", uploaded)
	}
	if len(s.Missing) > 0 {
		b.warnRow("not found", caution(missingList(s.Missing))+"\n"+
			dim("Skipped until they're back. If one moved, update it with `frost init`."))
	}
	if s.Stats.Skipped > 0 {
		b.warnRow("skipped", caution(fmt.Sprintf("%d items couldn't be read:", s.Stats.Skipped))+someOf(s.Warnings, s.Stats.Skipped))
	}
	if s.Stats.Kept > 0 {
		var names []string
		for _, p := range s.Kept {
			names = append(names, tildify(filepath.FromSlash(p)))
		}
		b.warnRow("kept", caution(fmt.Sprintf("%d files kept changing while they were read, so the snapshot has their previous copy:", s.Stats.Kept))+someOf(names, s.Stats.Kept))
	}
}

// someOf lists the first ten of items, of which there are total, one per
// line and indented, to follow a row's value.
func someOf(items []string, total int) string {
	var b strings.Builder
	for _, it := range items[:min(len(items), 10)] {
		b.WriteString("\n  " + printable(it))
	}
	if more := total - min(len(items), 10); more > 0 {
		fmt.Fprintf(&b, "\n  ... and %d more", more)
	}
	return b.String()
}

// alreadyBackedUp closes a backup that found nothing new to save.
func alreadyBackedUp(res engine.BackupResult, short snapshot.ShortIDs) string {
	return good("Already backed up.") + " Nothing has changed since snapshot " + bold(short.Of(res.Snapshot.ID)) + ", saved " + ago(res.Snapshot.Time) + "."
}

// changesText says what changed, like "3 added, 1 changed, 2 removed".
// Bare counts are files, and folders are named.
func changesText(c engine.Changes) string {
	var parts []string
	for _, p := range []struct {
		n    int
		what string
	}{
		{c.Files.Added, "added"}, {c.Files.Changed, "changed"}, {c.Files.Removed, "removed"},
	} {
		if p.n > 0 {
			parts = append(parts, humanCount(p.n)+" "+p.what)
		}
	}
	for _, p := range []struct {
		n    int
		what string
	}{
		{c.Folders.Added, "added"}, {c.Folders.Changed, "changed"}, {c.Folders.Removed, "removed"},
	} {
		if p.n > 0 {
			parts = append(parts, plural(p.n, "folder")+" "+p.what)
		}
	}
	return strings.Join(parts, ", ")
}

func printDryRun(b *block, res engine.BackupResult, short snapshot.ShortIDs) {
	s := res.Snapshot
	b.gap()
	if res.Compared && !res.Unchanged && !res.Changes.None() {
		b.row("changes", changesText(res.Changes))
	}
	b.row("files", fmt.Sprintf("%s (%s)", humanCount(s.Stats.Files), humanBytes(s.Stats.Bytes)))
	if len(s.Missing) > 0 {
		b.warnRow("not found", caution(missingList(s.Missing)))
	}
	b.gap()
	switch {
	case res.Unchanged:
		b.close("Nothing has changed since snapshot " + bold(short.Of(s.ID)) + ", saved " + ago(s.Time) + ", so there's nothing to back up.")
		return
	case len(res.Planned) == 0:
		b.line("No new data to upload. Everything in these files is already stored.")
	default:
		planned := slices.Clone(res.Planned)
		slices.SortFunc(planned, func(a, b engine.PlannedFile) int { return strings.Compare(a.Path, b.Path) })
		b.line(fmt.Sprintf("Would upload new data from %s:", plural(len(planned), "file")))
		b.gap()
		for _, p := range planned {
			b.line(fmt.Sprintf("%10s  %s", humanBytes(p.NewBytes), printable(tildify(filepath.FromSlash(p.Path)))))
		}
		b.gap()
		b.row("total", fmt.Sprintf("%s new, in %s chunks, out of %s scanned",
			bold(humanBytes(s.Stats.NewBytes)), humanCount(s.Stats.NewChunks), humanBytes(s.Stats.Bytes)))
	}
	b.gap()
	b.close("Nothing was uploaded. Run without " + bold("--dry-run") + " to back up.")
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
