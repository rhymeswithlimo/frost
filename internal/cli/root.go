// Package cli wires frost's commands together.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rhymeswithlimo/frost/internal/config"
	"github.com/rhymeswithlimo/frost/internal/storage/permafrost"
	"github.com/rhymeswithlimo/frost/internal/update"
)

// Version is set at build time with -ldflags "-X .../cli.Version=v1.2.3".
var Version = "dev"

// NewRoot builds the root command.
func NewRoot() *cobra.Command {
	var configDir string
	var cacheDir string
	blockOpen = false
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
			if cacheDir != "" {
				os.Setenv("FROST_CACHE_DIR", cacheDir)
			}
		},
	}
	root.PersistentFlags().StringVar(&configDir, "config-dir", "", "use a different config directory (default "+tildify(config.Dir())+")")
	root.PersistentFlags().StringVar(&cacheDir, "cache-dir", "", "use this cache directory for a scheduled run")
	root.PersistentFlags().MarkHidden("cache-dir")
	cobra.EnableCommandSorting = false
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetHelpCommand(&cobra.Command{Hidden: true})
	// Every -h shows the same help, with all of it.
	root.SetHelpFunc(func(*cobra.Command, []string) {
		rootHelp(root, terminalWidth(root.OutOrStdout()))
	})

	root.AddCommand(
		newInitCmd(),
		newBackupCmd(),
		newRestoreCmd(),
		newStatusCmd(),
		newBrowseCmd(),
		newConfigCmd(),
		newKeyCmd(),
		newUpdateCmd(),
	)
	return root
}

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	defer enableANSI()()
	update.UserAgent = "frost/" + Version
	if runtime.GOOS == "windows" {
		// The binary an update replaced can only be deleted once it has exited.
		if exe, err := update.Executable(); err == nil {
			update.Cleanup(exe)
		}
	}
	if err := NewRoot().ExecuteContext(ctx); err != nil {
		// An error that ends a block on the screen closes it. Otherwise it
		// stands alone, spaced like a block.
		closes := blockOpen && isTerminal(os.Stdout) && isTerminal(os.Stderr)
		s := errorLine(err, closes)
		if !closes {
			s = "\n" + s
		}
		fmt.Fprintln(os.Stderr, s+"\n")
		return 1
	}
	return 0
}

// errorLine is err as frost prints it, closing an open block if closes.
func errorLine(err error, closes bool) string {
	s := errStyle("error:") + " " + errorText(err)
	if closes {
		return closeLine(s)
	}
	return s
}

// errorText is err for the terminal. Line breaks frost put in to lay out a
// longer explanation stay; other control characters don't.
func errorText(err error) string {
	lines := strings.Split(plainError(err).Error(), "\n")
	for i, l := range lines {
		lines[i] = printable(l)
	}
	return strings.Join(lines, "\n")
}

// plainError swaps errors that need the user to do something for words
// that say what. Anything else comes back unchanged.
func plainError(err error) error {
	if errors.Is(err, permafrost.ErrUnauthorized) {
		return errors.New("the Permafrost access key was rejected, it may be wrong or expired. Run `frost init` and set up storage again to get a working one")
	}
	return err
}
