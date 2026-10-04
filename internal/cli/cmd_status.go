package cli

import (
	"errors"
	"fmt"
	"io"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/rhymeswithlimo/frost/internal/config"
	"github.com/rhymeswithlimo/frost/internal/engine"
	"github.com/rhymeswithlimo/frost/internal/snapshot"
	"github.com/rhymeswithlimo/frost/internal/update"
)

func newStatusCmd() *cobra.Command {
	var (
		verify bool
		all    bool
	)
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show recent snapshots, schedule and backup health",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			a, err := openApp(cmd.Context())
			if err != nil {
				if isStorageError(err) {
					printStorageProblem(out, err)
					return errors.New("can't open your backups")
				}
				return err
			}
			defer a.Close()
			e := a.engine
			verifyFailed := false

			b := newBlock(out)
			b.open("frost", Version+"  "+e.Repo.Backend.String()+"  key "+e.Repo.Key.Fingerprint())
			b.gap()
			if verify {
				n := a.cfg.Verify.Sample
				if n == 0 {
					n = 20
				}
				fmt.Fprint(out, railed(dim(fmt.Sprintf("Checking %d random chunks... ", n))))
				v, err := e.Verify(cmd.Context(), n, true)
				if err != nil {
					fmt.Fprintln(out)
					return err
				}
				verifyFailed = !v.OK()
				fmt.Fprintln(out, dim("done"))
				b.gap()
			}

			snaps, gone, err := e.RefreshSnapshots(cmd.Context())
			if err != nil {
				return err
			}
			slices.SortFunc(snaps, func(x, y snapshot.Snapshot) int { return y.Time.Compare(x.Time) })
			short := snapshot.Shorten(snaps)

			// Last run.
			if last, ok := e.LastBackup(); !ok {
				b.row("last backup", dim("never"))
			} else if last.Error != "" {
				text := errStyle("failed") + " " + ago(last.Time) + ": " + printable(last.Error)
				if v, ok := recentlyUpdated(); ok && runtime.GOOS == "darwin" && strings.Contains(last.Error, "operation not permitted") {
					text += "\n" + dim("frost updated itself to "+v+". "+fdaHint)
				}
				b.failRow("last backup", text)
			} else if len(last.Missing) > 0 || last.Skipped > 0 || last.Kept > 0 {
				var buts []string
				if len(last.Missing) > 0 {
					buts = append(buts, "not found: "+missingList(last.Missing))
				}
				if last.Skipped > 0 {
					buts = append(buts, fmt.Sprintf("%d items couldn't be read", last.Skipped))
				}
				if last.Kept > 0 {
					buts = append(buts, fmt.Sprintf("%d busy files kept their previous copy", last.Kept))
				}
				b.warnRow("last backup", caution("ok, but "+strings.Join(buts, "; ")+" ")+ago(last.Time)+dim("  "+short.Of(last.SnapshotID)))
			} else if last.Unchanged {
				b.row("last backup", good("ok ")+ago(last.Time)+dim(", nothing new since "+short.Of(last.SnapshotID)))
			} else {
				b.row("last backup", good("ok ")+ago(last.Time)+dim("  "+short.Of(last.SnapshotID)))
			}

			// Next run.
			switch text, level := nextRun(a.cfg, e); level {
			case levelFail:
				b.failRow("next backup", text)
			case levelWarn:
				b.warnRow("next backup", text)
			default:
				b.row("next backup", text)
			}

			// Health.
			if v, ok := e.LastVerify(); !ok {
				b.row("health", dim("not checked yet"))
			} else if v.OK() {
				b.row("health", good("ok ")+fmt.Sprintf("%d objects checked %s", v.Checked, ago(v.Time)))
			} else {
				text := errStyle(fmt.Sprintf("%d of %d checks failed", len(v.Failures), v.Checked)) + " " + ago(v.Time)
				for _, f := range v.Failures {
					text += "\n" + printable(f)
				}
				text += "\n" + dim("Run a new backup to re-upload anything missing, then `frost status --verify`.")
				b.failRow("health", text)
			}

			if text, warn := updateSummary(a.cfg, update.LoadState(updateStatePath()), time.Now()); warn {
				b.warnRow("updates", caution(printable(text)))
			} else {
				b.row("updates", printable(text))
			}

			if len(snaps) > 0 {
				b.row("protected", fmt.Sprintf("%s files, %s, in %s",
					humanCount(snaps[0].Stats.Files), humanBytes(snaps[0].Stats.Bytes), plural(len(snaps), "snapshot")))
			}
			if gone > 0 {
				b.warnRow("missing", caution(engine.GoneText(gone)+".")+"\n"+
					dim("If you moved your backups, "+moveHint(a.cfg.Storage, e.Repo.Backend.String())+"."))
			}

			b.gap()
			printSnapshots(b, snaps, short, all)
			if verifyFailed {
				return errors.New("verification failed")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&verify, "verify", false, "run a verification now")
	cmd.Flags().BoolVarP(&all, "all", "a", false, "list every snapshot, not just the latest 10")
	return cmd
}

// printStorageProblem is `status` when the storage won't open: what's
// wrong, how to fix it, and whether backups are failing because of it.
func printStorageProblem(out io.Writer, err error) {
	b := newBlock(out)
	b.open("frost", Version)
	b.gap()
	lines := strings.Split(err.Error(), "\n")
	for i, l := range lines {
		lines[i] = printable(l)
	}
	b.failRow("storage", errStyle("problem: ")+strings.Join(lines, "\n"))
	if f := loadKnown().Failed; f != nil {
		b.failRow("last backup", errStyle("failed ")+ago(f.Time)+dim(", it couldn't open the storage either"))
	}
	b.gap()
}

// How much a status row needs attention.
const (
	levelOK = iota
	levelWarn
	levelFail
)

// nextRun estimates the next scheduled backup from the last one and the
// interval. OS schedulers don't expose this in a portable way.
func nextRun(cfg config.Config, e *engine.Engine) (string, int) {
	if !cfg.Schedule.Enabled {
		return dim("automatic backups are off (") + "frost config set schedule.enabled true" + dim(")"), levelOK
	}
	every, err := config.Interval(cfg.Schedule.Every)
	if err != nil {
		return errStyle(err.Error()), levelFail
	}
	if !scheduleInstalled() {
		return caution("scheduled job is missing, run `frost init` or `frost config set schedule.enabled true`"), levelWarn
	}
	how := dim("  " + cfg.Schedule.Every + " via " + scheduleKind())
	last, ok := e.LastBackup()
	if !ok {
		return "soon" + how, levelOK
	}
	return "~" + in(last.Time.Add(every)) + how, levelOK
}

// printSnapshots lists snaps, newest first, as the last section of b and
// closes it.
func printSnapshots(b *block, snaps []snapshot.Snapshot, short snapshot.ShortIDs, all bool) {
	if len(snaps) == 0 {
		b.close("No snapshots yet. Run " + bold("frost backup") + ".")
		return
	}
	shown := snaps
	if !all && len(shown) > 10 {
		shown = shown[:10]
	}
	idWidth := len("snapshot")
	for _, s := range shown {
		idWidth = max(idWidth, len(short.Of(s.ID)))
	}
	b.section("snapshots")
	b.gap()
	const columns = "%-*s    %-16s    %8s    %9s    %9s"
	b.line(dim(fmt.Sprintf(columns, idWidth, "snapshot", "taken", "files", "size", "new")))
	for _, s := range shown {
		b.line(fmt.Sprintf(columns, idWidth, short.Of(s.ID), when(s.Time),
			humanCount(s.Stats.Files), humanBytes(s.Stats.Bytes), humanBytes(s.Stats.NewBytes)))
	}
	if len(shown) < len(snaps) {
		b.line(dim(fmt.Sprintf("+%d", len(snaps)-len(shown))))
		b.gap()
		b.close("See all snapshots with " + bold("frost status --all"))
		return
	}
	b.close("")
}
