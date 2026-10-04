package engine

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rhymeswithlimo/frost/internal/crypto"
	"github.com/rhymeswithlimo/frost/internal/repo"
	"github.com/rhymeswithlimo/frost/internal/snapshot"
	"github.com/rhymeswithlimo/frost/internal/storage"
	"github.com/rhymeswithlimo/frost/internal/storage/storagetest"
)

type stepContext struct {
	context.Context
	cancel context.CancelFunc
	at     int32
	checks atomic.Int32
	ready  *atomic.Bool
}

func (c *stepContext) Err() error {
	if (c.ready == nil || c.ready.Load()) && c.checks.Add(1) == c.at {
		c.cancel()
	}
	return c.Context.Err()
}

type referenceGate struct {
	storage.Backend
	ready *atomic.Bool
}

func (b referenceGate) Get(ctx context.Context, key string) ([]byte, error) {
	data, err := b.Backend.Get(ctx, key)
	if strings.HasPrefix(key, "chunks/") {
		b.ready.Store(true)
	}
	return data, err
}

func TestVerifyCancelsBeforeRequestingSync(t *testing.T) {
	e := newEnv(t)
	s := snapshot.Snapshot{ID: snapshot.NewID(), Time: time.Now().UTC()}
	tree := &snapshot.Tree{Files: []snapshot.File{{Path: "/missing", Type: snapshot.TypeFile, Size: 1, Chunks: []string{(crypto.ID{1}).String()}}}}
	listed, err := e.eng.Repo.SaveSnapshot(context.Background(), s, tree, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(e.eng.Manifest.AddChunks(listed), e.eng.Manifest.PutSnapshot(s)); err != nil {
		t.Fatal(err)
	}
	if err := e.eng.Manifest.PutMeta(metaChunkSync, chunkSync{Time: time.Now(), Where: storage.Location(e.mem)}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var ready atomic.Bool
	e.eng.Repo.Backend = referenceGate{Backend: e.mem, ready: &ready}
	stepped := &stepContext{Context: ctx, cancel: cancel, at: 3, ready: &ready}
	if _, err := e.eng.Verify(stepped, 0, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled verification returned %v", err)
	}
	var sync chunkSync
	if !e.eng.Manifest.GetMeta(metaChunkSync, &sync) || sync.Needed {
		t.Fatal("cancelled verification requested a chunk sync")
	}
	if _, ok := e.eng.LastVerify(); ok {
		t.Fatal("cancelled verification saved a completed result")
	}
}

func TestVerifyCancelsBeforeSavingResult(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stepped := &stepContext{Context: ctx, cancel: cancel, at: 2}
	if _, err := e.eng.Verify(stepped, 0, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled verification returned %v", err)
	}
	if _, ok := e.eng.LastVerify(); ok {
		t.Fatal("cancelled verification saved a completed result")
	}
}

func TestVerifyTreeChunkReferencesAndCancellation(t *testing.T) {
	e := newEnv(t)
	known, missing := crypto.ID{1}, crypto.ID{2}
	if err := e.eng.Manifest.AddChunks(map[crypto.ID]int{known: 1}); err != nil {
		t.Fatal(err)
	}
	tree := &snapshot.Tree{Files: []snapshot.File{
		{Chunks: []string{known.String(), known.String()}},
		{Chunks: []string{known.String(), missing.String(), "invalid"}},
		{Chunks: []string{missing.String(), "invalid"}},
	}}
	if n, err := e.eng.missingChunks(context.Background(), tree); err != nil || n != 2 {
		t.Fatalf("missing references = %d, %v", n, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stepped := &stepContext{Context: ctx, cancel: cancel, at: 2}
	if n, err := e.eng.missingChunks(stepped, tree); !errors.Is(err, context.Canceled) || n != 0 {
		t.Fatalf("cancelled reference scan = %d, %v", n, err)
	}
}

type verifyGate struct {
	storage.Backend
	started chan struct{}
	release chan struct{}
}

func (b *verifyGate) Get(ctx context.Context, key string) ([]byte, error) {
	if strings.HasPrefix(key, "chunks/") {
		b.started <- struct{}{}
		select {
		case <-b.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return b.Backend.Get(ctx, key)
}

func TestVerifyBoundsWorkersAndCancels(t *testing.T) {
	for _, cancelRun := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelRun), func(t *testing.T) {
			e := newEnv(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			chunks := make(map[crypto.ID]int)
			for i := range 256 {
				data := []byte(fmt.Sprint(i))
				id := e.key.ChunkID(data)
				if _, err := e.eng.Repo.PutChunk(ctx, id, data); err != nil {
					t.Fatal(err)
				}
				chunks[id] = len(data)
			}
			if err := e.eng.Manifest.AddChunks(chunks); err != nil {
				t.Fatal(err)
			}
			gate := &verifyGate{Backend: e.mem, started: make(chan struct{}, len(chunks)), release: make(chan struct{})}
			e.eng.Repo.Backend, e.eng.Downloaders = gate, 3
			before := runtime.NumGoroutine()
			type outcome struct {
				res VerifyResult
				err error
			}
			done := make(chan outcome, 1)
			go func() {
				res, err := e.eng.Verify(ctx, len(chunks), false)
				done <- outcome{res, err}
			}()
			var release sync.Once
			unblock := func() { release.Do(func() { close(gate.release) }) }
			defer unblock()
			for range e.eng.Downloaders {
				select {
				case <-gate.started:
				case <-time.After(5 * time.Second):
					t.Fatal("verification workers didn't start")
				}
			}
			if added := runtime.NumGoroutine() - before; added > e.eng.Downloaders+8 {
				unblock()
				<-done
				t.Fatalf("verification started %d goroutines for %d workers", added, e.eng.Downloaders)
			}
			if cancelRun {
				cancel()
			} else {
				unblock()
			}
			got := <-done
			if cancelRun {
				if !errors.Is(got.err, context.Canceled) {
					t.Fatalf("cancelled verification returned %v", got.err)
				}
				if _, ok := e.eng.LastVerify(); ok {
					t.Fatal("cancelled verification saved a completed result")
				}
			} else if got.err != nil || !got.res.OK() || got.res.Checked != len(chunks) {
				t.Fatalf("verification = %+v, %v", got.res, got.err)
			}
		})
	}
}

// listCounter counts listings of chunks/.
type listCounter struct {
	storage.Backend
	mu    sync.Mutex
	lists int
}

func (b *listCounter) List(ctx context.Context, prefix string) ([]string, error) {
	if strings.HasPrefix(prefix, "chunks/") {
		b.mu.Lock()
		b.lists++
		b.mu.Unlock()
	}
	return b.Backend.List(ctx, prefix)
}

func (b *listCounter) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lists
}

func TestBackupTrustsRecentChunkList(t *testing.T) {
	e := newEnv(t)
	lc := &listCounter{Backend: e.mem}
	e.eng.Repo.Backend = lc
	e.write("a.bin", random(2<<20, 21))

	e.backup(BackupOptions{})
	if lc.count() != 1 {
		t.Fatalf("first backup listed chunks %d times, want 1", lc.count())
	}
	e.write("b.bin", random(1<<20, 22))
	e.backup(BackupOptions{})
	if _, err := e.eng.Verify(context.Background(), 20, false); err != nil {
		t.Fatal(err)
	}
	if lc.count() != 1 {
		t.Fatalf("backup and verify inside the sync window listed chunks %d times in all, want 1", lc.count())
	}

	// A week later the list is refreshed once.
	if err := e.eng.Manifest.PutMeta(metaChunkSync, chunkSync{Time: time.Now().Add(-syncEvery - time.Hour)}); err != nil {
		t.Fatal(err)
	}
	e.backup(BackupOptions{})
	e.backup(BackupOptions{})
	if lc.count() != 2 {
		t.Fatalf("listed chunks %d times after the window, want 2", lc.count())
	}

	// A full verification always lists.
	if _, err := e.eng.Verify(context.Background(), 20, true); err != nil {
		t.Fatal(err)
	}
	if lc.count() != 3 {
		t.Fatalf("full verify didn't list chunks: %d", lc.count())
	}
}

// placed gives a backend a location of its own, as two buckets would have.
type placed struct {
	storage.Backend
	where string
}

func (p placed) Location() string { return p.where }

// restorable checks every file in snapshot id comes back.
func (e *env) restorable(t *testing.T, id string) {
	t.Helper()
	if _, err := e.eng.Restore(context.Background(), id, RestoreOptions{Target: t.TempDir()}); err != nil {
		t.Fatalf("snapshot %s doesn't restore: %v", id, err)
	}
}

// Only frost.repo moved: the data isn't there, and the first backup in the
// new place must notice instead of trusting the chunk list.
func TestRepoFileMovedAloneUploadsEverything(t *testing.T) {
	e := newEnv(t)
	e.write("a.bin", random(3<<20, 23))
	e.backup(BackupOptions{})
	fresh := storagetest.NewMem()
	fresh.SetRaw("frost.repo", e.mem.Raw("frost.repo"))
	r, err := repo.Open(context.Background(), placed{fresh, "elsewhere"}, e.key)
	if err != nil {
		t.Fatal(err)
	}
	e.eng.Repo = r
	res := e.backup(BackupOptions{})
	if res.Snapshot.Stats.NewChunks == 0 {
		t.Fatal("backup in the new place trusted chunks that aren't there")
	}
	e.restorable(t, res.Snapshot.ID)
}

// Two copies of one repository: backing up to one says nothing about what
// the other holds.
func TestCopiesOfOneRepoDontShareChunkList(t *testing.T) {
	e := newEnv(t)
	e.write("a.bin", random(2<<20, 24))
	e.backup(BackupOptions{})
	copyMem := storagetest.NewMem()
	keys, _ := e.mem.List(context.Background(), "")
	for _, k := range keys {
		copyMem.SetRaw(k, e.mem.Raw(k))
	}
	original := e.eng.Repo.Backend
	e.eng.Repo.Backend = placed{copyMem, "the copy"}
	e.write("b.bin", random(2<<20, 25)) // only ever uploaded to the copy
	e.backup(BackupOptions{})

	e.eng.Repo.Backend = original
	res := e.backup(BackupOptions{})
	e.restorable(t, res.Snapshot.ID)
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
