package cli

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rhymeswithlimo/frost/internal/config"
	"github.com/rhymeswithlimo/frost/internal/crypto"
	"github.com/rhymeswithlimo/frost/internal/repo"
	"github.com/rhymeswithlimo/frost/internal/schedule"
	"github.com/rhymeswithlimo/frost/internal/storage"
	"github.com/rhymeswithlimo/frost/internal/tui"
)

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Set up frost: what to back up, where, and how often",
		Long: `Walks you through setup and writes config.toml: where backups go, which
folders, how often, and your recovery phrase. Run it again any time to review
or change your settings.

In a terminal it opens a full-screen setup. With piped input it asks plain
questions, one per line.

On new storage it generates your encryption key and shows the recovery phrase
once. On storage that already has backups it asks for the phrase instead.`,
		Args: cobra.NoArgs,
		RunE: runInit,
	}
}

func runInit(cmd *cobra.Command, _ []string) error {
	cfg, err := config.LoadFile()
	existing := err == nil
	if err != nil && !errors.Is(err, config.ErrNoConfig) {
		return err
	}
	local, err := loadKey()
	if err != nil && !errors.Is(err, ErrNoKey) {
		return err
	}
	in, inOK := cmd.InOrStdin().(*os.File)
	out, outOK := cmd.OutOrStdout().(*os.File)
	if inOK && outOK && isTerminal(in) && isTerminal(out) && ansiOK {
		return runSetupScreens(cmd, cfg, existing, local)
	}
	return runInitPrompts(cmd, cfg, existing, local)
}

// runSetupScreens is init in a terminal: the full-screen wizard.
func runSetupScreens(cmd *cobra.Command, cfg config.Config, existing bool, local *crypto.Key) error {
	out := cmd.OutOrStdout()
	deps := tui.SetupDeps{
		LocalKey: local,
		Connect: func(ctx context.Context, s config.Storage) (tui.RepoState, error) {
			_, st, err := connect(ctx, s, local)
			return st, err
		},
		NewKey:    newKey,
		Unlock:    unlock,
		Finish:    finishSetup,
		PickWords: pickWords,
		DirExists: func(p string) bool { return len(missingDirs([]string{p})) == 0 },
		Scheduler: schedule.Kind(),
	}
	res, err := tui.Setup(cmd.Context(), deps, cfg, existing)
	if err != nil {
		return err
	}
	if !res.Saved {
		fmt.Fprintln(out, dim("Setup closed. Nothing was changed."))
		return nil
	}
	fmt.Fprintln(out, good("frost is set up."))
	for _, r := range res.Rows {
		fmt.Fprintln(out, kv(r[0], r[1]))
	}
	fmt.Fprintf(out, "\nPreview your first backup with %s, or start it with %s.\n",
		bold("frost backup --dry-run"), bold("frost backup"))
	return nil
}

// finishSetup creates the repository if it's new, then saves the config and
// key and installs the schedule. It returns what it did, for display.
func finishSetup(ctx context.Context, cfg config.Config, key *crypto.Key, newRepo bool) ([][2]string, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if newRepo {
		b, err := newBackend(cfg.Storage)
		if err != nil {
			return nil, err
		}
		if _, err := repo.Init(ctx, b, key); err != nil {
			return nil, explainConnect(err)
		}
	}
	if err := config.Save(cfg); err != nil {
		return nil, err
	}
	if err := saveKey(key); err != nil {
		return nil, err
	}
	rows := [][2]string{
		{"config", tildify(config.Path())},
		{"key", tildify(config.KeyPath()) + " (readable only by you)"},
	}
	switch err := syncSchedule(cfg); {
	case err != nil:
		rows = append(rows, [2]string{"schedule", "not installed: " + err.Error()})
	case cfg.Schedule.Enabled:
		rows = append(rows, [2]string{"schedule", cfg.Schedule.Every + " via " + schedule.Kind()})
	default:
		rows = append(rows, [2]string{"schedule", "off"})
	}
	return rows, nil
}

// runInitPrompts is init when input is piped or there's no terminal:
// plain questions, one per line.
func runInitPrompts(cmd *cobra.Command, cfg config.Config, existing bool, local *crypto.Key) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	p := newPrompter(cmd)

	fmt.Fprintln(out, heading("frost setup"))
	if existing {
		fmt.Fprintln(out, dim("Existing config found. Press enter to keep a value."))
	}
	fmt.Fprintln(out)

	// 1. Where. It goes first because it's the step that can fail.
	var b storage.Backend
	var state tui.RepoState
	for {
		if err := askStorage(p, &cfg); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s ... ", dim("Connecting"))
		var err error
		if b, state, err = connect(ctx, cfg.Storage, local); err == nil {
			fmt.Fprintln(out, good("ok"))
			break
		}
		fmt.Fprintln(out, errStyle("failed"))
		fmt.Fprintln(out, caution("  "+err.Error()))
		fmt.Fprintln(out)
	}

	// 2. What. No default: frost shouldn't back up anything you didn't pick.
	fmt.Fprintln(out)
	for {
		list, err := p.list(bold("Folders to back up")+dim(" (full paths, comma separated)"), cfg.Paths)
		if err != nil {
			return err
		}
		var paths []string
		problem := ""
		for _, f := range list {
			clean, inside, err := config.AddPath(f, paths)
			if err != nil {
				problem = f + ": " + err.Error()
				break
			}
			var kept []string
			for i, p := range paths {
				if !slices.Contains(inside, i) {
					kept = append(kept, p)
				}
			}
			paths = append(kept, clean)
		}
		switch {
		case problem != "":
			fmt.Fprintln(out, caution("  "+problem))
			continue
		case len(paths) == 0:
			fmt.Fprintln(out, dim("  add at least one folder"))
			continue
		}
		cfg.Paths = paths
		missing := missingDirs(cfg.Paths)
		if len(missing) == 0 {
			break
		}
		fmt.Fprintln(out, caution("  not found: "+strings.Join(missing, ", ")))
		ok, err := p.yesNo("  Add anyway? They're skipped until they exist.", false)
		if err != nil {
			return err
		}
		if ok {
			break
		}
	}
	var err error
	for {
		if cfg.Exclude, err = p.list(bold("Skip files matching")+dim(" (comma separated, - for none)"), cfg.Exclude); err != nil {
			return err
		}
		bad := slices.IndexFunc(cfg.Exclude, func(pat string) bool { _, err := path.Match(pat, ""); return err != nil })
		if bad < 0 {
			break
		}
		fmt.Fprintln(out, caution(fmt.Sprintf("  %q isn't a valid pattern, check its brackets", cfg.Exclude[bad])))
	}

	// 3. When.
	fmt.Fprintln(out)
	if cfg.Schedule.Enabled, err = p.yesNo(bold("Back up automatically?"), cfg.Schedule.Enabled || !existing); err != nil {
		return err
	}
	if cfg.Schedule.Enabled {
		for {
			if cfg.Schedule.Every, err = p.ask(bold("How often?")+dim(" ("+strings.Join(config.Intervals, ", ")+")"), orDefault(cfg.Schedule.Every, "daily")); err != nil {
				return err
			}
			if _, err := config.Interval(cfg.Schedule.Every); err == nil {
				break
			}
			fmt.Fprintln(out, dim("  pick one of: "+strings.Join(config.Intervals, ", ")))
		}
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	// 4. The key.
	key, newRepo, err := promptKey(ctx, p, b, state, local)
	if err != nil {
		return err
	}

	// 5. Save and schedule.
	rows, err := finishSetup(ctx, cfg, key, newRepo)
	if err != nil {
		return err
	}
	fmt.Fprintln(out)
	for _, r := range rows {
		fmt.Fprintln(out, kv(r[0], r[1]))
	}
	fmt.Fprintf(out, "\n%s Preview your first backup with %s, or start it with %s.\n",
		good("Done."), bold("frost backup --dry-run"), bold("frost backup"))
	return nil
}

func askStorage(p *prompter, cfg *config.Config) error {
	options := []string{
		"Permafrost " + dim("(one access key, nothing else to set up)"),
		"S3-compatible bucket " + dim("(AWS, Backblaze B2, Cloudflare R2, Wasabi, MinIO, ...)"),
	}
	def := -1
	switch cfg.Storage.Backend {
	case "permafrost":
		def = 0
	case "s3":
		def = 1
	}
	i, err := p.choose(bold("Where should backups go?"), options, def)
	if err != nil {
		return err
	}
	s := &cfg.Storage
	switch i {
	case 0:
		s.Backend = "permafrost"
		if s.Permafrost.Token, err = p.secret("  Access key", s.Permafrost.Token); err != nil {
			return err
		}
	case 1:
		s.Backend = "s3"
		if s.S3.Endpoint, err = p.required("  Endpoint (e.g. s3.us-east-1.amazonaws.com)", s.S3.Endpoint); err != nil {
			return err
		}
		if s.S3.Region, err = p.ask("  Region (blank if your provider doesn't use one)", s.S3.Region); err != nil {
			return err
		}
		if s.S3.Bucket, err = p.required("  Bucket", s.S3.Bucket); err != nil {
			return err
		}
		if s.S3.Prefix, err = p.ask("  Folder inside the bucket (optional)", s.S3.Prefix); err != nil {
			return err
		}
		if s.S3.AccessKeyID, err = p.required("  Access key ID", s.S3.AccessKeyID); err != nil {
			return err
		}
		if s.S3.SecretAccessKey, err = p.secret("  Secret access key", s.S3.SecretAccessKey); err != nil {
			return err
		}
	}
	return nil
}

// promptKey sorts out the key for backend b, given what connect found
// there. newRepo says the repository still needs creating.
func promptKey(ctx context.Context, p *prompter, b storage.Backend, state tui.RepoState, local *crypto.Key) (key *crypto.Key, newRepo bool, err error) {
	switch state {
	case tui.RepoLocalOK:
		fmt.Fprintln(p.out, good("Your key on this machine opens this storage."))
		return local, false, nil
	case tui.RepoNeedsPhrase:
		fmt.Fprintln(p.out, "\nThis storage already has frost backups. Enter the recovery phrase to connect.")
		key, err = askPhraseFor(ctx, p, b)
		return key, false, err
	case tui.RepoLocalWrong:
		fmt.Fprintln(p.out, "\nThis storage has backups made with a different key than the one on this machine.")
		fmt.Fprintln(p.out, "Enter the recovery phrase for these backups, and frost will use that key here instead.")
		key, err = askPhraseFor(ctx, p, b)
		return key, false, err
	}
	if local != nil {
		return local, true, nil
	}
	if key, err = newKey(); err != nil {
		return nil, false, err
	}
	return key, true, showNewPhrase(p, key)
}

func askPhraseFor(ctx context.Context, p *prompter, b storage.Backend) (*crypto.Key, error) {
	for tries := 0; tries < 3; tries++ {
		phrase, err := p.secret(bold("Recovery phrase:"), "")
		if err != nil {
			return nil, err
		}
		key, err := phraseKey(phrase)
		if err != nil {
			fmt.Fprintln(p.out, errStyle("  "+err.Error()))
			continue
		}
		if err := opensRepo(ctx, b, key); err != nil {
			fmt.Fprintln(p.out, errStyle("  "+err.Error()))
			continue
		}
		return key, nil
	}
	return nil, errors.New("couldn't unlock the repository")
}

// showNewPhrase shows the recovery phrase once and makes the user prove
// they wrote it down.
func showNewPhrase(p *prompter, key *crypto.Key) error {
	out := p.out
	fmt.Fprintf(out, "\n%s\n\n", heading("your recovery phrase"))
	fmt.Fprint(out, phraseGrid(key.Phrase()))
	fmt.Fprintf(out, "\n%s\n", bold("Write these 24 words down and keep them somewhere safe."))
	fmt.Fprintln(out, "They're the only way to restore your files if this machine is lost.")
	fmt.Fprintln(out, "Nobody can recover them for you: not your storage provider, not us.")
	fmt.Fprintln(out)

	words := strings.Fields(key.Phrase())
	for {
		if _, err := p.ask(dim("Press enter once you've written them down."), ""); err != nil {
			return err
		}
		i, j := pickWords()
		a, err := p.ask(fmt.Sprintf("To check: what's word #%d?", i+1), "")
		if err != nil {
			return err
		}
		c, err := p.ask(fmt.Sprintf("And word #%d?", j+1), "")
		if err != nil {
			return err
		}
		if strings.EqualFold(a, words[i]) && strings.EqualFold(c, words[j]) {
			fmt.Fprintln(out, good("Correct."))
			return nil
		}
		fmt.Fprintln(out, caution("That doesn't match."))
		again, err := p.yesNo("See the words again?", true)
		if err != nil {
			return err
		}
		if again {
			fmt.Fprint(out, phraseGrid(key.Phrase()))
		}
	}
}

// Swapped out in tests.
var (
	newKey    = crypto.NewKey
	pickWords = randomWords
)

// randomWords picks two different word positions, in order.
func randomWords() (int, int) {
	i, j := rand.IntN(24), rand.IntN(24)
	for j == i {
		j = rand.IntN(24)
	}
	return min(i, j), max(i, j)
}

// phraseGrid lays the 24 words out in numbered columns.
func phraseGrid(phrase string) string {
	words := strings.Fields(phrase)
	var b strings.Builder
	rows := (len(words) + 3) / 4
	for r := range rows {
		b.WriteString("  ")
		for c := range 4 {
			i := c*rows + r
			if i < len(words) {
				fmt.Fprintf(&b, "%s %-10s", dim(fmt.Sprintf("%2d.", i+1)), words[i])
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// syncSchedule makes the OS scheduler match the config. It's a variable so
// tests can stub it out and never touch the real scheduler.
var syncSchedule = installSchedule

func installSchedule(cfg config.Config) error {
	if !cfg.Schedule.Enabled {
		return schedule.Remove()
	}
	every, err := config.Interval(cfg.Schedule.Every)
	if err != nil {
		return err
	}
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(bin); err == nil {
		bin = resolved
	}
	os.MkdirAll(config.CacheDir(), 0o700)
	job := schedule.Job{Binary: bin, Every: every, LogFile: logPath()}
	if d := os.Getenv("FROST_CONFIG_DIR"); d != "" {
		job.ConfigDir = d
	}
	return schedule.Install(job)
}

func missingDirs(paths []string) []string {
	var missing []string
	for _, p := range paths {
		if fi, err := os.Stat(config.Expand(p)); err != nil || !fi.IsDir() {
			missing = append(missing, p)
		}
	}
	return missing
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
