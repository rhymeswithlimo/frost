package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/rhymeswithlimo/frost/internal/config"
)

func newConfigCmd() *cobra.Command {
	var showSecrets bool
	cmd := &cobra.Command{
		Use:   "config [get <key> | set <key> <value...> | edit [editor]]",
		Short: "Read or change settings without rerunning init",
		Long: `With no arguments, prints every setting. Credentials are masked unless
you pass --show-secrets.

  get <key>             print one setting
  set <key> <value...>  change one setting (lists take one value per item)
  edit [editor]         open config.toml in $EDITOR, or the editor you name,
                        and save your changes once you type yes

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
			// edit comes first: it has to work on a file that doesn't parse.
			if len(args) > 0 && args[0] == "edit" {
				if len(args) > 2 {
					return errors.New("usage: frost config edit [editor]")
				}
				return configEdit(cmd, strings.Join(args[1:], ""))
			}
			out := cmd.OutOrStdout()
			cfg, err := config.LoadFile()
			if err != nil {
				return err
			}
			if len(args) == 0 {
				b := newBlock(out)
				b.open("config", tildify(config.Path()))
				b.gap()
				for _, k := range config.Keys() {
					b.width = max(b.width, len(k)+1)
				}
				for _, k := range config.Keys() {
					v, _ := cfg.Get(k)
					switch {
					case v == "":
						v = dim("not set")
					case config.IsSecret(k) && !showSecrets:
						v = "********"
					default:
						v = printable(strings.ReplaceAll(v, "\n", ", "))
					}
					b.row(k, v)
				}
				b.gap()
				b.close("Change one with " + bold("frost config set <key> <value>") + ".")
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
				if config.IsSecret(args[1]) && v != "" && !showSecrets {
					v = "********"
				}
				fmt.Fprintln(out, v)
				return nil

			case "set":
				if len(args) < 2 {
					return errors.New("usage: frost config set <key> <value...>")
				}
				was := cfg
				if err := cfg.Set(args[1], args[2:]); err != nil {
					return err
				}
				if err := cfg.Validate(); err != nil && !onlyUnrelated(err, args[1]) {
					return err
				}
				b := newBlock(out)
				b.open("config", tildify(config.Path()))
				b.gap()
				changes := configChanges(was, cfg)
				if len(changes) == 0 {
					b.close(args[1] + " is already set to that, so nothing changed.")
					return nil
				}
				printChanges(b, changes)
				if strings.HasPrefix(args[1], "storage.") {
					if err := checkStorageChange(cmd.Context(), b, was.Storage, cfg.Storage, false); err != nil {
						return err
					}
				}
				if err := config.Save(cfg); err != nil {
					return err
				}
				done, err := resync(was.Schedule, cfg)
				if err != nil {
					return err
				}
				b.gap()
				b.close(good("Saved.") + done)
				return nil
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

// resync updates the OS scheduled job if the schedule changed, and says
// what it did, to follow "Saved.".
func resync(before config.Schedule, cfg config.Config) (string, error) {
	if before == cfg.Schedule {
		return "", nil
	}
	if err := syncSchedule(cfg); err != nil {
		return "", fmt.Errorf("saved, but updating the scheduled job failed: %w", err)
	}
	if cfg.Schedule.Enabled {
		return dim(" Scheduled job updated: " + cfg.Schedule.Every + "."), nil
	}
	return dim(" Scheduled job removed."), nil
}

// configEdit opens a copy of config.toml in an editor, shows what changed,
// and saves the copy over config.toml once the user types yes. It works on
// the file's bytes, so it can fix a file that doesn't parse, and it saves
// them as written, comments and all.
func configEdit(cmd *cobra.Command, named string) error {
	path := config.Path()
	orig, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return config.ErrNoConfig
	}
	if err != nil {
		return err
	}
	editor, err := editorCommand(named)
	if err != nil {
		return err
	}
	was := config.Default()
	wasOK := config.Parse(orig, &was) == nil

	// The copy holds credentials, so it stays in the private config folder.
	dir, err := os.MkdirTemp(config.Dir(), ".frost-edit-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	draft := filepath.Join(dir, "config.toml")
	if err := config.WritePrivate(draft, orig); err != nil {
		return err
	}

	p := newPrompter(cmd)
	p.open("config", tildify(path))
	p.gap()
	var edited []byte
	now := config.Default()
	for {
		took, err := p.edit(editor, draft)
		if err != nil {
			return err
		}
		p.gap()
		if edited, err = os.ReadFile(draft); err != nil {
			return err
		}
		// Some editors hand the file to a window that's already open and
		// return straight away, before there's anything to read.
		if took < time.Second && bytes.Equal(edited, orig) {
			if _, err := p.ask(fmt.Sprintf("%s didn't wait for you. Save and close %s, then press enter.", editorName(editor), filepath.Base(draft)), ""); err != nil {
				return err
			}
			if edited, err = os.ReadFile(draft); err != nil {
				return err
			}
		}
		if wasOK && bytes.Equal(edited, orig) {
			p.close("No changes, so nothing was saved.")
			return nil
		}
		now = config.Default()
		if err = config.Parse(edited, &now); err == nil {
			break
		}
		p.fail(caution(err.Error()))
		again, err := p.yesNo("Open it again to fix it?", true)
		if err != nil {
			return err
		}
		if !again {
			return errors.New("cancelled, nothing was saved")
		}
		p.gap()
	}

	if !wasOK {
		p.ok("config.toml reads cleanly again.")
	} else if changes := configChanges(was, now); len(changes) > 0 {
		printChanges(p.block, changes)
	} else {
		p.line("No settings changed, only comments or layout.")
	}
	if err := now.Validate(); err != nil {
		p.warn(caution(err.Error()))
	}
	// Only a warning here. edit is how to make a change the check refuses.
	checkStorageChange(cmd.Context(), p.block, was.Storage, now.Storage, true)
	p.gap()
	if ok, err := p.confirm("yes", "save"); err != nil || !ok {
		return errors.Join(err, errors.New("cancelled, nothing was saved"))
	}
	if err := config.WritePrivate(path, edited); err != nil {
		return err
	}
	done, err := resync(was.Schedule, now)
	if err != nil {
		return err
	}
	p.gap()
	p.close(good("Saved.") + done)
	return nil
}

// edit opens path in editor and waits for it to close, saying what to do
// meanwhile. It returns how long the editor took.
func (p *prompter) edit(editor []string, path string) (time.Duration, error) {
	name := editorName(editor)
	p.line(dim(fmt.Sprintf("Opened %s in %s. Save your changes, then close %s to carry on.", filepath.Base(path), name, name)))
	start := time.Now()
	err := openEditor(editor, path)
	took := time.Since(start)
	if err != nil {
		return took, fmt.Errorf("%s didn't run: %w", name, err)
	}
	return took, nil
}

// openEditor runs editor on path and waits for it to exit. It's a variable
// so tests never start a real editor.
var openEditor = func(editor []string, path string) error {
	c := exec.Command(editor[0], slices.Concat(editor[1:], []string{path})...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}

// editorCommand is the editor `config edit` opens: the one named on the
// command line, then $VISUAL, then $EDITOR, then nano, vim or vi, or
// Notepad on Windows.
func editorCommand(named string) ([]string, error) {
	var parts []string
	for _, s := range []string{named, os.Getenv("VISUAL"), os.Getenv("EDITOR")} {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		if fi, err := os.Stat(s); err == nil && fi.Mode().IsRegular() {
			parts = []string{s} // a path with spaces in it
		} else {
			parts = strings.Fields(s)
		}
		break
	}
	if parts == nil {
		return []string{defaultEditor()}, nil
	}
	if _, err := exec.LookPath(parts[0]); err != nil {
		if runtime.GOOS == "windows" && isNano(parts[0]) {
			if p := gitNano(); p != "" {
				parts[0] = p
				return parts, nil
			}
		}
		return nil, fmt.Errorf("can't find the editor %q, check it's installed and on your PATH", parts[0])
	}
	return parts, nil
}

func defaultEditor() string {
	if runtime.GOOS == "windows" {
		return "notepad"
	}
	for _, e := range []string{"nano", "vim"} {
		if _, err := exec.LookPath(e); err == nil {
			return e
		}
	}
	return "vi"
}

func isNano(name string) bool {
	return strings.TrimSuffix(strings.ToLower(name), ".exe") == "nano"
}

// gitNano finds the nano that comes with Git for Windows. Outside Git Bash
// it isn't on PATH.
func gitNano() string {
	var roots []string
	for _, v := range []string{"ProgramFiles", "ProgramW6432"} {
		if dir := os.Getenv(v); dir != "" {
			roots = append(roots, filepath.Join(dir, "Git"))
		}
	}
	if dir := os.Getenv("LocalAppData"); dir != "" {
		roots = append(roots, filepath.Join(dir, "Programs", "Git"))
	}
	if git, err := exec.LookPath("git"); err == nil {
		// git.exe sits in <root>\cmd, <root>\bin or <root>\mingw64\bin.
		d := filepath.Dir(git)
		roots = append(roots, filepath.Dir(d), filepath.Dir(filepath.Dir(d)))
	}
	for _, r := range roots {
		p := filepath.Join(r, "usr", "bin", "nano.exe")
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

// editorName is how output refers to editor.
func editorName(editor []string) string {
	name := strings.TrimSuffix(filepath.Base(editor[0]), filepath.Ext(editor[0]))
	if strings.EqualFold(name, "notepad") {
		return "Notepad"
	}
	return name
}

// settingChange is a setting that differs between two configs, and how it
// changed, ready to show.
type settingChange struct{ key, how string }

// configChanges lists the settings that differ from was to now. Secrets
// only say they changed.
func configChanges(was, now config.Config) []settingChange {
	var out []settingChange
	for _, k := range config.Keys() {
		a, _ := was.Get(k)
		b, _ := now.Get(k)
		if a == b {
			continue
		}
		var how string
		switch {
		case config.IsSecret(k):
			how = "changed"
		case config.IsList(k):
			how = listChange(items(a), items(b))
		default:
			how = orNotSet(printable(a)) + dim(" to ") + bold(orNotSet(printable(b)))
		}
		out = append(out, settingChange{k, how})
	}
	return out
}

// listChange shows the items added to and removed from a list, one per line.
func listChange(was, now []string) string {
	var lines []string
	for _, it := range now {
		if !slices.Contains(was, it) {
			lines = append(lines, good("+")+" "+printable(it))
		}
	}
	for _, it := range was {
		if !slices.Contains(now, it) {
			lines = append(lines, errStyle("-")+" "+printable(it))
		}
	}
	if len(lines) == 0 {
		return "same items, new order"
	}
	return strings.Join(lines, "\n")
}

// items splits a list setting as Get returns it.
func items(v string) []string {
	if v == "" {
		return nil
	}
	return strings.Split(v, "\n")
}

func orNotSet(v string) string {
	if v == "" {
		return dim("not set")
	}
	return v
}

// printChanges prints changes as rows of b, aligned on the longest key.
func printChanges(b *block, changes []settingChange) {
	for _, c := range changes {
		b.width = max(b.width, len(c.key)+1)
	}
	for _, c := range changes {
		b.row(c.key, c.how)
	}
}
