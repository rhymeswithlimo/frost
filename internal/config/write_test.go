package config

import (
	"os"
	"path/filepath"
	"testing"
)

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
