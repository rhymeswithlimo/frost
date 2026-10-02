package engine

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rhymeswithlimo/frost/internal/repo"
	"github.com/rhymeswithlimo/frost/internal/storage"
	"github.com/rhymeswithlimo/frost/internal/storage/storagetest"
)

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
