package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/rhymeswithlimo/frost/internal/config"
	"github.com/rhymeswithlimo/frost/internal/crypto"
	"github.com/rhymeswithlimo/frost/internal/desktop"
	"github.com/rhymeswithlimo/frost/internal/schedule"
	"github.com/rhymeswithlimo/frost/internal/snapshot"
	"github.com/rhymeswithlimo/frost/internal/storage/permafrost"
	"github.com/rhymeswithlimo/frost/internal/update"
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
	scheduleKind = func() string { return "test scheduler" }
	scheduleInstalled = func() bool { return true }
	newKey = func() (*crypto.Key, error) { return f.key, nil }
	pickWords = func() (int, int) { return 2, 17 }
	openBrowser = func(string) error { return errors.New("no browser in tests") }
	latestRelease = func(context.Context) (update.Release, error) {
		return update.Release{}, errors.New("no network in tests")
	}
	installRelease = func(context.Context, update.Release, string) error { return errors.New("no updates in tests") }
	t.Cleanup(func() {
		latestRelease = update.Latest
		installRelease = update.Install
		syncSchedule = installSchedule
		scheduleKind = schedule.Kind
		scheduleInstalled = schedule.Installed
		newKey = crypto.NewKey
		pickWords = randomWords
		openBrowser = desktop.Open
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
		"2", f.s3URL, "us-east-1", "backups", "", "AKID", "SECRET", // storage
		f.src, "*.tmp", // directories, excludes
		"y", "6h", // schedule
		"",           // written it down
		wordA, wordB, // word check
	)
}

func TestEndToEnd(t *testing.T) {
	f := setup(t)

	// A wrong word check offers the phrase again and asks again; EOF then ends it.
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
	// Nothing changed, so nothing is uploaded or saved, and status says so.
	out = must(t, "", "backup")
	if !strings.Contains(out, "Already backed up") || strings.Contains(out, "uploaded after compression") {
		t.Fatalf("second backup output:\n%s", out)
	}

	out = must(t, "", "status")
	for _, want := range []string{"last backup", "nothing new since", "health", "snapshots"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status missing %q:\n%s", want, out)
		}
	}

	// Restores need exactly one destination.
	for want, args := range map[string][]string{
		"choose where":         {"restore", "latest"},
		"only one":             {"restore", "latest", "--beside", "--overwrite"},
		"no folder":            {"restore", "latest", "--to", filepath.Join(t.TempDir(), "missing")},
		"beside the originals": {"restore", "latest", "/", "--beside"}, // nothing to put a folder beside
	} {
		if _, err := run(t, "", args...); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%q: %v, want %q", args, err, want)
		}
	}
	restored := func(p string) {
		t.Helper()
		if data, err := os.ReadFile(p); err != nil || string(data) != "buy milk" {
			t.Fatalf("restored %s = %q, %v", p, data, err)
		}
	}
	find := func(dir string) string {
		t.Helper()
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "frost-restore-") {
				return filepath.Join(dir, e.Name())
			}
		}
		t.Fatalf("no restore folder in %s", dir)
		return ""
	}

	// --to keeps the backed-up folder's name inside the new folder.
	target := t.TempDir()
	must(t, "", "restore", "latest", "--to", target)
	restored(filepath.Join(find(target), filepath.Base(f.src), "notes", "todo.txt"))

	// --beside puts the new folder next to what was picked.
	must(t, "", "restore", "latest", filepath.Join(f.src, "notes"), "--beside")
	restored(filepath.Join(find(f.src), "notes", "todo.txt"))

	// --overwrite asks first, and puts the backed-up version back. On macOS
	// the temp dir is under /var, a link owned by root, which it follows.
	dir := t.TempDir()
	todo := filepath.Join(dir, "todo.txt")
	os.WriteFile(todo, []byte("buy milk"), 0o644)
	must(t, "", "backup", "--path", dir)
	os.WriteFile(todo, []byte("buy oat milk"), 0o644)
	if _, err := run(t, "n\n", "restore", "latest", todo, "--overwrite"); err == nil {
		t.Fatal("overwrite went ahead after no")
	}
	if data, _ := os.ReadFile(todo); string(data) != "buy oat milk" {
		t.Fatalf("overwrite after no changed the file: %q", data)
	}
	must(t, "", "restore", "latest", todo, "--overwrite", "-y")
	restored(todo)

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

func TestShortSnapshotIDs(t *testing.T) {
	f := setup(t)
	must(t, f.initAnswers(f.phrase[2], f.phrase[17]), "init")
	backup := must(t, "", "backup")

	a, err := openApp(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var id string
	for k := range a.engine.Manifest.Snapshots() {
		id = k
	}
	a.Close()
	short := snapshot.Short(id)
	if short == id {
		t.Fatalf("ID %q has no short form", id)
	}

	// The shown ID is enough to restore by, and names the new folder.
	target := t.TempDir()
	restore := must(t, "", "restore", short, "--to", target)
	if _, err := os.Stat(filepath.Join(target, "frost-restore-"+short)); err != nil {
		t.Errorf("restore folder: %v", err)
	}
	status := must(t, "", "status")
	for name, out := range map[string]string{"backup": backup, "restore": restore, "status": status} {
		if out = strings.ToLower(out); !strings.Contains(out, short) || strings.Contains(out, id) {
			t.Errorf("%s should show %s, not %s:\n%s", name, short, id, out)
		}
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
		"2", f.s3URL, "us-east-1", "backups", "", "AKID", "SECRET",
		f.src, "*.tmp", "n",
		f.key.Phrase(),
	)
	out := must(t, answers, "init")
	if !strings.Contains(out, "already has frost backups") {
		t.Fatalf("init on existing repo:\n%s", out)
	}
	// A new machine has nothing to compare with, so it saves a snapshot,
	// but the data is already stored.
	out = must(t, "", "backup")
	if !strings.Contains(out, "Saved snapshot") || strings.Contains(out, "uploaded after compression") {
		t.Fatalf("backup on new machine re-uploaded data:\n%s", out)
	}
}

// Cobra's built-in help and completion commands stay off: -h covers help,
// and completion scripts aren't something frost ships.
func TestNoBuiltinCommands(t *testing.T) {
	// Cobra only adds them while executing.
	root := NewRoot()
	root.SetArgs([]string{"--help"})
	root.SetOut(io.Discard)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, c := range root.Commands() {
		if !c.Hidden && (c.Name() == "help" || c.Name() == "completion") {
			t.Fatalf("%s command is visible", c.Name())
		}
	}
}

func TestInitRetriesFailedConnect(t *testing.T) {
	f := setup(t)
	answers := lines(
		"2", f.s3URL, "us-east-1", "no-such-bucket", "", "AKID", "SECRET",
		// Second go: everything but the bucket keeps its answer.
		"", "", "", "backups", "", "", "",
		f.src, "*.tmp", "n",
		"", f.phrase[2], f.phrase[17],
	)
	out := must(t, answers, "init")
	if !strings.Contains(out, "no bucket with that name") {
		t.Fatalf("no plain explanation of the failed connect:\n%s", out)
	}
	if strings.Contains(out, "SECRET") {
		t.Fatal("init printed the secret")
	}
	must(t, "", "backup")
}

// permafrostServer is just enough of docs/PERMAFROST.md for init and a
// backup to work. The full reference server lives in the permafrost package.
func permafrostServer(t *testing.T, token string) string {
	var mu sync.Mutex
	objs := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":{"code":"unauthorized","message":"bad token"}}`))
			return
		}
		if r.URL.Path == "/v1/objects" {
			var keys []string
			for k := range objs {
				if strings.HasPrefix(k, r.URL.Query().Get("prefix")) {
					keys = append(keys, k)
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"keys": keys, "next_cursor": ""})
			return
		}
		key := strings.TrimPrefix(r.URL.Path, "/v1/objects/")
		switch r.Method {
		case http.MethodPut:
			if _, exists := objs[key]; exists && r.Header.Get("If-None-Match") == "*" {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			objs[key], _ = io.ReadAll(r.Body)
			w.WriteHeader(204)
		case http.MethodGet:
			b, ok := objs[key]
			if !ok {
				w.WriteHeader(404)
				w.Write([]byte(`{"error":{"code":"not_found","message":"not found"}}`))
				return
			}
			w.Write(b)
		case http.MethodDelete:
			delete(objs, key)
			w.WriteHeader(204)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestInitPermafrostNeedsOnlyTheKey(t *testing.T) {
	f := setup(t)
	cfg := config.Default()
	cfg.Storage.Backend = "permafrost"
	cfg.Storage.Permafrost.URL = permafrostServer(t, "right-key")
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}

	answers := lines(
		"", "", "wrong-key", // enter keeps Permafrost, then "I have a key"
		"1", "right-key",
		f.src, "*.tmp", "n",
		"", f.phrase[2], f.phrase[17],
	)
	out := must(t, answers, "init")
	if !strings.Contains(out, "didn't accept that access key") {
		t.Fatalf("no plain explanation of the bad key:\n%s", out)
	}
	if strings.Contains(out, "right-key") || strings.Contains(out, "wrong-key") {
		t.Fatal("init printed an access key")
	}
	must(t, "", "backup")
}

func TestInitGetsPermafrostKey(t *testing.T) {
	f := setup(t)
	cfg := config.Default()
	cfg.Storage.Permafrost.URL = permafrostServer(t, "new-key")
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	// The browser goes through checkout and comes back with a key.
	var opened string
	openBrowser = func(u string) error {
		p, err := url.Parse(u)
		if err != nil {
			return err
		}
		q := p.Query()
		opened = p.Path
		back := q.Get("redirect_uri") + "?" + url.Values{"state": {q.Get("state")}, "token": {"new-key"}}.Encode()
		go http.Get(back)
		return nil
	}
	answers := lines(
		"1", "2", // Permafrost, no key yet
		f.src, "*.tmp", "n",
		"", f.phrase[2], f.phrase[17],
	)
	out := must(t, answers, "init")
	if opened != "/checkout" {
		t.Errorf("opened %q, want the custom server's /checkout", opened)
	}
	if strings.Contains(out, "new-key") {
		t.Fatal("init printed the access key")
	}
	saved, err := config.LoadFile()
	if err != nil || saved.Storage.Backend != "permafrost" || saved.Storage.Permafrost.Token != "new-key" {
		t.Fatalf("saved storage %+v, %v", saved.Storage, err)
	}
	must(t, "", "backup")

	// Later the key stops working: say so plainly, and don't retry.
	saved.Storage.Permafrost.Token = "expired"
	config.Save(saved)
	_, err = run(t, "", "backup")
	if !errors.Is(err, permafrost.ErrUnauthorized) || !strings.Contains(plainError(err).Error(), "frost init") {
		t.Fatalf("backup with a rejected key: %v", err)
	}
}

func TestSaveTokenKeepsBackend(t *testing.T) {
	setup(t)
	cfg := config.Default()
	cfg.Storage.Backend, cfg.Storage.S3.Bucket = "s3", "backups"
	config.Save(cfg)
	if err := saveToken("new-key"); err != nil {
		t.Fatal(err)
	}
	got, _ := config.LoadFile()
	if got.Storage.Backend != "s3" || got.Storage.Permafrost.Token != "new-key" {
		t.Errorf("storage after saving a key: %+v", got.Storage)
	}

	// On a fresh machine the key's file isn't a finished setup.
	os.Remove(config.Path())
	if err := saveToken("new-key"); err != nil {
		t.Fatal(err)
	}
	got, _ = config.LoadFile()
	if got.Storage.Backend != "permafrost" || len(got.Paths) != 0 {
		t.Errorf("fresh config after saving a key: %+v", got)
	}
}

func TestKeyVerifyMismatchWithoutConfig(t *testing.T) {
	f := setup(t)
	if err := saveKey(f.key); err != nil {
		t.Fatal(err)
	}
	other, _ := crypto.NewKey()
	if _, err := run(t, other.Phrase()+"\n", "key", "verify"); err == nil {
		t.Fatal("wrong key reported success")
	}
}

func TestRerunCommandNamesTheSnapshot(t *testing.T) {
	got := rerunCommand("maple-otter-3f1c", []string{"/home/me/My Documents"}, true, "", false)
	want := "frost restore maple-otter-3f1c '" + filepath.FromSlash("/home/me/My Documents") + "' --beside"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if got := rerunCommand("x", nil, false, "/tmp/out", false); got != "frost restore x --to /tmp/out" {
		t.Fatalf("--to: %s", got)
	}
	if got := rerunCommand("x", nil, false, "", true); got != "frost restore x --overwrite" {
		t.Fatalf("--overwrite: %s", got)
	}
}
