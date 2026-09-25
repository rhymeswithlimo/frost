package cli

import (
	"fmt"
	"io"
	"slices"

	"github.com/spf13/cobra"

	"github.com/rhymeswithlimo/frost/internal/config"
	"github.com/rhymeswithlimo/frost/internal/engine"
	"github.com/rhymeswithlimo/frost/internal/schedule"
	"github.com/rhymeswithlimo/frost/internal/snapshot"
)

func newStatusCmd() *cobra.Command {
	var (
		verify bool
		all    bool
	)
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show recent snapshots, schedule and backup health",
		Long: `Shows your recent snapshots, when the last and next backups run, and the
result of the latest verification (a random sample of uploaded data
downloaded and checked against its hashes).

--verify runs a fresh verification first. You can put
"frost status --verify" in your own scheduler to check on a separate cadence.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.Close()
			out := cmd.OutOrStdout()
			e := a.engine

			if verify {
				n := a.cfg.Verify.Sample
				if n == 0 {
					n = 20
				}
				fmt.Fprintf(out, dim("Checking %d random chunks... "), n)
				if _, err := e.Verify(cmd.Context(), n); err != nil {
					return err
				}
				fmt.Fprintln(out, dim("done"))
			}

			snaps, err := e.Repo.Snapshots(cmd.Context(), e.Manifest.Snapshots())
			if err != nil {
				return err
			}
			e.Manifest.SetSnapshots(snaps)
			slices.SortFunc(snaps, func(x, y snapshot.Snapshot) int { return y.Time.Compare(x.Time) })

			fmt.Fprintf(out, "%s %s\n", heading("frost"), dim(e.Repo.Backend.String()+"  key "+e.Repo.Key.Fingerprint()))

			// Last run.
			if last, ok := e.LastBackup(); !ok {
				fmt.Fprintln(out, kv("last backup", dim("never")))
			} else if last.Error != "" {
				fmt.Fprintln(out, kv("last backup", errStyle("FAILED ")+ago(last.Time)+": "+last.Error))
			} else if last.Skipped > 0 {
				fmt.Fprintln(out, kv("last backup", caution(fmt.Sprintf("ok, but %d items couldn't be read ", last.Skipped))+ago(last.Time)+dim("  "+last.SnapshotID)))
			} else {
				fmt.Fprintln(out, kv("last backup", good("ok ")+ago(last.Time)+dim("  "+last.SnapshotID)))
			}

			// Next run.
			fmt.Fprintln(out, kv("next backup", nextRun(a.cfg, e)))

			// Health.
			if v, ok := e.LastVerify(); !ok {
				fmt.Fprintln(out, kv("health", dim("not checked yet")))
			} else if v.OK() {
				fmt.Fprintln(out, kv("health", good("ok ")+fmt.Sprintf("%d objects checked %s", v.Checked, ago(v.Time))))
			} else {
				fmt.Fprintln(out, kv("health", errStyle(fmt.Sprintf("PROBLEM: %d of %d checks failed %s", len(v.Failures), v.Checked, ago(v.Time)))))
				for _, f := range v.Failures {
					fmt.Fprintln(out, "               "+f)
				}
				fmt.Fprintln(out, "               "+dim("Run a new backup to re-upload anything missing, then `frost status --verify`."))
			}

			if len(snaps) > 0 {
				fmt.Fprintln(out, kv("protected", fmt.Sprintf("%s files, %s, in %d snapshots",
					humanCount(snaps[0].Stats.Files), humanBytes(snaps[0].Stats.Bytes), len(snaps))))
			}

			fmt.Fprintln(out)
			printSnapshots(out, snaps, all)
			return nil
		},
	}
	cmd.Flags().BoolVar(&verify, "verify", false, "run a verification now")
	cmd.Flags().BoolVarP(&all, "all", "a", false, "list every snapshot, not just the latest 10")
	return cmd
}

// nextRun estimates the next scheduled backup from the last one and the
// interval. OS schedulers don't expose this in a portable way.
func nextRun(cfg config.Config, e *engine.Engine) string {
	if !cfg.Schedule.Enabled {
		return dim("automatic backups are off (") + "frost config set schedule.enabled true" + dim(")")
	}
	every, err := config.Interval(cfg.Schedule.Every)
	if err != nil {
		return errStyle(err.Error())
	}
	if !schedule.Installed() {
		return caution("scheduled job is missing, run `frost init` or `frost config set schedule.enabled true`")
	}
	how := dim("  " + cfg.Schedule.Every + " via " + schedule.Kind())
	last, ok := e.LastBackup()
	if !ok {
		return "soon" + how
	}
	return "~" + in(last.Time.Add(every)) + how
}

func printSnapshots(out io.Writer, snaps []snapshot.Snapshot, all bool) {
	if len(snaps) == 0 {
		fmt.Fprintln(out, dim("  No snapshots yet. Run `frost backup`."))
		return
	}
	fmt.Fprintln(out, dim(fmt.Sprintf("  %-22s %-18s %8s %10s %10s", "SNAPSHOT", "TAKEN", "FILES", "SIZE", "NEW")))
	shown := snaps
	if !all && len(shown) > 10 {
		shown = shown[:10]
	}
	for _, s := range shown {
		fmt.Fprintf(out, "  %-22s %-18s %8s %10s %10s\n", s.ID, when(s.Time),
			humanCount(s.Stats.Files), humanBytes(s.Stats.Bytes), humanBytes(s.Stats.NewBytes))
	}
	if len(shown) < len(snaps) {
		fmt.Fprintln(out, dim(fmt.Sprintf("  ... %d older, use --all to see them", len(snaps)-len(shown))))
	}
}
