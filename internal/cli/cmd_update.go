package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/rhymeswithlimo/frost/internal/config"
	"github.com/rhymeswithlimo/frost/internal/update"
)

// Variables so tests never go online or replace the test binary.
var (
	latestRelease  = update.Latest
	installRelease = update.Install
	selfPath       = update.Executable
	canReplace     = update.CanReplace
)

// checkEvery is how often scheduled backups look for a new release. A bit
// under a day, so a daily backup that starts a few seconds early still checks.
const checkEvery = 20 * time.Hour

func updateStatePath() string { return filepath.Join(config.CacheDir(), "update.json") }

func newUpdateCmd() *cobra.Command {
	var check bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update frost to the latest release",
		Long: `Downloads the latest frost release, checks it was signed with the frost
release key, and replaces this binary with it. Your config, key and backups
aren't touched.

Scheduled backups do this on their own, at most once a day, unless
update.auto is false.`,
		Example: `  frost update
  frost update --check`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			if !update.Valid(Version) {
				return update.ErrDevBuild
			}
			exe, err := selfPath()
			if err != nil {
				return fmt.Errorf("can't find the frost binary: %w", err)
			}
			if !check {
				// Find out now, not after the download.
				if err := canReplace(exe); err != nil {
					return err
				}
			}

			path := updateStatePath()
			st := update.LoadState(path)
			rel, err := latestRelease(cmd.Context())
			if errors.Is(err, update.ErrNoRelease) {
				fmt.Fprintln(out, "frost "+Version+dim(", no releases have been published yet"))
				return nil
			}
			if err != nil {
				return err
			}
			st.Checked, st.Latest, st.Error = time.Now(), rel.Version, ""
			if !underSudo() {
				// As root it would leave a cache folder the user can't write to.
				defer func() { st.Save(path) }()
			}

			if !update.Newer(rel.Version, Version) {
				fmt.Fprintln(out, "frost "+Version+dim(" is the latest release"))
				return nil
			}
			fmt.Fprintf(out, "%s %s%s\n", heading("new release"), bold(rel.Version), dim(", you have "+Version))
			fmt.Fprintln(out, kv("notes", rel.Page))
			if check {
				fmt.Fprintln(out, kv("", dim("Run `frost update` to install it.")))
				return nil
			}
			fmt.Fprintln(out, kv("download", rel.Archive))
			if err := installRelease(cmd.Context(), rel, exe); err != nil {
				return err
			}
			st.Installed, st.From, st.InstalledAt = rel.Version, Version, time.Now()
			fmt.Fprintln(out, kv("installed", good("ok ")+printable(tildify(exe))))
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only say whether there's a newer release")
	return cmd
}

// underSudo reports whether this is `sudo frost ...`.
func underSudo() bool { return os.Geteuid() == 0 && os.Getenv("SUDO_UID") != "" }

// fdaHint is for macOS, which can treat a replaced binary as a new app.
const fdaHint = "If backups of protected folders start failing with \"operation not permitted\", turn frost off and on again under System Settings > Privacy & Security > Full Disk Access."

// autoUpdate runs after a scheduled backup. It never fails the backup:
// problems go to the log and show up in `frost status`.
func autoUpdate(ctx context.Context, out io.Writer) {
	if !update.Valid(Version) {
		return
	}
	path := updateStatePath()
	st := update.LoadState(path)
	now := time.Now()
	if now.Sub(st.Checked) < checkEvery && !st.Checked.After(now) {
		return
	}
	cfg, err := config.LoadFile()
	if err != nil {
		cfg = config.Default()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	logf := func(format string, args ...any) {
		fmt.Fprintf(out, "[%s] "+format+"\n", append([]any{time.Now().Format(time.RFC3339)}, args...)...)
	}

	st.Checked, st.Error = now, ""
	defer func() { st.Save(path) }()
	rel, err := latestRelease(ctx)
	if errors.Is(err, update.ErrNoRelease) {
		return
	}
	if err != nil {
		st.Error = err.Error()
		logf("update check failed: %v", err)
		return
	}
	st.Latest = rel.Version
	if !update.Newer(rel.Version, Version) {
		return
	}
	if !cfg.Update.Auto {
		logf("frost %s is available, run `frost update` to install it", rel.Version)
		return
	}
	exe, err := selfPath()
	if err == nil {
		err = installRelease(ctx, rel, exe)
	}
	if err != nil {
		st.Error = err.Error()
		logf("updating to %s failed: %v", rel.Version, err)
		return
	}
	st.Installed, st.From, st.InstalledAt = rel.Version, Version, time.Now()
	logf("updated frost from %s to %s", Version, rel.Version)
}

// updateSummary says how updates are set up and what happened last, for
// `frost status` and the browser's settings. It never goes online. warn
// means something needs the user.
func updateSummary(cfg config.Config, st update.State, now time.Time) (text string, warn bool) {
	if !update.Valid(Version) {
		return "not for development builds", false
	}
	mode := "off"
	if cfg.Update.Auto {
		mode = "automatic"
		if !cfg.Schedule.Enabled {
			mode = "automatic, but only after scheduled backups, which are off"
		}
	}
	auto := cfg.Update.Auto && cfg.Schedule.Enabled
	switch {
	case update.Newer(st.Latest, Version) && auto && st.Error != "":
		return fmt.Sprintf("%s is out, but the last update failed: %s. Run `frost update`", st.Latest, st.Error), true
	case update.Newer(st.Latest, Version) && auto:
		return fmt.Sprintf("%s, %s installs after the next backup", mode, st.Latest), false
	case update.Newer(st.Latest, Version):
		return fmt.Sprintf("%s, %s is out: run `frost update`", mode, st.Latest), true
	case auto && st.Error != "":
		return fmt.Sprintf("%s, the last check failed %s: %s", mode, relative(now.Sub(st.Checked))+" ago", st.Error), true
	case st.Installed == Version && st.From != "" && now.Sub(st.InstalledAt) < 7*24*time.Hour:
		return fmt.Sprintf("%s, updated from %s %s", mode, st.From, relative(now.Sub(st.InstalledAt))+" ago"), false
	}
	return mode, false
}

// recentlyUpdated returns the version frost updated itself to in the last
// month, if it did.
func recentlyUpdated() (string, bool) {
	st := update.LoadState(updateStatePath())
	if st.Installed == "" || st.Installed != Version || time.Since(st.InstalledAt) > 30*24*time.Hour {
		return "", false
	}
	return st.Installed, true
}
