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
	fmt.Fprintln(p.out, caution("Anyone who sees this phrase can decrypt all of your backups."))
	fmt.Fprintln(p.out, "Make sure nobody's looking at your screen and you're not screen sharing.")
	s, err := p.ask("Type "+bold("show")+" to continue:", "")
	if err != nil {
		return err
	}
	if s != "show" {
		return errors.New("cancelled")
	}
	fmt.Fprintf(p.out, "\n%s\n\n%s\n", heading("recovery phrase"), phraseGrid(k.Phrase()))
	fmt.Fprintln(p.out, dim("key fingerprint "+k.Fingerprint()))
	return nil
}

func keyVerify(cmd *cobra.Command) error {
	p := newPrompter(cmd)
	phrase, err := p.secret(bold("Recovery phrase:"), "")
	if err != nil {
		return err
	}
	k, err := phraseKey(phrase)
	if err != nil {
		return err
	}
	fmt.Fprintln(p.out, good("valid phrase ")+dim("fingerprint "+k.Fingerprint()))

	if local, err := loadKey(); err == nil {
		if local.Fingerprint() == k.Fingerprint() {
			fmt.Fprintln(p.out, good("matches ")+"the key on this machine")
		} else {
			fmt.Fprintln(p.out, errStyle("does NOT match ")+"the key on this machine "+dim("("+local.Fingerprint()+")"))
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return nil // nothing more to check against
	}
	b, err := newBackend(cfg.Storage)
	if err != nil {
		return err
	}
	switch _, err := repo.Open(cmd.Context(), b, k); {
	case err == nil:
		fmt.Fprintln(p.out, good("opens ")+"the repository at "+b.String())
	case errors.Is(err, repo.ErrWrongKey):
		fmt.Fprintln(p.out, errStyle("does NOT open ")+"the repository at "+b.String())
		return errors.New("key doesn't match")
	default:
		fmt.Fprintln(p.out, caution("couldn't check the repository: ")+err.Error())
	}
	return nil
}

func keyImport(cmd *cobra.Command) error {
	p := newPrompter(cmd)
	local, err := loadKey()
	if err != nil && !errors.Is(err, ErrNoKey) {
		fmt.Fprintln(p.out, caution("The existing key file is unreadable and will be replaced."))
	}

	phrase, err := p.secret(bold("Recovery phrase:"), "")
	if err != nil {
		return err
	}
	k, err := phraseKey(phrase)
	if err != nil {
		return err
	}
	if local != nil && local.Fingerprint() == k.Fingerprint() {
		fmt.Fprintln(p.out, good("That's already the key on this machine."))
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
		fmt.Fprintln(p.out, good("opens ")+"the repository at "+b.String())
	} else {
		fmt.Fprintln(p.out, dim("No config yet, so the phrase wasn't checked against any storage. Run `frost init` next."))
	}

	if local != nil {
		fmt.Fprintln(p.out, caution("This replaces the key currently on this machine ("+local.Fingerprint()+")."))
		fmt.Fprintln(p.out, "Backups made with the old key will need the old phrase to restore.")
		ok, err := p.yesNo("Replace it?", false)
		if err != nil || !ok {
			return errors.Join(err, errors.New("cancelled"))
		}
	}
	if err := saveKey(k); err != nil {
		return err
	}
	fmt.Fprintln(p.out, good("Key imported. ")+dim("fingerprint "+k.Fingerprint()))
	return nil
}
