package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/rhymeswithlimo/frost/internal/config"
	"github.com/rhymeswithlimo/frost/internal/repo"
)

func newKeyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "key <show | verify | import>",
		Short: "Show, check or import your recovery phrase",
		Long: `Your key is a 24 word recovery phrase. It's stored on this machine only,
in a file only you can read, and it never leaves it.

  show    print the recovery phrase (asks first)
  verify  type a phrase to check it matches this machine's key and your backups
  import  use an existing phrase on this machine, e.g. after a reinstall`,
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"show", "verify", "import"},
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "show":
				return keyShow(cmd)
			case "verify":
				return keyVerify(cmd)
			case "import":
				return keyImport(cmd)
			}
			return fmt.Errorf("unknown key action %q (use show, verify or import)", args[0])
		},
	}
	return cmd
}

func keyShow(cmd *cobra.Command) error {
	k, err := loadKey()
	if err != nil {
		return err
	}
	p := newPrompter(cmd)
	p.open("recovery phrase", "")
	p.gap()
	p.warn(caution("Anyone who sees this phrase can decrypt all of your backups."))
	p.line("Make sure nobody's looking at your screen and you're not screen sharing.")
	if ok, err := p.confirm("show", "continue"); err != nil || !ok {
		return errors.Join(err, errors.New("cancelled"))
	}
	p.gap()
	p.line(phraseGrid(k.Phrase()))
	p.gap()
	p.close(dim("key fingerprint " + k.Fingerprint()))
	return nil
}

func keyVerify(cmd *cobra.Command) error {
	p := newPrompter(cmd)
	p.open("key verify", "")
	p.gap()
	phrase, err := p.secret(bold("Recovery phrase:"), "")
	if err != nil {
		return err
	}
	k, err := phraseKey(phrase)
	if err != nil {
		return err
	}
	p.ok("valid phrase  " + dim("fingerprint "+k.Fingerprint()))
	var mismatch error

	if local, err := loadKey(); err == nil {
		if local.Equal(k) {
			p.ok("matches the key on this machine")
		} else {
			p.fail("doesn't match the key on this machine " + dim("("+local.Fingerprint()+")"))
			mismatch = errors.New("phrase doesn't match the local key")
		}
	} else if !errors.Is(err, ErrNoKey) {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		if errors.Is(err, config.ErrNoConfig) {
			return verified(p, mismatch)
		}
		return err
	}
	b, err := newBackend(cfg.Storage)
	if err != nil {
		return err
	}
	switch _, err := repo.Open(cmd.Context(), b, k); {
	case err == nil:
		p.ok("opens the repository at " + b.String())
	case errors.Is(err, repo.ErrWrongKey):
		p.fail("doesn't open the repository at " + b.String())
		return errors.New("key doesn't match")
	default:
		p.warn("couldn't check the repository: " + err.Error())
		return err
	}
	return verified(p, mismatch)
}

// verified closes `key verify`, unless a check failed and the error will.
func verified(p *prompter, mismatch error) error {
	p.gap()
	if mismatch == nil {
		p.close(good("The phrase checks out."))
	}
	return mismatch
}

func keyImport(cmd *cobra.Command) error {
	p := newPrompter(cmd)
	p.open("key import", "")
	p.gap()
	local, err := loadKey()
	if err != nil && !errors.Is(err, ErrNoKey) {
		p.warn(caution("The existing key file is unreadable and will be replaced."))
	}

	phrase, err := p.secret(bold("Recovery phrase:"), "")
	if err != nil {
		return err
	}
	k, err := phraseKey(phrase)
	if err != nil {
		return err
	}
	if local.Equal(k) {
		p.close(good("That's already the key on this machine."))
		return nil
	}

	if cfg, err := config.Load(); err == nil {
		b, err := newBackend(cfg.Storage)
		if err != nil {
			return err
		}
		if _, err := repo.Open(cmd.Context(), b, k); err != nil {
			return fmt.Errorf("this phrase can't open %s: %w", b, err)
		}
		p.ok("opens the repository at " + b.String())
	} else {
		if !errors.Is(err, config.ErrNoConfig) {
			return err
		}
		p.line(dim("No config yet, so the phrase wasn't checked against any storage. Run `frost init` next."))
	}

	if local != nil {
		p.warn(caution("This replaces the key currently on this machine (" + local.Fingerprint() + ")."))
		p.line("Backups made with the old key will need the old phrase to restore.")
		ok, err := p.yesNo("Replace it?", false)
		if err != nil || !ok {
			return errors.Join(err, errors.New("cancelled"))
		}
	}
	if err := saveKey(k); err != nil {
		return err
	}
	p.gap()
	p.close(good("Key imported. ") + dim("fingerprint "+k.Fingerprint()))
	return nil
}
