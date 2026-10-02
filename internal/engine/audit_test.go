package engine

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/rhymeswithlimo/frost/internal/repo"
	"github.com/rhymeswithlimo/frost/internal/snapshot"
	"github.com/rhymeswithlimo/frost/internal/storage"
)

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

func TestBackupRepairsMissingCachedChunk(t *testing.T) {
	e := newEnv(t)
	e.write("file", []byte("save me"))
	first := e.backup(BackupOptions{})
	ctx := context.Background()
	ids, err := e.eng.Repo.ChunkIDs(ctx)
	if err != nil || len(ids) == 0 {
		t.Fatalf("chunks: %v", err)
	}
	if err := e.mem.Delete(ctx, repo.ChunkKey(ids[0])); err != nil {
		t.Fatal(err)
	}
	// The sampled check after a backup notices and asks for a sync.
	if v, err := e.eng.Verify(ctx, 100, false); err != nil || v.OK() {
		t.Fatalf("verify missed a deleted chunk: %+v, %v", v, err)
	}
	second := e.backup(BackupOptions{Paths: []string{e.src, e.src}})
	if second.Snapshot.Stats.Files != first.Snapshot.Stats.Files {
		t.Fatal("overlapping paths counted twice")
	}
	if _, err := e.eng.Repo.GetChunk(ctx, ids[0]); err != nil {
		t.Fatalf("missing chunk not repaired: %v", err)
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

type changingBackend struct {
	storage.Backend
	once   sync.Once
	change func()
}

func (b *changingBackend) Put(ctx context.Context, key string, data []byte) error {
	b.once.Do(b.change)
	return b.Backend.Put(ctx, key, data)
}

// A file that changes once while it's read is read again after the walk.
func TestBackupRetriesFileChangedDuringRead(t *testing.T) {
	e := newEnv(t)
	e.write("active", random(32<<20, 9))
	p := filepath.Join(e.src, "active")
	var changeErr error
	e.eng.Uploaders = 1
	e.eng.Repo.Backend = &changingBackend{Backend: e.mem, change: func() {
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			changeErr = err
			return
		}
		_, changeErr = f.Write([]byte("changed during backup"))
		f.Close()
	}}
	res := e.backup(BackupOptions{})
	if changeErr != nil {
		t.Fatal(changeErr)
	}
	if res.Snapshot.Stats.Skipped != 0 || res.Snapshot.Stats.Kept != 0 || res.Snapshot.Stats.Files != 1 {
		t.Fatalf("file that changed once wasn't read again: %+v", res.Snapshot)
	}
	e.eng.Repo.Backend = e.mem
	target := t.TempDir()
	if _, err := e.eng.Restore(context.Background(), res.Snapshot.ID, RestoreOptions{Target: target}); err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile(p)
	if got, _ := os.ReadFile(filepath.Join(e.restoredRoot(target), "active")); !bytes.Equal(got, want) {
		t.Fatal("snapshot doesn't hold the file as it ended up")
	}
}

func TestVerifyFreshCacheAndMissingTreeChunk(t *testing.T) {
	e := newEnv(t)
	e.write("file", []byte("saved"))
	e.backup(BackupOptions{})
	ids, _ := e.eng.Repo.ChunkIDs(context.Background())
	if err := e.eng.Manifest.ReplaceChunks(nil); err != nil {
		t.Fatal(err)
	}
	if err := e.eng.Manifest.SetSnapshots(nil); err != nil {
		t.Fatal(err)
	}
	v, err := e.eng.Verify(context.Background(), 20, true)
	if err != nil || !v.OK() || v.Total == 0 {
		t.Fatalf("fresh-cache verification: %+v, %v", v, err)
	}
	e.mem.Delete(context.Background(), repo.ChunkKey(ids[0]))
	if err := e.eng.Manifest.ReplaceChunks(nil); err != nil {
		t.Fatal(err)
	}
	v, err = e.eng.Verify(context.Background(), 20, true)
	if err != nil || v.OK() {
		t.Fatalf("missing tree chunk: %+v, %v", v, err)
	}
}
