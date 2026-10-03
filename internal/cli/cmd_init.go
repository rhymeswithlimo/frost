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
	"time"

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
	existing := err == nil && len(cfg.Paths) > 0 // a key saved mid-setup doesn't count
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
		Elsewhere: backupsElsewhere,
		Checkout:  checkout,
	}
	res, err := tui.Setup(cmd.Context(), deps, cfg, existing)
	if err != nil {
		return err
	}
	if !res.Saved {
		single(out, dim("Setup closed. Nothing was changed."))
		return nil
	}
	b := newBlock(out)
	b.open("frost setup", "")
	b.gap()
	for _, r := range res.Rows {
		b.row(r[0], r[1])
	}
	b.gap()
	b.close(setUp())
	return nil
}

// setUp is the line that closes a finished setup.
func setUp() string {
	return fmt.Sprintf("%s Preview your first backup with %s, or start it with %s.",
		good("frost is set up."), bold("frost backup --dry-run"), bold("frost backup"))
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
		rows = append(rows, [2]string{"schedule", cfg.Schedule.Every + " via " + scheduleKind()})
	default:
		rows = append(rows, [2]string{"schedule", "off"})
	}
	return rows, nil
}

// runInitPrompts is init when input is piped or there's no terminal:
// plain questions, one per line.
func runInitPrompts(cmd *cobra.Command, cfg config.Config, existing bool, local *crypto.Key) error {
	ctx := cmd.Context()
	p := newPrompter(cmd)

	p.open("frost setup", "")
	if existing {
		p.line(dim("Existing config found. Press enter to keep a value."))
	}
	p.gap()

	// 1. Where. It goes first because it's the step that can fail.
	var b storage.Backend
	var state tui.RepoState
	for {
		if err := askStorage(ctx, p, &cfg); err != nil {
			return err
		}
		p.question(dim("Connecting ..."))
		var err error
		if b, state, err = connect(ctx, cfg.Storage, local); err == nil {
			fmt.Fprintln(p.out, good("ok"))
			break
		}
		fmt.Fprintln(p.out, errStyle("failed"))
		p.fail(caution(err.Error()))
		p.gap()
	}

	// 2. What. No default: frost shouldn't back up anything you didn't pick.
	p.gap()
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
			p.warn(caution(problem))
			continue
		case len(paths) == 0:
			p.line(dim("  add at least one folder"))
			continue
		}
		cfg.Paths = paths
		missing := missingDirs(cfg.Paths)
		if len(missing) == 0 {
			break
		}
		p.warn(caution("not found: " + strings.Join(missing, ", ")))
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
		p.warn(caution(fmt.Sprintf("%q isn't a valid pattern, check its brackets", cfg.Exclude[bad])))
	}

	// 3. When.
	p.gap()
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
			p.line(dim("  pick one of: " + strings.Join(config.Intervals, ", ")))
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
	p.gap()
	for _, r := range rows {
		p.row(r[0], r[1])
	}
	p.gap()
	p.close(setUp())
	return nil
}

func askStorage(ctx context.Context, p *prompter, cfg *config.Config) error {
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
		if s.Permafrost.Token == "" {
			have, err := p.choose("  Do you have a Permafrost access key?", []string{"I have a key", "I don't have a key yet"}, 0)
			if err != nil {
				return err
			}
			if have == 1 {
				if err := getToken(ctx, p, s); err != nil {
					return err
				}
				if s.Permafrost.Token != "" {
					return nil
				}
			}
		}
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
		if s.S3.Prefix, err = p.ask("  Folder inside the bucket (/ for the top level)", s.S3.Prefix); err != nil {
			return err
		}
		s.S3.Prefix = strings.Trim(s.S3.Prefix, "/")
		if s.S3.AccessKeyID, err = p.required("  Access key ID", s.S3.AccessKeyID); err != nil {
			return err
		}
		if s.S3.SecretAccessKey, err = p.secret("  Secret access key", s.S3.SecretAccessKey); err != nil {
			return err
		}
	}
	return nil
}

// getToken opens the page for getting a key and waits for it to come back.
// On failure it says why, and the caller asks for a key to paste instead.
func getToken(ctx context.Context, p *prompter, s *config.Storage) error {
	page, wait, err := checkout(ctx, *s)
	if err != nil {
		p.warn(caution(err.Error()))
		return nil
	}
	p.line("  Grab one in your browser.")
	p.line(fmt.Sprintf("  %s %s%s", dim("If it didn't open, go to"), bold(page), dim(", then paste the key below.")))
	p.question("  " + dim("Waiting ..."))
	token, err := wait()
	if token != "" {
		s.Permafrost.Token = token
		fmt.Fprintln(p.out, good("got your access key"))
	} else {
		fmt.Fprintln(p.out, errStyle("stopped"))
	}
	if err != nil {
		p.warn(caution(err.Error()))
	}
	if token == "" {
		p.line(dim("  Paste your access key, or press ctrl+c and run frost init again to retry."))
	}
	return nil
}

// promptKey sorts out the key for backend b, given what connect found
// there. newRepo says the repository still needs creating.
func promptKey(ctx context.Context, p *prompter, b storage.Backend, state tui.RepoState, local *crypto.Key) (key *crypto.Key, newRepo bool, err error) {
	switch state {
	case tui.RepoLocalOK:
		p.gap()
		p.ok("Your key on this machine opens this storage.")
		return local, false, nil
	case tui.RepoNeedsPhrase:
		p.gap()
		p.line("This storage already has frost backups. Enter the recovery phrase to connect.")
		key, err = askPhraseFor(ctx, p, b)
		return key, false, err
	case tui.RepoLocalWrong:
		p.gap()
		p.line("This storage has backups made with a different key than the one on this machine.")
		p.line("Enter the recovery phrase for these backups, and frost will use that key here instead.")
		key, err = askPhraseFor(ctx, p, b)
		return key, false, err
	}
	if local != nil {
		if k := loadKnown(); k.Where != "" && k.Where != storage.Location(b) {
			p.gap()
			p.warn(fmt.Sprintf("There are no backups in %s yet. This machine's backups are in %s.", b, k.Shown))
			p.line("They stay there, but frost will only show the ones made here from now on, and the first backup uploads everything again.")
			ok, err := p.yesNo("Start a separate set of backups here?", false)
			if err != nil {
				return nil, false, err
			}
			if !ok {
				return nil, false, errors.New("nothing was changed. To keep using your backups, run `frost init` again and point it at " + k.Shown)
			}
		}
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
			p.fail(err.Error())
			continue
		}
		if err := opensRepo(ctx, b, key); err != nil {
			p.fail(err.Error())
			continue
		}
		return key, nil
	}
	return nil, errors.New("couldn't unlock the repository")
}

// showNewPhrase shows the recovery phrase once and makes the user prove
// they wrote it down.
func showNewPhrase(p *prompter, key *crypto.Key) error {
	p.gap()
	p.section("your recovery phrase")
	p.gap()
	p.line(phraseGrid(key.Phrase()))
	p.gap()
	p.line(bold("Write these 24 words down and keep them somewhere safe."))
	p.line("They're the only way to restore your files if this machine is lost.")
	p.line("Nobody can recover them for you: not your storage provider, not us.")
	p.gap()

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
			p.ok("Correct.")
			return nil
		}
		p.warn(caution("That doesn't match."))
		again, err := p.yesNo("See the words again?", true)
		if err != nil {
			return err
		}
		if again {
			p.gap()
			p.line(phraseGrid(key.Phrase()))
			p.gap()
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

// phraseGrid lays the 24 words out in numbered columns, one row per line.
func phraseGrid(phrase string) string {
	words := strings.Fields(phrase)
	rows := make([]string, (len(words)+3)/4)
	for r := range rows {
		var b strings.Builder
		for c := range 4 {
			i := c*len(rows) + r
			if i < len(words) {
				fmt.Fprintf(&b, "%s %-10s", dim(fmt.Sprintf("%2d.", i+1)), words[i])
			}
		}
		rows[r] = strings.TrimRight(b.String(), " ")
	}
	return strings.Join(rows, "\n")
}

// syncSchedule makes the OS scheduler match the config. It's a variable so
// tests can stub it out and never touch the real scheduler.
var (
	syncSchedule      = installSchedule
	scheduleKind      = schedule.Kind
	scheduleInstalled = schedule.Installed
)

func installSchedule(cfg config.Config) error {
	if !cfg.Schedule.Enabled {
		return schedule.Remove()
	}
	every, err := config.Interval(cfg.Schedule.Every)
	if err != nil {
		return err
	}
	job, err := scheduledJob(every)
	if err != nil {
		return err
	}
	return schedule.Install(job)
}

// scheduledJob keeps the directories used at setup, even without its environment.
func scheduledJob(every time.Duration) (schedule.Job, error) {
	var job schedule.Job
	bin, err := os.Executable()
	if err != nil {
		return job, err
	}
	if resolved, err := filepath.EvalSymlinks(bin); err == nil {
		bin = resolved
	}
	if err := os.MkdirAll(config.CacheDir(), 0o700); err != nil {
		return job, err
	}
	log, err := filepath.Abs(logPath())
	if err != nil {
		return job, err
	}
	job = schedule.Job{Binary: bin, Every: every, LogFile: log}
	job.ConfigDir, err = filepath.Abs(config.Dir())
	if err != nil {
		return job, err
	}
	job.CacheDir, err = filepath.Abs(config.CacheDir())
	return job, err
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
