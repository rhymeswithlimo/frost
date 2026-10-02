//go:build !windows

package engine

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rhymeswithlimo/frost/internal/snapshot"
)

// An in-place restore follows links above what it writes when they're
// yours or root's, like macOS's /var, and refuses anyone else's.
func TestRestoreInPlaceThroughLinks(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	dir := t.TempDir() // under /var on macOS, itself a link
	real := filepath.Join(dir, "real")
	os.Mkdir(real, 0o700)
	abs := filepath.Join(dir, "abs")
	rel := filepath.Join(dir, "rel")
	if err := os.Symlink(real, abs); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	os.Symlink(filepath.Join("..", filepath.Base(dir), "real"), rel)

	restore := func(p string) error {
		s := snapshot.Snapshot{ID: snapshot.NewID()}
		tree := &snapshot.Tree{Files: []snapshot.File{{Path: filepath.ToSlash(p), Type: snapshot.TypeFile, Mode: 0o600}}}
		if _, err := e.eng.Repo.SaveSnapshot(ctx, s, tree, nil); err != nil {
			t.Fatal(err)
		}
		_, err := e.eng.Restore(ctx, s.ID, RestoreOptions{})
		return err
	}
	for _, p := range []string{
		filepath.Join(abs, "a"),
		filepath.Join(rel, "b"),
		filepath.Join(abs, "new", "c"), // a missing folder is made inside
	} {
		if err := CanOverwrite([]string{filepath.ToSlash(p)}); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if err := restore(p); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	for _, name := range []string{"a", "b", filepath.Join("new", "c")} {
		if _, err := os.Stat(filepath.Join(real, name)); err != nil {
			t.Fatalf("%s not restored through the link: %v", name, err)
		}
	}

	trustedLink = func(fs.FileInfo) bool { return false }
	t.Cleanup(func() { trustedLink = defaultTrustedLink })
	p := filepath.Join(abs, "d")
	if err := CanOverwrite([]string{filepath.ToSlash(p)}); err == nil || !strings.Contains(err.Error(), "another user") {
		t.Fatalf("someone else's link: %v", err)
	}
	if err := restore(p); err == nil {
		t.Fatal("restored through someone else's link")
	}
	if _, err := os.Lstat(filepath.Join(real, "d")); err == nil {
		t.Fatal("wrote through someone else's link")
	}
}

func TestCanOverwriteForeignPath(t *testing.T) {
	if err := CanOverwrite([]string{"C:/Users/me/a"}); err == nil {
		t.Fatal("a Windows path was accepted here")
	}
}
