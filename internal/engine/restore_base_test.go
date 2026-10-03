package engine

import (
	"context"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/rhymeswithlimo/frost/internal/snapshot"
)

func TestRestoreBase(t *testing.T) {
	e := newEnv(t)
	e.write("want/a", []byte("a"))
	e.write("want/deep/b", []byte("b"))
	e.write("skip/c", []byte("c"))
	res := e.backup(BackupOptions{})
	src := filepath.ToSlash(e.src)
	restore := func(base string, include ...string) (string, error) {
		target := filepath.Join(t.TempDir(), "out")
		_, err := e.eng.Restore(context.Background(), res.Snapshot.ID, RestoreOptions{
			Target: target, NewTarget: true, Base: base, Include: include,
		})
		return target, err
	}
	has := func(target string, want ...string) {
		t.Helper()
		var got []string
		filepath.WalkDir(target, func(p string, d os.DirEntry, _ error) error {
			if !d.IsDir() {
				rel, _ := filepath.Rel(target, p)
				got = append(got, filepath.ToSlash(rel))
			}
			return nil
		})
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Fatalf("restored %q, want %q", got, want)
		}
	}

	// A selected folder keeps its name.
	include := []string{src + "/want"}
	target, err := restore(snapshot.RestoreBase(include), include...)
	if err != nil {
		t.Fatal(err)
	}
	has(target, "want/a", "want/deep/b")

	// A single file lands at the top.
	include = []string{src + "/want/a"}
	target, err = restore(snapshot.RestoreBase(include), include...)
	if err != nil {
		t.Fatal(err)
	}
	has(target, "a")

	// The whole snapshot, by its backed-up folders.
	target, err = restore(snapshot.RestoreBase(res.Snapshot.Paths), res.Snapshot.Paths...)
	if err != nil {
		t.Fatal(err)
	}
	name := path.Base(src)
	has(target, name+"/skip/c", name+"/want/a", name+"/want/deep/b")

	// Anything outside the base is refused before a thing is written.
	target, err = restore(src+"/want/deep", src+"/want")
	if err == nil {
		t.Fatal("restored a path outside the base")
	}
	if _, statErr := os.Lstat(target); !os.IsNotExist(statErr) {
		t.Fatal("created the target for a refused restore")
	}

	if _, err := e.eng.Restore(context.Background(), res.Snapshot.ID, RestoreOptions{Base: src}); err == nil {
		t.Fatal("a base without a target was accepted")
	}
}

func TestNewRestoreFolder(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "frost-restore-x")
	if err := os.Mkdir(base, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base+"-1", []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, resume, err := NewRestoreFolder(dir, "x", nil)
	if err != nil || got != base+"-2" || resume {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestNewRestoreFolderSharedShortID(t *testing.T) {
	dir := t.TempDir()
	a, b := "maple-absurd-3f1c9a0b2e7", "maple-absurd-3f1c0000000"
	got, _, err := NewRestoreFolder(dir, a, nil)
	if err != nil || got != filepath.Join(dir, "frost-restore-maple-absurd-3f1c") {
		t.Fatalf("got %q, %v", got, err)
	}
	// An unfinished restore of a, in that folder.
	if err := os.Mkdir(got, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(newMarker(a, nil))
	if err := os.WriteFile(filepath.Join(got, restoreMarker), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	// b has the same short ID, so it gets the next name, not a's restore.
	if other, resume, err := NewRestoreFolder(dir, b, nil); err != nil || other != got+"-1" || resume {
		t.Fatalf("b got %q, resume %v, %v", other, resume, err)
	}
	if again, resume, err := NewRestoreFolder(dir, a, nil); err != nil || again != got || !resume {
		t.Fatalf("a got %q, resume %v, %v", again, resume, err)
	}
}

func TestBesideFolder(t *testing.T) {
	dir := t.TempDir()
	got, _, err := BesideFolder(filepath.ToSlash(dir), "x", nil)
	if err != nil || got != filepath.Join(dir, "frost-restore-x") {
		t.Fatalf("got %q, %v", got, err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("left %d entries behind", len(left))
	}
	for _, base := range []string{"", "/", "C:/", filepath.ToSlash(filepath.Join(dir, "missing"))} {
		if _, _, err := BesideFolder(base, "x", nil); err == nil {
			t.Errorf("%q: accepted", base)
		}
	}
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		return // no read-only folders to test with
	}
	ro := filepath.Join(dir, "ro")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(ro, 0o700) })
	if _, _, err := BesideFolder(filepath.ToSlash(ro), "x", nil); err == nil || !strings.Contains(err.Error(), "can't write") {
		t.Fatalf("read-only folder: %v", err)
	}
}
