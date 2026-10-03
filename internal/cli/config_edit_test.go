package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/rhymeswithlimo/frost/internal/config"
)

// stubEditor replaces the editor with edits, one per time it opens, each
// turning the file's text into new text. It records the files it opened.
func stubEditor(t *testing.T, edits ...func(string) string) *[]string {
	t.Helper()
	var opened []string
	openEditor = func(_ []string, path string) error {
		opened = append(opened, path)
		if len(opened) > len(edits) {
			return errors.New("editor opened too many times")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(path, []byte(edits[len(opened)-1](string(raw))), 0o600)
	}
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	t.Cleanup(func() { openEditor = defaultOpenEditor })
	return &opened
}

var defaultOpenEditor = openEditor

func replace(old, new string) func(string) string {
	return func(s string) string { return strings.Replace(s, old, new, 1) }
}

func configFile(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// noDrafts checks config edit cleaned up its copy of the config.
func noDrafts(t *testing.T) {
	t.Helper()
	entries, _ := os.ReadDir(config.Dir())
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".frost-edit-") {
			t.Fatalf("left %s behind", e.Name())
		}
	}
}

func TestConfigEditSavesOnlyAfterYes(t *testing.T) {
	f := setup(t)
	must(t, f.initAnswers(f.phrase[2], f.phrase[17]), "init")
	before := configFile(t)
	edit := replace(`every = "6h"`, "# mine\nevery = \"daily\"")

	// Anything but yes keeps the file as it was.
	stubEditor(t, edit)
	if _, err := run(t, "y\n", "config", "edit"); err == nil || !strings.Contains(err.Error(), "nothing was saved") {
		t.Fatalf("edit without yes: %v", err)
	}
	if configFile(t) != before || len(f.sched) != 1 {
		t.Fatal("edit without yes changed something")
	}
	noDrafts(t)

	// yes saves the file as written, comments too, and resyncs the schedule.
	opened := stubEditor(t, edit)
	out := must(t, "yes\n", "config", "edit")
	for _, want := range []string{"Opened config.toml in", "schedule.every", "6h to daily", "Type yes to save", "Saved."} {
		if !strings.Contains(out, want) {
			t.Fatalf("edit output is missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(configFile(t), "# mine") {
		t.Fatal("edit didn't save the file as written")
	}
	if len(f.sched) != 2 || f.sched[1].Schedule.Every != "daily" {
		t.Fatalf("schedule not resynced: %+v", f.sched)
	}
	// The copy holds credentials, so it stays in the private config folder.
	if d := filepath.Dir(filepath.Dir((*opened)[0])); d != config.Dir() || filepath.Base((*opened)[0]) != "config.toml" {
		t.Fatalf("edited %s, want a config.toml inside %s", (*opened)[0], config.Dir())
	}
	noDrafts(t)
}

func TestConfigEditWithoutChanges(t *testing.T) {
	f := setup(t)
	must(t, f.initAnswers(f.phrase[2], f.phrase[17]), "init")
	stubEditor(t, func(s string) string { return s })
	out := must(t, "\n", "config", "edit")
	if !strings.Contains(out, "No changes") || strings.Contains(out, "Type yes") {
		t.Fatalf("unchanged edit:\n%s", out)
	}
	noDrafts(t)
}

// saveLater edits the file an editor opened when frost first reads input,
// like an editor that returned straight away but is still open.
type saveLater struct {
	opened *[]string
	edit   func(string) string
	in     io.Reader
	saved  bool
}

func (s *saveLater) Read(b []byte) (int, error) {
	if !s.saved {
		s.saved = true
		path := (*s.opened)[0]
		raw, _ := os.ReadFile(path)
		os.WriteFile(path, []byte(s.edit(string(raw))), 0o600)
	}
	return s.in.Read(b)
}

func TestConfigEditWaitsForEditorsThatDont(t *testing.T) {
	f := setup(t)
	must(t, f.initAnswers(f.phrase[2], f.phrase[17]), "init")
	opened := stubEditor(t, func(s string) string { return s })
	root := NewRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(&saveLater{opened: opened, edit: replace(`every = "6h"`, `every = "daily"`), in: strings.NewReader("\nyes\n")})
	root.SetArgs([]string{"config", "edit"})
	if err := root.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "didn't wait for you") || !strings.Contains(out.String(), "6h to daily") {
		t.Fatalf("edit with an editor that didn't wait:\n%s", out.String())
	}
	if !strings.Contains(configFile(t), `every = "daily"`) {
		t.Fatal("the change made after the editor returned wasn't saved")
	}
	noDrafts(t)
}

func TestConfigEditMasksSecrets(t *testing.T) {
	f := setup(t)
	must(t, f.initAnswers(f.phrase[2], f.phrase[17]), "init")
	stubEditor(t, replace(`"SECRET"`, `"NEWSECRET"`))
	out := must(t, "yes\n", "config", "edit")
	if !strings.Contains(out, "storage.s3.secret_access_key") || !strings.Contains(out, "changed") || strings.Contains(out, "SECRET") {
		t.Fatalf("secret change:\n%s", out)
	}
	if !strings.Contains(configFile(t), `"NEWSECRET"`) {
		t.Fatal("secret change wasn't saved")
	}
}

func TestConfigEditFixesBrokenFiles(t *testing.T) {
	f := setup(t)
	must(t, f.initAnswers(f.phrase[2], f.phrase[17]), "init")
	good := configFile(t)
	broken := func(string) string { return good + "\nnot toml\n" }

	// A mistake reopens the editor, and a fix can then be saved.
	stubEditor(t, broken, func(string) string { return good + "\n# fixed\n" })
	out := must(t, "y\nyes\n", "config", "edit")
	if !strings.Contains(out, "Open it again to fix it?") || !strings.Contains(configFile(t), "# fixed") {
		t.Fatalf("fixing a mistake:\n%s", out)
	}

	// Saying no to reopening keeps the file as it was.
	stubEditor(t, broken)
	if _, err := run(t, "n\n", "config", "edit"); err == nil || !strings.Contains(err.Error(), "nothing was saved") {
		t.Fatalf("giving up on a mistake: %v", err)
	}
	if configFile(t) != good+"\n# fixed\n" {
		t.Fatal("a broken edit was saved")
	}

	// A file that's already broken can still be opened and fixed.
	os.WriteFile(config.Path(), []byte(good+"\nnot toml\n"), 0o600)
	if _, err := run(t, "", "config"); err == nil {
		t.Fatal("config read a broken file")
	}
	stubEditor(t, func(string) string { return good })
	out = must(t, "yes\n", "config", "edit")
	if !strings.Contains(out, "reads cleanly again") || configFile(t) != good {
		t.Fatalf("fixing a broken file:\n%s", out)
	}
	noDrafts(t)
}

func TestConfigSetShowsTheChange(t *testing.T) {
	f := setup(t)
	must(t, f.initAnswers(f.phrase[2], f.phrase[17]), "init")
	out := must(t, "", "config", "set", "schedule.every", "daily")
	if !strings.Contains(out, "6h to daily") || !strings.Contains(out, "Saved.") {
		t.Fatalf("config set:\n%s", out)
	}
	out = must(t, "", "config", "set", "schedule.every", "daily")
	if !strings.Contains(out, "already set") || len(f.sched) != 2 {
		t.Fatalf("config set to the same value:\n%s", out)
	}
	out = must(t, "", "config", "set", "exclude", "*.tmp", "node_modules")
	if !strings.Contains(out, "+ node_modules") {
		t.Fatalf("config set on a list:\n%s", out)
	}
	out = must(t, "", "config", "set", "storage.s3.secret_access_key", "NEWSECRET")
	if !strings.Contains(out, "changed") || strings.Contains(out, "SECRET") {
		t.Fatalf("config set on a secret:\n%s", out)
	}
}

func TestEditorChoice(t *testing.T) {
	bin := t.TempDir()
	name := "fakeedit"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(bin, name), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "fakeedit --wait")

	for named, want := range map[string][]string{
		"":         {"fakeedit", "--wait"},
		"fakeedit": {"fakeedit"},
	} {
		if got, err := editorCommand(named); err != nil || !slices.Equal(got, want) {
			t.Errorf("editorCommand(%q) = %q, %v, want %q", named, got, err, want)
		}
	}
	if _, err := editorCommand("nosuchedit"); err == nil || !strings.Contains(err.Error(), "can't find the editor") {
		t.Errorf("missing editor: %v", err)
	}
}

func TestGitNano(t *testing.T) {
	root := t.TempDir()
	nano := filepath.Join(root, "Git", "usr", "bin", "nano.exe")
	os.MkdirAll(filepath.Dir(nano), 0o755)
	os.WriteFile(nano, nil, 0o755)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("ProgramFiles", root)
	t.Setenv("ProgramW6432", "")
	t.Setenv("LocalAppData", "")
	if got := gitNano(); got != nano {
		t.Fatalf("gitNano() = %q, want %q", got, nano)
	}
	t.Setenv("ProgramFiles", t.TempDir())
	if got := gitNano(); got != "" {
		t.Fatalf("gitNano() = %q with no Git installed", got)
	}
}
