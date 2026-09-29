package cli

import (
	"context"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/rhymeswithlimo/frost/internal/tui"
	"github.com/rhymeswithlimo/frost/internal/update"
)

func newBrowseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "browse",
		Short: "Open the snapshot browser",
		Long: `Opens a full-screen browser for your snapshots: pick one by date, walk its
files as they were then, compare two snapshots, and choose what to restore.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runBrowser(cmd.Context()) },
	}
}

func runBrowser(ctx context.Context) error {
	a, err := openApp(ctx)
	if err != nil {
		return err
	}
	// Read what the browser needs, then let go of the manifest so a
	// scheduled backup isn't locked out while the browser is open.
	st := tui.StateFrom(a.engine)
	a.Close()
	st.Version = Version
	st.Updates, _ = updateSummary(a.cfg, update.LoadState(updateStatePath()), time.Now())
	st.Updates = strings.ReplaceAll(st.Updates, "`", "") // the TUI doesn't quote commands
	return tui.Run(ctx, a.engine.Repo, a.cfg, st)
}
