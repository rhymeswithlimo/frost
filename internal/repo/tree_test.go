package repo

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rhymeswithlimo/frost/internal/crypto"
	"github.com/rhymeswithlimo/frost/internal/snapshot"
	"github.com/rhymeswithlimo/frost/internal/storage"
	"github.com/rhymeswithlimo/frost/internal/storage/storagetest"
)

func newRepo(t *testing.T) (*Repo, *storagetest.Mem) {
	t.Helper()
	k, _ := crypto.NewKey()
	m := storagetest.NewMem()
	r, err := Init(context.Background(), m, k)
	if err != nil {
		t.Fatal(err)
	}
	return r, m
}

// bigTree has files with made-up chunk IDs, enough to span many pieces.
func bigTree(files, chunks int, seed int64) *snapshot.Tree {
	rnd := rand.New(rand.NewSource(seed))
	t := &snapshot.Tree{}
	mtime := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	for i := range files {
		f := snapshot.File{Path: fmt.Sprintf("/home/me/disk-%05d.img", i), Type: snapshot.TypeFile, Mode: 0o644, ModTime: mtime, Size: int64(chunks) << 20}
		for range chunks {
			var id crypto.ID
			rnd.Read(id[:])
			f.Chunks = append(f.Chunks, id.String())
		}
		t.Files = append(t.Files, f)
	}
	return t
}

func TestTreeRoundTripAndDedupe(t *testing.T) {
	ctx := context.Background()
	r, m := newRepo(t)
	tree := bigTree(200, 1000, 1) // ~13 MB of JSON
	first := snapshot.Snapshot{ID: snapshot.NewID()}
	up, err := r.SaveSnapshot(ctx, first, tree, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(up) < 5 {
		t.Fatalf("file list went up in %d pieces, want several", len(up))
	}
	got, err := r.LoadTree(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, tree) {
		t.Fatal("file list changed in a round trip")
	}

	// One file changes: only the pieces around it go up again.
	tree.Files[100].ModTime = tree.Files[100].ModTime.Add(time.Second)
	have := func(id crypto.ID) bool { _, ok := up[id]; return ok }
	puts := m.Puts
	again, err := r.SaveSnapshot(ctx, snapshot.Snapshot{ID: snapshot.NewID()}, tree, have)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) > 2 {
		t.Fatalf("a one-file change uploaded %d of %d pieces", len(again), len(up))
	}
	if n := m.Puts - puts; n != len(again)+2 {
		t.Fatalf("second snapshot made %d puts, want %d pieces + index + header", n, len(again))
	}
}

func TestTreeMissingPieceFails(t *testing.T) {
	ctx := context.Background()
	r, m := newRepo(t)
	s := snapshot.Snapshot{ID: snapshot.NewID()}
	up, err := r.SaveSnapshot(ctx, s, bigTree(50, 1000, 2), nil)
	if err != nil {
		t.Fatal(err)
	}
	for id := range up {
		m.Delete(ctx, ChunkKey(id))
		break
	}
	if _, err := r.LoadTree(ctx, s.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("file list with a missing piece: %v", err)
	}
}

// slowMem delays gets by a random amount so downloads finish out of order.
type slowMem struct {
	*storagetest.Mem
	mu  sync.Mutex
	rnd *rand.Rand
}

func (s *slowMem) Get(ctx context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	d := time.Duration(s.rnd.Intn(3000)) * time.Microsecond
	s.mu.Unlock()
	time.Sleep(d)
	return s.Mem.Get(ctx, key)
}

func TestFetchOrderAndDedupe(t *testing.T) {
	ctx := context.Background()
	r, m := newRepo(t)
	r.Backend = &slowMem{Mem: m, rnd: rand.New(rand.NewSource(3))}
	var ids []crypto.ID
	for i := range 40 {
		data := []byte(fmt.Sprintf("chunk %d", i))
		id := r.Key.ChunkID(data)
		if _, err := r.PutChunk(ctx, id, data); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	// 30 copies of one chunk in a row is longer than the window of 8.
	seq := append([]crypto.ID(nil), ids...)
	for range 30 {
		seq = append(seq, ids[0])
	}
	seq = append(seq, ids[5])
	gets := m.Gets
	var got []string
	err := r.Fetch(ctx, seq, 4, func(i int, data []byte) error {
		if r.Key.ChunkID(data) != seq[i] {
			return fmt.Errorf("entry %d got the wrong chunk", i)
		}
		got = append(got, string(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(seq) || got[0] != "chunk 0" || got[39] != "chunk 39" || got[40] != "chunk 0" || got[len(got)-1] != "chunk 5" {
		t.Fatalf("out of order: %v", got)
	}
	// 40 distinct, plus chunk 0 again (its first copy left the window long
	// before the run), plus chunk 5 again.
	if n := m.Gets - gets; n != 42 {
		t.Fatalf("fetched %d times, want 42", n)
	}

	stop := errors.New("stop")
	calls := 0
	err = r.Fetch(ctx, seq, 4, func(int, []byte) error {
		if calls++; calls == 3 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || calls != 3 {
		t.Fatalf("fetch didn't stop at the first error: %v after %d calls", err, calls)
	}
	if err := r.Fetch(ctx, []crypto.ID{r.Key.ChunkID([]byte("never stored"))}, 4, func(int, []byte) error { return nil }); !errors.Is(err, storage.ErrNotFound) || !strings.Contains(err.Error(), "chunk") {
		t.Fatalf("missing chunk: %v", err)
	}
}
