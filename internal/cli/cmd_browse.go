package cli

import (
	"context"
	"errors"
	"io"
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
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return runBrowser(cmd.Context(), cmd.OutOrStdout()) },
	}
}

func runBrowser(ctx context.Context, out io.Writer) error {
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
	err = tui.Run(ctx, a.engine.Repo, a.cfg, st)
	if stopped := (*tui.RestoreStopped)(nil); errors.As(err, &stopped) {
		single(out, caution("Restore stopped.")+" "+stopped.Advice)
		return nil
	}
	return err
}
