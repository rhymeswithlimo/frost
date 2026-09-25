package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rhymeswithlimo/frost/internal/config"
)

func newConfigCmd() *cobra.Command {
	var showSecrets bool
	cmd := &cobra.Command{
		Use:   "config [get <key> | set <key> <value...> | edit]",
		Short: "Read or change settings without rerunning init",
		Long: `With no arguments, prints every setting. Credentials are masked unless
you pass --show-secrets.

  get <key>             print one setting
  set <key> <value...>  change one setting (lists take one value per item)
  edit                  open config.toml in $EDITOR

Changing schedule.enabled or schedule.every updates the OS scheduled job.`,
		Example: `  frost config
  frost config get paths
  frost config set schedule.every 6h
  frost config set paths ~/Documents ~/Pictures
  frost config set exclude node_modules "*.iso"
  frost config edit`,
		Args:      cobra.ArbitraryArgs,
		ValidArgs: []string{"get", "set", "edit"},
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			cfg, err := config.LoadFile()
			if err != nil {
				return err
			}
			if len(args) == 0 {
				fmt.Fprintln(out, dim("# "+tildify(config.Path())))
				for _, k := range config.Keys() {
					v, _ := cfg.Get(k)
					if config.IsSecret(k) && v != "" && !showSecrets {
						v = "********"
					}
					fmt.Fprintf(out, "%-32s %s\n", k, strings.ReplaceAll(v, "\n", ", "))
				}
				return nil
			}

			switch args[0] {
			case "get":
				if len(args) != 2 {
					return errors.New("usage: frost config get <key>")
				}
				v, err := cfg.Get(args[1])
				if err != nil {
					return err
				}
				fmt.Fprintln(out, v)
				return nil

			case "set":
				if len(args) < 2 {
					return errors.New("usage: frost config set <key> <value...>")
				}
				before := cfg.Schedule
				if err := cfg.Set(args[1], args[2:]); err != nil {
					return err
				}
				if err := cfg.Validate(); err != nil && !onlyUnrelated(err, args[1]) {
					return err
				}
				if err := config.Save(cfg); err != nil {
					return err
				}
				fmt.Fprintln(out, good("saved ")+args[1])
				return resync(cmd, before, cfg)

			case "edit":
				before := cfg.Schedule
				if err := editFile(config.Path()); err != nil {
					return err
				}
				cfg, err = config.LoadFile()
				if err != nil {
					return fmt.Errorf("config.toml has a problem, fix it with `frost config edit`: %w", err)
				}
				if err := cfg.Validate(); err != nil {
					fmt.Fprintln(out, caution("warning: ")+err.Error())
				}
				return resync(cmd, before, cfg)
			}
			return fmt.Errorf("unknown config action %q (use get, set or edit)", args[0])
		},
	}
	cmd.Flags().BoolVar(&showSecrets, "show-secrets", false, "print credentials in full")
	return cmd
}

// onlyUnrelated lets `config set` save a change while some other setting is
// still incomplete, as long as the setting being changed is fine.
func onlyUnrelated(err error, key string) bool {
	return !strings.Contains(err.Error(), strings.Split(key, ".")[0])
}

func resync(cmd *cobra.Command, before config.Schedule, cfg config.Config) error {
	if before == cfg.Schedule {
		return nil
	}
	if err := syncSchedule(cfg); err != nil {
		return fmt.Errorf("saved, but updating the scheduled job failed: %w", err)
	}
	if cfg.Schedule.Enabled {
		fmt.Fprintln(cmd.OutOrStdout(), dim("scheduled job updated: "+cfg.Schedule.Every))
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), dim("scheduled job removed"))
	}
	return nil
}

func editFile(path string) error {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
		if runtime.GOOS == "windows" {
			editor = "notepad"
		}
		for _, e := range []string{"nano", "vim"} {
			if _, err := exec.LookPath(e); err == nil && runtime.GOOS != "windows" {
				editor = e
				break
			}
		}
	}
	parts := strings.Fields(editor)
	c := exec.Command(parts[0], slices.Concat(parts[1:], []string{path})...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}
