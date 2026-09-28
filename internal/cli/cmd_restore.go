package cli

import (
	"errors"
	"fmt"
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
  frost restore maple-otter --to ~/Desktop
  frost restore latest ~/notes.txt --overwrite`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				if !isTerminal(os.Stdin) {
					return errors.New("say which snapshot to restore, e.g. `frost restore latest`")
				}
				return runBrowser(cmd.Context())
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

			// A new folder holds what was backed up, relative to the folder
			// it all shares. The whole snapshot is its backed-up folders.
			if len(include) == 0 && !overwrite {
				include = snap.Paths
			}
			base := snapshot.RestoreBase(include)
			var target string
			switch {
			case beside:
				target, err = engine.BesideFolder(base, snap.ID)
				if err != nil {
					return fmt.Errorf("can't restore beside the originals: %w. Use --to <dir> instead", err)
				}
			case to != "":
				if target, err = engine.NewRestoreFolder(to, snap.ID); err != nil {
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
			where := "original locations " + caution("(existing files will be replaced)")
			if target != "" {
				where = tildify(target)
			} else {
				base = ""
			}

			fmt.Fprintf(out, "%s %s %s\n", heading("restore "+snap.ID), dim(when(snap.Time)), dim("("+ago(snap.Time)+")"))
			if len(include) > 0 {
				fmt.Fprintln(out, kv("paths", strings.Join(include, "\n               ")))
			} else {
				fmt.Fprintln(out, kv("paths", "everything"))
			}
			fmt.Fprintln(out, kv("into", where))

			if overwrite && !yes {
				ok, err := newPrompter(cmd).yesNo("Go ahead?", false)
				if err != nil || !ok {
					return errors.Join(err, errors.New("cancelled"))
				}
			}

			live := liveOutput()
			res, err := a.engine.Restore(cmd.Context(), snap.ID, engine.RestoreOptions{
				Target:    target,
				NewTarget: target != "",
				Base:      base,
				Include:   include,
				Progress: func(p string, done, total int) {
					if live {
						statusLine(out, fmt.Sprintf("  %d/%d  %s", done, total, dim(printable(path.Base(p)))))
					}
				},
			})
			if live {
				clearStatus(out)
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "%s %s files (%s), every chunk checked against its hash.\n",
				good("Restored"), humanCount(res.Files), humanBytes(res.Bytes))
			return nil
		},
	}
	cmd.Flags().BoolVar(&beside, "beside", false, "restore into a new folder next to the originals")
	cmd.Flags().StringVar(&to, "to", "", "restore into a new folder inside this directory")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "restore over the originals, replacing what's there (asks first)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask before overwriting")
	return cmd
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
