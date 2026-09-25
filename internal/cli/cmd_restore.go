package cli

import (
	"errors"
	"fmt"
	"os"
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
		target  string
		inPlace bool
		yes     bool
	)
	cmd := &cobra.Command{
		Use:   "restore [snapshot] [paths...]",
		Short: "Get files back from a snapshot",
		Long: `Restores files from a snapshot. Pick the snapshot by ID, by a unique start
of its ID, by "latest", or by time: "3 days ago", "12h", "yesterday",
"2026-09-20". A time picks the newest snapshot at or before it.

By default files go into a new folder, ./frost-restore-<id>, at their full
original path, so nothing on your disk is overwritten. Use --in-place to put
them back where they came from.

With no arguments in a terminal, opens the snapshot browser.`,
		Example: `  frost restore latest
  frost restore "3 days ago" ~/Documents/taxes
  frost restore maple-otter --target /tmp/r
  frost restore latest ~/notes.txt --in-place`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				if !isTerminal(os.Stdin) {
					return errors.New("say which snapshot to restore, e.g. `frost restore latest`")
				}
				return runBrowser(cmd.Context())
			}
			if inPlace && target != "" {
				return errors.New("--in-place and --target can't be used together")
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

			switch {
			case inPlace:
				target = ""
			case target == "":
				target = "frost-restore-" + snap.ID
			}
			where := "original locations " + caution("(existing files will be replaced)")
			if target != "" {
				abs, _ := filepath.Abs(target)
				target = abs
				where = tildify(abs)
			}

			fmt.Fprintf(out, "%s %s %s\n", heading("restore "+snap.ID), dim(when(snap.Time)), dim("("+ago(snap.Time)+")"))
			if len(include) > 0 {
				fmt.Fprintln(out, kv("paths", strings.Join(include, "\n               ")))
			} else {
				fmt.Fprintln(out, kv("paths", "everything"))
			}
			fmt.Fprintln(out, kv("into", where))

			if inPlace && !yes {
				ok, err := newPrompter(cmd).yesNo("Go ahead?", false)
				if err != nil || !ok {
					return errors.Join(err, errors.New("cancelled"))
				}
			}

			live := isTerminal(os.Stdout)
			res, err := a.engine.Restore(cmd.Context(), snap.ID, engine.RestoreOptions{
				Target:  target,
				Include: include,
				Progress: func(p string, done, total int) {
					if live {
						fmt.Fprintf(out, "\r\033[K  %d/%d  %s", done, total, dim(filepath.Base(p)))
					}
				},
			})
			if live {
				fmt.Fprint(out, "\r\033[K")
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "%s %s files (%s), every chunk checked against its hash.\n",
				good("Restored"), humanCount(res.Files), humanBytes(res.Bytes))
			return nil
		},
	}
	cmd.Flags().StringVarP(&target, "target", "t", "", "restore into this directory (default ./frost-restore-<id>)")
	cmd.Flags().BoolVar(&inPlace, "in-place", false, "restore over the original locations")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask for confirmation")
	return cmd
}
