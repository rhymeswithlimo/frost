package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Setenv("FROST_CONFIG_DIR", t.TempDir())
	c := Default()
	c.Paths = []string{"~/Documents", `C:\Users\me "quoted"`}
	c.Storage.Backend = "s3"
	c.Storage.S3 = S3{Endpoint: "s3.example.com", Bucket: "b", SecretAccessKey: "s3cr3t"}
	if err := Save(c); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(Path()); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("config mode = %v, want 0600", fi.Mode().Perm())
	}
	got, err := LoadFile()
	if err != nil {
		t.Fatal(err)
	}
	if got.Paths[1] != c.Paths[1] || got.Storage.S3.SecretAccessKey != "s3cr3t" || got.Schedule.Every != "daily" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	if err := got.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestEmptyListsRender(t *testing.T) {
	t.Setenv("FROST_CONFIG_DIR", t.TempDir())
	c := Config{}
	if err := Save(c); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(); err != nil {
		t.Fatal(err)
	}
}

func TestUnknownKeyRejected(t *testing.T) {
	var c Config
	if err := Parse([]byte("pathz = []"), &c); err == nil {
		t.Fatal("typo in config accepted")
	}
}

func TestEnvNotSaved(t *testing.T) {
	t.Setenv("FROST_CONFIG_DIR", t.TempDir())
	t.Setenv("FROST_S3_SECRET_ACCESS_KEY", "from-env")
	c := Default()
	Save(c)
	loaded, _ := Load()
	if loaded.Storage.S3.SecretAccessKey != "from-env" {
		t.Fatal("env override not applied")
	}
	file, _ := LoadFile()
	if file.Storage.S3.SecretAccessKey != "" {
		t.Fatal("LoadFile applied env")
	}
}

func TestSetGet(t *testing.T) {
	c := Default()
	if err := c.Set("schedule.every", []string{"6h"}); err != nil {
		t.Fatal(err)
	}
	if v, _ := c.Get("schedule.every"); v != "6h" {
		t.Fatalf("got %q", v)
	}
	if err := c.Set("schedule.enabled", []string{"maybe"}); err == nil {
		t.Fatal("bad bool accepted")
	}
	if err := c.Set("nope", []string{"x"}); err == nil {
		t.Fatal("unknown key accepted")
	}
	if _, err := Interval("5h"); err == nil {
		t.Fatal("5h accepted")
	}
}

func TestS3FolderDefault(t *testing.T) {
	t.Setenv("FROST_CONFIG_DIR", t.TempDir())
	if p := Default().Storage.S3.Prefix; p != "frost" {
		t.Fatalf("default folder = %q, want frost", p)
	}
	// Saved as the bucket's top level, it stays there.
	c := Default()
	c.Storage.S3.Prefix = ""
	if err := Save(c); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadFile(); err != nil || got.Storage.S3.Prefix != "" {
		t.Fatalf("top-level folder came back as %q, %v", got.Storage.S3.Prefix, err)
	}
	// A file that doesn't mention it gets the default.
	if err := os.WriteFile(Path(), []byte("[storage]\nbackend = \"s3\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadFile(); err != nil || got.Storage.S3.Prefix != "frost" {
		t.Fatalf("missing folder came back as %q, %v", got.Storage.S3.Prefix, err)
	}
}

func TestWritePrivateReplacesWithoutFollowingDestination(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config")
	if err := WritePrivate(p, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivate(p, []byte("second")); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(p); string(got) != "second" {
		t.Fatal("replacement failed")
	}
	oldTmp := p + ".tmp"
	if err := os.WriteFile(oldTmp, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivate(p, []byte("third")); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(oldTmp); string(got) != "untouched" {
		t.Fatal("reused predictable temporary file")
	}
}

func TestIntervalNormalizesDuration(t *testing.T) {
	if _, err := Interval(" 2H "); err != nil {
		t.Fatal(err)
	}
}

func TestWritePrivateReplacesSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "config")
	if err := os.Symlink(outside, dst); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := WritePrivate(dst, []byte("replacement")); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "untouched" {
		t.Fatalf("symlink target changed: %q, %v", got, err)
	}
	if got, err := os.ReadFile(dst); err != nil || string(got) != "replacement" {
		t.Fatalf("replacement = %q, %v", got, err)
	}
}
