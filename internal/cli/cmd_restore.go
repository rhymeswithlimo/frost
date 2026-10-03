package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/rhymeswithlimo/frost/internal/config"
	"github.com/rhymeswithlimo/frost/internal/engine"
	"github.com/rhymeswithlimo/frost/internal/snapshot"
)

func newRestoreCmd() *cobra.Command {
	var (
		beside, overwrite, yes bool
		to                     string
	)
	cmd := &cobra.Command{
		Use:   "restore [snapshot] [paths...]",
		Short: "Get files back from a snapshot",
		Long: `Restores files from a snapshot. Pick the snapshot by ID, by a unique start
of its ID, by "latest", or by time: "3 days ago", "12h", "yesterday",
"2026-09-20". A time picks the newest snapshot at or before it.

Say where the files go with exactly one of:

  --beside      a new frost-restore-<id> folder next to the originals
  --to <dir>    a new frost-restore-<id> folder inside <dir>
  --overwrite   back where they came from, replacing what's there

In a new folder, what you restore keeps its name: restoring ~/Documents/taxes
with --beside gives ~/Documents/frost-restore-<id>/taxes.

With no arguments in a terminal, opens the snapshot browser.`,
		Example: `  frost restore latest --beside
  frost restore "3 days ago" ~/Documents/taxes --beside
  frost restore maple-absurd-3f1c --to ~/Desktop
  frost restore latest ~/notes.txt --overwrite`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				if !isTerminal(os.Stdin) {
					return errors.New("say which snapshot to restore, e.g. `frost restore latest`")
				}
				return runBrowser(cmd.Context(), cmd.OutOrStdout())
			}
			switch n := btoi(beside) + btoi(overwrite) + btoi(to != ""); {
			case n == 0:
				return errors.New("choose where to restore: --beside, --to <dir> or --overwrite")
			case n > 1:
				return errors.New("choose only one of --beside, --to and --overwrite")
			}
			if to != "" {
				abs, err := filepath.Abs(config.Expand(to))
				if err != nil {
					return err
				}
				if info, err := os.Stat(abs); err != nil || !info.IsDir() {
					return fmt.Errorf("there's no folder at %s", tildify(abs))
				}
				to = abs
			}

			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.Close()
			out := cmd.OutOrStdout()

			snaps, err := a.engine.Repo.Snapshots(cmd.Context(), a.engine.Manifest.Snapshots())
			if err != nil {
				return err
			}
			snap, err := snapshot.Resolve(snaps, args[0], time.Now())
			if err != nil {
				return err
			}

			var include []string
			for _, p := range args[1:] {
				abs, err := filepath.Abs(config.Expand(p))
				if err != nil {
					return err
				}
				include = append(include, filepath.ToSlash(abs))
			}
			rerun := rerunCommand(snap.ID, include, beside, to, overwrite)

			// A new folder holds what was backed up, relative to the folder
			// it all shares. The whole snapshot is its backed-up folders.
			if len(include) == 0 && !overwrite {
				include = snap.Paths
			}
			base := snapshot.RestoreBase(include)
			var target string
			var resume bool
			switch {
			case beside:
				target, resume, err = engine.BesideFolder(base, snap.ID, include)
				if err != nil {
					return fmt.Errorf("can't restore beside the originals: %w. Use --to <dir> instead", err)
				}
			case to != "":
				if target, resume, err = engine.NewRestoreFolder(to, snap.ID, include); err != nil {
					return err
				}
			default:
				// Say so before asking, not after.
				check := include
				if len(check) == 0 {
					check = snap.Paths
				}
				if err := engine.CanOverwrite(check); err != nil {
					return fmt.Errorf("can't overwrite the originals: %w. Use --beside or --to <dir> instead", err)
				}
			}
			p := newPrompter(cmd)
			p.open("restore "+snapshot.Shorten(snaps).Of(snap.ID), when(snap.Time)+" ("+ago(snap.Time)+")")
			p.gap()
			if len(include) > 0 {
				p.row("paths", strings.Join(include, "\n"))
			} else {
				p.row("paths", "everything")
			}
			if target != "" {
				where := tildify(target)
				if resume {
					where += dim(", carrying on with the unfinished restore there")
				}
				p.row("into", where)
			} else {
				base = ""
				p.warnRow("into", "original locations "+caution("(existing files will be replaced)"))
			}

			if overwrite && !yes {
				p.gap()
				ok, err := p.yesNo("Go ahead?", false)
				if err != nil || !ok {
					return errors.Join(err, errors.New("cancelled"))
				}
			}

			live := liveOutput()
			opts := engine.RestoreOptions{
				Target:    target,
				NewTarget: target != "",
				Base:      base,
				Include:   include,
			}
			if live {
				opts.Progress = restorePrinter(out)
			}
			res, err := a.engine.Restore(cmd.Context(), snap.ID, opts)
			if live {
				clearStatus(out)
			}
			if err != nil {
				if res.Unfinished {
					return fmt.Errorf("%w\n\nWhat's restored so far was kept. To carry on from there, run:\n\n  %s", err, rerun)
				}
				return err
			}
			p.gap()
			p.close(fmt.Sprintf("%s %s files (%s), every chunk checked against its hash.",
				good("Restored"), humanCount(res.Files), humanBytes(res.Bytes)))
			return nil
		},
	}
	cmd.Flags().BoolVar(&beside, "beside", false, "restore into a new folder next to the originals")
	cmd.Flags().StringVar(&to, "to", "", "restore into a new folder inside this directory")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "restore over the originals, replacing what's there (asks first)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask before overwriting")
	return cmd
}

// rerunCommand is the restore to run to continue this one. It names the
// snapshot by its full ID, because "latest" may mean a newer one by then
// and a later snapshot could share a short ID, and the paths in full,
// because it may run from another folder.
func rerunCommand(id string, paths []string, beside bool, to string, overwrite bool) string {
	quote := func(s string) string {
		if strings.ContainsAny(s, " \t'\"$&;|<>()*?[]#~`") {
			return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
		}
		return s
	}
	parts := []string{"frost", "restore", id}
	for _, p := range paths {
		parts = append(parts, quote(filepath.FromSlash(p)))
	}
	switch {
	case beside:
		parts = append(parts, "--beside")
	case to != "":
		parts = append(parts, "--to", quote(to))
	case overwrite:
		parts = append(parts, "--overwrite")
	}
	return strings.Join(parts, " ")
}

func restorePrinter(out io.Writer) func(engine.RestoreProgress) {
	last := time.Time{}
	return func(p engine.RestoreProgress) {
		if time.Since(last) < 100*time.Millisecond {
			return
		}
		last = time.Now()
		name := dim(printable(path.Base(p.Path)))
		if p.Checking {
			statusLine(out, railed(fmt.Sprintf("checking what's already there, %s of %s  %s", humanBytes(p.Bytes), humanBytes(p.TotalBytes), name)))
			return
		}
		statusLine(out, railed(fmt.Sprintf("%s/%s files, %s of %s  %s", humanCount(p.Files), humanCount(p.TotalFiles), humanBytes(p.Bytes), humanBytes(p.TotalBytes), name)))
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
