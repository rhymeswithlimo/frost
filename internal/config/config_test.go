package config

import (
	"os"
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
