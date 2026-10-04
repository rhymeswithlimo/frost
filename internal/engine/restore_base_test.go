package engine

import (
	"context"
	"encoding/json"
	"errors"
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

func TestRestorePreservesDestinationOnInvalidSize(t *testing.T) {
	for _, size := range []int64{2, 20} {
		t.Run(string(rune('a'+size)), func(t *testing.T) {
			e := newEnv(t)
			ctx := context.Background()
			id := e.key.ChunkID([]byte("new data"))
			if _, err := e.eng.Repo.PutChunk(ctx, id, []byte("new data")); err != nil {
				t.Fatal(err)
			}
			s := snapshot.Snapshot{ID: snapshot.NewID()}
			tree := &snapshot.Tree{Files: []snapshot.File{{Path: "/file", Type: snapshot.TypeFile, Size: size, Chunks: []string{id.String()}, Mode: 0o600}}}
			if _, err := e.eng.Repo.SaveSnapshot(ctx, s, tree, nil); err != nil {
				t.Fatal(err)
			}
			target := t.TempDir()
			file := filepath.Join(target, "file")
			if err := os.WriteFile(file, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := e.eng.Restore(ctx, s.ID, RestoreOptions{Target: target}); err == nil {
				t.Fatal("invalid size accepted")
			}
			got, err := os.ReadFile(file)
			if err != nil || string(got) != "original" {
				t.Fatalf("original lost: %q, %v", got, err)
			}
			entries, _ := os.ReadDir(target)
			if len(entries) != 1 {
				t.Fatal("temporary file leaked")
			}
		})
	}
}

func TestRestoreRefusesSymlinkParent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s := snapshot.Snapshot{ID: snapshot.NewID()}
	tree := &snapshot.Tree{Files: []snapshot.File{{Path: "/link/file", Type: snapshot.TypeFile, Mode: 0o600}}}
	if _, err := e.eng.Repo.SaveSnapshot(ctx, s, tree, nil); err != nil {
		t.Fatal(err)
	}
	target, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(target, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := e.eng.Restore(ctx, s.ID, RestoreOptions{Target: target}); err == nil {
		t.Fatal("symlink parent accepted")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatal("wrote outside target")
	}
}

func TestRestoreReplacementAndNewTarget(t *testing.T) {
	e := newEnv(t)
	if resolved, err := filepath.EvalSymlinks(e.src); err == nil {
		e.src = resolved
	}
	e.write("file", []byte("saved"))
	res := e.backup(BackupOptions{})
	target := t.TempDir()
	ctx := context.Background()
	if _, err := e.eng.Restore(ctx, res.Snapshot.ID, RestoreOptions{Target: target, NewTarget: true}); err == nil {
		t.Fatal("existing target accepted")
	}
	for range 2 {
		if _, err := e.eng.Restore(ctx, res.Snapshot.ID, RestoreOptions{Target: target}); err != nil {
			t.Fatal(err)
		}
	}
	e.write("file", []byte("changed"))
	if _, err := e.eng.Restore(ctx, res.Snapshot.ID, RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(e.src, "file")); string(got) != "saved" {
		t.Fatalf("in-place restore: %q", got)
	}
}

func TestRestorePreflightsWholeSelection(t *testing.T) {
	e := newEnv(t)
	s := snapshot.Snapshot{ID: snapshot.NewID()}
	tree := &snapshot.Tree{Files: []snapshot.File{{Path: "/good", Type: snapshot.TypeFile}, {Path: "/../bad", Type: snapshot.TypeFile}}}
	ctx := context.Background()
	if _, err := e.eng.Repo.SaveSnapshot(ctx, s, tree, nil); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if _, err := e.eng.Restore(ctx, s.ID, RestoreOptions{Target: target}); err == nil {
		t.Fatal("unsafe tree accepted")
	}
	if entries, _ := os.ReadDir(target); len(entries) != 0 {
		t.Fatal("wrote before validating entire selection")
	}
}

func TestRestoreDoesNotDeleteDirectoryDestination(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s := snapshot.Snapshot{ID: snapshot.NewID()}
	if _, err := e.eng.Repo.SaveSnapshot(ctx, s, &snapshot.Tree{Files: []snapshot.File{{Path: "/file", Type: snapshot.TypeFile, Mode: 0o600}}}, nil); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	out := filepath.Join(target, "file")
	if err := os.Mkdir(out, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := e.eng.Restore(ctx, s.ID, RestoreOptions{Target: target}); err == nil {
		t.Fatal("file replaced directory")
	}
	if info, err := os.Stat(out); err != nil || !info.IsDir() {
		t.Fatalf("directory lost: %v", err)
	}
}

func TestRestoreCancellationStopsEmptyFiles(t *testing.T) {
	e := newEnv(t)
	s := snapshot.Snapshot{ID: snapshot.NewID()}
	tree := &snapshot.Tree{Files: []snapshot.File{
		{Path: "/first", Type: snapshot.TypeFile, Mode: 0o600},
		{Path: "/second", Type: snapshot.TypeFile, Mode: 0o600},
	}}
	if _, err := e.eng.Repo.SaveSnapshot(context.Background(), s, tree, nil); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	second := filepath.Join(target, "second")
	if err := os.WriteFile(second, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res, err := e.eng.Restore(ctx, s.ID, RestoreOptions{Target: target, Progress: func(p RestoreProgress) {
		if !p.Checking && p.Files == 1 {
			cancel()
		}
	}})
	if !errors.Is(err, context.Canceled) || res.Files != 1 {
		t.Fatalf("cancelled restore = %+v, %v", res, err)
	}
	if got, err := os.ReadFile(second); err != nil || string(got) != "original" {
		t.Fatalf("cancelled restore replaced an unstarted file: %q, %v", got, err)
	}
}

func TestRestoreCancellationAfterFinalFile(t *testing.T) {
	for _, name := range []string{"empty", "empty_completed", "completed"} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			s := snapshot.Snapshot{ID: snapshot.NewID()}
			file := snapshot.File{Path: "/only", Type: snapshot.TypeFile, Mode: 0o600}
			var data []byte
			if name == "completed" {
				data = []byte("already restored")
				id := e.key.ChunkID(data)
				if _, err := e.eng.Repo.PutChunk(context.Background(), id, data); err != nil {
					t.Fatal(err)
				}
				file.Size, file.Chunks = int64(len(data)), []string{id.String()}
			}
			if _, err := e.eng.Repo.SaveSnapshot(context.Background(), s, &snapshot.Tree{Files: []snapshot.File{file}}, nil); err != nil {
				t.Fatal(err)
			}
			target := t.TempDir()
			if name != "empty" {
				if err := os.WriteFile(filepath.Join(target, "only"), data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			eng := &Engine{Repo: e.eng.Repo}
			res, err := eng.Restore(ctx, s.ID, RestoreOptions{Target: target, Progress: func(p RestoreProgress) {
				if !p.Checking && p.Files == 1 {
					cancel()
				}
			}})
			if !errors.Is(err, context.Canceled) || res.Files != 1 {
				t.Fatalf("final-file cancellation returned %+v, %v", res, err)
			}
			if got, err := os.ReadFile(filepath.Join(target, "only")); err != nil || string(got) != string(data) {
				t.Fatalf("completed file changed after cancellation: %q, %v", got, err)
			}
		})
	}
}
