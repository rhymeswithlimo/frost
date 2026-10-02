package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/rhymeswithlimo/frost/internal/config"
	"github.com/rhymeswithlimo/frost/internal/storage"
)

// folder is the fixture's bucket seen through prefix p.
func (f *fixture) folder(t *testing.T, p string) storage.Backend {
	t.Helper()
	b, err := newBackend(config.Storage{Backend: "s3", S3: config.S3{Endpoint: f.s3URL, Region: "us-east-1", Bucket: "backups", Prefix: p, AccessKeyID: "AKID", SecretAccessKey: "SECRET"}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// copyFolder copies every object under one prefix to another, or only
// frost.repo with repoOnly.
func copyFolder(t *testing.T, from, to storage.Backend, repoOnly bool) {
	t.Helper()
	ctx := context.Background()
	keys, err := from.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if repoOnly && k != "frost.repo" {
			continue
		}
		data, err := from.Get(ctx, k)
		if err != nil {
			t.Fatal(err)
		}
		if err := to.Put(ctx, k, data); err != nil {
			t.Fatal(err)
		}
	}
}

// setPrefix changes the folder in config.toml directly, as an editor would.
func setPrefix(t *testing.T, p string) {
	t.Helper()
	cfg, err := config.LoadFile()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Storage.S3.Prefix = p
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestStorageFolderChanges(t *testing.T) {
	f := setup(t)
	must(t, f.initAnswers(f.phrase[2], f.phrase[17]), "init")
	must(t, "", "backup")
	if p := strings.TrimSpace(must(t, "", "config", "get", "storage.s3.prefix")); p != "frost" {
		t.Fatalf("init put backups in %q, want frost", p)
	}

	// An empty folder is refused, and says where the backups are.
	out, err := run(t, "", "config", "set", "storage.s3.prefix", "elsewhere")
	if err == nil || !strings.Contains(err.Error(), "no backups in s3://backups/elsewhere/") || !strings.Contains(err.Error(), "Yours are in s3://backups/frost/") || !strings.Contains(err.Error(), "Nothing was saved") {
		t.Fatalf("empty folder: %v\n%s", err, out)
	}
	if p := strings.TrimSpace(must(t, "", "config", "get", "storage.s3.prefix")); p != "frost" {
		t.Fatalf("refused change was saved: %q", p)
	}

	// Changed behind frost's back: status explains, and a failed backup shows.
	setPrefix(t, "elsewhere")
	if _, err := run(t, "", "backup"); err == nil {
		t.Fatal("backup to an empty folder worked")
	}
	out, err = run(t, "", "status")
	for _, want := range []string{"PROBLEM", "Your backups were last opened in s3://backups/frost/", "storage.s3.prefix changed from frost to elsewhere", "frost config set storage.s3.prefix frost", "FAILED"} {
		if err == nil || !strings.Contains(out, want) {
			t.Fatalf("status after a folder change is missing %q (%v):\n%s", want, err, out)
		}
	}

	// Only frost.repo moved: refused, and named.
	setPrefix(t, "frost")
	copyFolder(t, f.folder(t, "frost"), f.folder(t, "half"), true)
	if _, err := run(t, "", "config", "set", "storage.s3.prefix", "half"); err == nil || !strings.Contains(err.Error(), "none of your snapshots") {
		t.Fatalf("frost.repo moved alone: %v", err)
	}

	// The whole folder moved: accepted, and backups carry on there.
	copyFolder(t, f.folder(t, "frost"), f.folder(t, "moved"), false)
	must(t, "", "config", "set", "storage.s3.prefix", "moved")
	out = must(t, "", "backup")
	if !strings.Contains(out, "none, everything was already backed up") {
		t.Fatalf("backup after a full move re-uploaded:\n%s", out)
	}
	if out := must(t, "", "status"); strings.Contains(out, "FAILED") || strings.Contains(out, "missing") {
		t.Fatalf("status after the move still complains:\n%s", out)
	}

	// Forced onto the half folder, the lost snapshots are reported.
	setPrefix(t, "half")
	out, _ = run(t, "", "status")
	if !strings.Contains(out, "aren't in storage any more") {
		t.Fatalf("status didn't notice the snapshots are gone:\n%s", out)
	}
}

// Running init on an empty folder when this machine's backups are elsewhere
// asks first.
func TestInitAsksBeforeStartingOver(t *testing.T) {
	f := setup(t)
	must(t, f.initAnswers(f.phrase[2], f.phrase[17]), "init")
	must(t, "", "backup")
	again := func(answer string) (string, error) {
		return run(t, lines("", "", "", "", "other", "", "", "", "", "", "", answer), "init")
	}
	out, err := again("n")
	if err == nil || !strings.Contains(out, "This machine's backups are in s3://backups/frost/") || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("init didn't ask, or went ahead anyway (%v):\n%s", err, out)
	}
	if p := strings.TrimSpace(must(t, "", "config", "get", "storage.s3.prefix")); p != "frost" {
		t.Fatalf("declined init still saved %q", p)
	}
	if _, err := again("y"); err != nil {
		t.Fatal(err)
	}
	if p := strings.TrimSpace(must(t, "", "config", "get", "storage.s3.prefix")); p != "other" {
		t.Fatalf("accepted init saved %q", p)
	}
}

// With two settings changed, setting them back one at a time would be
// refused halfway, so the advice is to edit the file.
func TestHintForSeveralChanges(t *testing.T) {
	was := config.Storage{Backend: "s3", S3: config.S3{Endpoint: "e", Bucket: "a", Prefix: "frost"}}
	now := config.Storage{Backend: "s3", S3: config.S3{Endpoint: "e", Bucket: "b", Prefix: "new"}}
	k := known{Storage: was, Shown: "s3://a/frost/", Where: "old place"}
	b, _ := newBackend(now)
	hint := storageHint(k, now, b, true)
	if strings.Contains(hint, "frost config set") || !strings.Contains(hint, "frost config edit, and set storage.s3.bucket to a and storage.s3.prefix to frost") {
		t.Fatalf("hint:\n%s", hint)
	}
}

// --config-dir gives each config its own record of where its backups are.
func TestKnownIsPerConfig(t *testing.T) {
	t.Setenv("FROST_CACHE_DIR", t.TempDir())
	t.Setenv("FROST_CONFIG_DIR", t.TempDir())
	first := knownPath()
	t.Setenv("FROST_CONFIG_DIR", t.TempDir())
	if knownPath() == first {
		t.Fatal("two config folders share one record")
	}
}

func TestErrorTextKeepsLineBreaks(t *testing.T) {
	got := errorText(&storageError{"first\n\nsecond \x1b[31mred"})
	if got != "first\n\nsecond ?[31mred" {
		t.Fatalf("%q", got)
	}
}
