package cli

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rhymeswithlimo/frost/internal/config"
	"github.com/rhymeswithlimo/frost/internal/crypto"
	"github.com/rhymeswithlimo/frost/internal/repo"
	"github.com/rhymeswithlimo/frost/internal/schedule"
	"github.com/rhymeswithlimo/frost/internal/storage"
)

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Set up frost: what to back up, where, and how often",
		Long: `Walks you through setup and writes config.toml. Run it again any time to
change your answers. Your existing values are offered as defaults.

On a new repository it generates your encryption key and shows the recovery
phrase once. On an existing repository it asks for the phrase instead.`,
		Args: cobra.NoArgs,
		RunE: runInit,
	}
}

func runInit(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	p := newPrompter(cmd)

	cfg, err := config.LoadFile()
	existing := err == nil
	if err != nil && !errors.Is(err, config.ErrNoConfig) {
		return err
	}

	fmt.Fprintln(out, heading("frost setup"))
	if existing {
		fmt.Fprintln(out, dim("Existing config found. Press enter to keep a value."))
	}
	fmt.Fprintln(out)

	// 1. What to back up.
	def := cfg.Paths
	if len(def) == 0 {
		def = []string{"~/Documents"}
	}
	for {
		if cfg.Paths, err = p.list(bold("Directories to back up")+dim(" (comma separated)"), def); err != nil {
			return err
		}
		if missing := missingDirs(cfg.Paths); len(missing) > 0 {
			fmt.Fprintln(out, caution("  not found: "+strings.Join(missing, ", ")))
			continue
		}
		if len(cfg.Paths) > 0 {
			break
		}
	}
	if cfg.Exclude, err = p.list(bold("Skip files matching")+dim(" (comma separated, - for none)"), cfg.Exclude); err != nil {
		return err
	}

	// 2. When.
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
		}
	}

	// 3. Where.
	fmt.Fprintln(out)
	b, err := askStorage(p, &cfg)
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	// 4. Connect, and set up or import the key.
	fmt.Fprintf(out, "\n%s %s ... ", dim("Connecting to"), b)
	if err := probe(ctx, b); err != nil {
		fmt.Fprintln(out, errStyle("failed"))
		return fmt.Errorf("can't use %s: %w", b, err)
	}
	fmt.Fprintln(out, good("ok"))

	key, err := setupKey(ctx, p, b)
	if err != nil {
		return err
	}

	// 5. Save and schedule.
	if err := config.Save(cfg); err != nil {
		return err
	}
	if err := saveKey(key); err != nil {
		return err
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, kv("config", tildify(config.Path())))
	fmt.Fprintln(out, kv("key", tildify(config.KeyPath())+dim(" (readable only by you)")))

	if err := syncSchedule(cfg); err != nil {
		fmt.Fprintln(out, kv("schedule", errStyle("not installed: ")+err.Error()))
	} else if cfg.Schedule.Enabled {
		fmt.Fprintln(out, kv("schedule", cfg.Schedule.Every+dim(" via "+schedule.Kind())))
	} else {
		fmt.Fprintln(out, kv("schedule", "off"))
	}

	fmt.Fprintf(out, "\n%s Preview your first backup with %s, or start it with %s.\n",
		good("Done."), bold("frost backup --dry-run"), bold("frost backup"))
	return nil
}

func askStorage(p *prompter, cfg *config.Config) (storage.Backend, error) {
	options := []string{
		"S3-compatible bucket " + dim("(AWS, Backblaze B2, Cloudflare R2, Wasabi, MinIO, ...)"),
		"Permafrost " + dim("(hosted storage, see docs/PERMAFROST.md)"),
	}
	if cfg.Storage.Backend != "" {
		fmt.Fprintln(p.out, dim("Currently using "+cfg.Storage.Backend+"."))
	}
	i, err := p.choose(bold("Where should backups go?"), options)
	if err != nil {
		return nil, err
	}
	s := &cfg.Storage
	switch i {
	case 0:
		s.Backend = "s3"
		if s.S3.Endpoint, err = p.required("  Endpoint (e.g. s3.us-east-1.amazonaws.com)", s.S3.Endpoint); err != nil {
			return nil, err
		}
		if s.S3.Region, err = p.ask("  Region (blank if your provider doesn't use one)", s.S3.Region); err != nil {
			return nil, err
		}
		if s.S3.Bucket, err = p.required("  Bucket", s.S3.Bucket); err != nil {
			return nil, err
		}
		if s.S3.Prefix, err = p.ask("  Folder inside the bucket (optional)", s.S3.Prefix); err != nil {
			return nil, err
		}
		if s.S3.AccessKeyID, err = p.required("  Access key ID", s.S3.AccessKeyID); err != nil {
			return nil, err
		}
		if s.S3.SecretAccessKey, err = p.secret("  Secret access key", s.S3.SecretAccessKey); err != nil {
			return nil, err
		}
	case 1:
		s.Backend = "permafrost"
		if s.Permafrost.URL, err = p.required("  Permafrost URL", s.Permafrost.URL); err != nil {
			return nil, err
		}
		if s.Permafrost.Token, err = p.secret("  API token", s.Permafrost.Token); err != nil {
			return nil, err
		}
	}
	return newBackend(cfg.Storage)
}

// probe checks the backend is reachable and writable.
func probe(ctx context.Context, b storage.Backend) error {
	const k = "frost.probe"
	if err := b.Put(ctx, k, []byte("ok")); err != nil {
		return err
	}
	if _, err := b.Get(ctx, k); err != nil {
		return err
	}
	return b.Delete(ctx, k)
}

// setupKey returns the key to use with backend b, creating a repository and
// a new key if there isn't one yet.
func setupKey(ctx context.Context, p *prompter, b storage.Backend) (*crypto.Key, error) {
	local, err := loadKey()
	if err != nil && !errors.Is(err, ErrNoKey) {
		return nil, err
	}

	// Existing repository: the key must match it.
	if local != nil {
		if _, err := repo.Open(ctx, b, local); err == nil {
			fmt.Fprintln(p.out, good("Your key on this machine opens this repository."))
			return local, nil
		} else if !errors.Is(err, repo.ErrNotInitialized) {
			if errors.Is(err, repo.ErrWrongKey) {
				return nil, fmt.Errorf("%s already holds backups made with a different key; import that key with `frost key import`", b)
			}
			return nil, err
		}
	} else if exists, err := repo.Exists(ctx, b); err != nil {
		return nil, err
	} else if exists {
		fmt.Fprintln(p.out, "\nThis storage already has frost backups. Enter the recovery phrase to connect.")
		return askPhraseFor(ctx, p, b)
	}

	// New repository.
	key := local
	if key == nil {
		if key, err = newKey(); err != nil {
			return nil, err
		}
		if err := showNewPhrase(p, key); err != nil {
			return nil, err
		}
	}
	if _, err := repo.Init(ctx, b, key); err != nil {
		return nil, err
	}
	return key, nil
}

func askPhraseFor(ctx context.Context, p *prompter, b storage.Backend) (*crypto.Key, error) {
	for tries := 0; tries < 3; tries++ {
		phrase, err := p.required(bold("Recovery phrase:"), "")
		if err != nil {
			return nil, err
		}
		key, err := crypto.KeyFromPhrase(phrase)
		if err != nil {
			fmt.Fprintln(p.out, errStyle("  "+err.Error()))
			continue
		}
		if _, err := repo.Open(ctx, b, key); err != nil {
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
		fmt.Fprintln(out, caution("That doesn't match. Here are the words again:"))
		fmt.Fprint(out, phraseGrid(key.Phrase()))
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
