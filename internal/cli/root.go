// Package cli wires frost's seven commands together.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/spf13/cobra"

	"github.com/rhymeswithlimo/frost/internal/config"
	"github.com/rhymeswithlimo/frost/internal/storage/permafrost"
)

// Version is set at build time with -ldflags "-X .../cli.Version=v1.2.3".
var Version = "dev"

// NewRoot builds the root command.
func NewRoot() *cobra.Command {
	var configDir string
	root := &cobra.Command{
		Use:   "frost",
		Short: "Encrypted, incremental backups to storage you choose",
		Long: `frost backs up your directories to S3-compatible storage or Permafrost.
Everything is encrypted on this machine before it's uploaded. Nobody else
can read your files, not the storage provider and not the frost authors.`,
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			if configDir != "" {
				os.Setenv("FROST_CONFIG_DIR", configDir)
			}
		},
	}
	root.PersistentFlags().StringVar(&configDir, "config-dir", "", "use a different config directory (default "+tildify(config.Dir())+")")
	cobra.EnableCommandSorting = false
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetHelpCommand(&cobra.Command{Hidden: true})

	root.AddCommand(
		newInitCmd(),
		newBackupCmd(),
		newRestoreCmd(),
		newStatusCmd(),
		newBrowseCmd(),
		newConfigCmd(),
		newKeyCmd(),
	)
	return root
}

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	defer enableANSI()()
	if err := NewRoot().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, errStyle("error:"), plainError(err))
		return 1
	}
	return 0
}

// plainError swaps errors that need the user to do something for words
// that say what. Anything else comes back unchanged.
func plainError(err error) error {
	if errors.Is(err, permafrost.ErrUnauthorized) {
		return errors.New("Permafrost didn't accept your access key, it may be wrong or expired. Run `frost init` and set up storage again to get a working one")
	}
	return err
}
