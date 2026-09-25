package cli

import (
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/rhymeswithlimo/frost/internal/config"
	"github.com/rhymeswithlimo/frost/internal/crypto"
)

// run executes the CLI with args and stdin, returning combined output.
func run(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	root := NewRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func must(t *testing.T, stdin string, args ...string) string {
	t.Helper()
	out, err := run(t, stdin, args...)
	if err != nil {
		t.Fatalf("frost %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func lines(s ...string) string { return strings.Join(s, "\n") + "\n" }

type fixture struct {
	src    string
	s3URL  string
	key    *crypto.Key
	phrase []string
	sched  []config.Config
}

func setup(t *testing.T) *fixture {
	t.Helper()
	t.Setenv("FROST_CONFIG_DIR", t.TempDir())
	t.Setenv("FROST_CACHE_DIR", t.TempDir())

	f := &fixture{src: t.TempDir()}
	f.key, _ = crypto.NewKey()
	f.phrase = strings.Fields(f.key.Phrase())

	syncSchedule = func(c config.Config) error { f.sched = append(f.sched, c); return nil }
	newKey = func() (*crypto.Key, error) { return f.key, nil }
	pickWords = func() (int, int) { return 2, 17 }
	t.Cleanup(func() {
		syncSchedule = installSchedule
		newKey = crypto.NewKey
		pickWords = randomWords
	})

	mem := s3mem.New()
	mem.CreateBucket("backups")
	srv := httptest.NewServer(gofakes3.New(mem).Server())
	t.Cleanup(srv.Close)
	f.s3URL = srv.URL

	os.MkdirAll(filepath.Join(f.src, "notes"), 0o755)
	os.WriteFile(filepath.Join(f.src, "notes", "todo.txt"), []byte("buy milk"), 0o644)
	os.WriteFile(filepath.Join(f.src, "photo.jpg"), bytes.Repeat([]byte{1, 2, 3}, 100000), 0o644)
	os.WriteFile(filepath.Join(f.src, "junk.tmp"), []byte("skip me"), 0o644)
	return f
}

func (f *fixture) initAnswers(wordA, wordB string) string {
	return lines(
		f.src, "*.tmp", // directories, excludes
		"y", "6h", // schedule
		"1", f.s3URL, "us-east-1", "backups", "", "AKID", "SECRET", // storage
		"",           // written it down
		wordA, wordB, // word check
	)
}

func TestEndToEnd(t *testing.T) {
	f := setup(t)

	// A wrong word check repeats the phrase and asks again; EOF then ends it.
	if _, err := run(t, f.initAnswers("wrong", "words"), "init"); err == nil {
		t.Fatal("init accepted a wrong word check")
	}

	out := must(t, f.initAnswers(f.phrase[2], f.phrase[17]), "init")
	if !strings.Contains(out, "Correct.") || !strings.Contains(out, f.phrase[0]) {
		t.Fatalf("init output:\n%s", out)
	}
	if len(f.sched) != 1 || f.sched[0].Schedule.Every != "6h" {
		t.Fatalf("schedule not installed: %+v", f.sched)
	}
	if fi, _ := os.Stat(config.KeyPath()); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("key file mode %v", fi.Mode().Perm())
	}

	// Dry run shows files and uploads nothing.
	out = must(t, "", "backup", "--dry-run")
	if !strings.Contains(out, "photo.jpg") || strings.Contains(out, "junk.tmp") {
		t.Fatalf("dry run output:\n%s", out)
	}

	out = must(t, "", "backup")
	if !strings.Contains(out, "verified") || !strings.Contains(out, "ok") {
		t.Fatalf("backup output:\n%s", out)
	}
	out = must(t, "", "backup")
	if !strings.Contains(out, "none, everything was already backed up") {
		t.Fatalf("second backup output:\n%s", out)
	}

	out = must(t, "", "status")
	for _, want := range []string{"last backup", "health", "SNAPSHOT"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status missing %q:\n%s", want, out)
		}
	}

	target := t.TempDir()
	must(t, "", "restore", "latest", "--target", target)
	var restored []byte
	filepath.WalkDir(target, func(p string, d os.DirEntry, err error) error {
		if d != nil && d.Name() == "todo.txt" {
			restored, _ = os.ReadFile(p)
		}
		return nil
	})
	if string(restored) != "buy milk" {
		t.Fatalf("restored todo.txt = %q", restored)
	}

	// config get/set, and schedule changes resync the job.
	must(t, "", "config", "set", "schedule.every", "daily")
	if got := strings.TrimSpace(must(t, "", "config", "get", "schedule.every")); got != "daily" {
		t.Fatalf("config get = %q", got)
	}
	if len(f.sched) != 2 {
		t.Fatalf("schedule not resynced after config set")
	}
	if out := must(t, "", "config"); strings.Contains(out, "SECRET") {
		t.Fatal("config listing leaked the secret")
	}

	// key verify with the right and a wrong phrase.
	out = must(t, lines(f.key.Phrase()), "key", "verify")
	if !strings.Contains(out, "opens") {
		t.Fatalf("key verify:\n%s", out)
	}
	other, _ := crypto.NewKey()
	if _, err := run(t, lines(other.Phrase()), "key", "verify"); err == nil {
		t.Fatal("key verify accepted the wrong phrase")
	}

	// key show needs the confirmation word.
	if _, err := run(t, lines("no"), "key", "show"); err == nil {
		t.Fatal("key show didn't ask for confirmation")
	}
	if out := must(t, lines("show"), "key", "show"); !strings.Contains(out, f.phrase[23]) {
		t.Fatal("key show didn't print the phrase")
	}
}

func TestNewMachineImport(t *testing.T) {
	f := setup(t)
	must(t, f.initAnswers(f.phrase[2], f.phrase[17]), "init")
	must(t, "", "backup")

	// Simulate a new machine: fresh config and cache, same bucket.
	t.Setenv("FROST_CONFIG_DIR", t.TempDir())
	t.Setenv("FROST_CACHE_DIR", t.TempDir())
	newKey = func() (*crypto.Key, error) {
		t.Fatal("init generated a key for an existing repository")
		return nil, nil
	}

	answers := lines(
		f.src, "*.tmp", "n",
		"1", f.s3URL, "us-east-1", "backups", "", "AKID", "SECRET",
		f.key.Phrase(),
	)
	out := must(t, answers, "init")
	if !strings.Contains(out, "already has frost backups") {
		t.Fatalf("init on existing repo:\n%s", out)
	}
	out = must(t, "", "backup")
	if !strings.Contains(out, "none, everything was already backed up") {
		t.Fatalf("backup on new machine re-uploaded data:\n%s", out)
	}
}

func TestSevenCommands(t *testing.T) {
	var names []string
	for _, c := range NewRoot().Commands() {
		if !c.Hidden && c.Name() != "help" {
			names = append(names, c.Name())
		}
	}
	if len(names) != 7 {
		t.Fatalf("commands = %v, want exactly 7", names)
	}
}
