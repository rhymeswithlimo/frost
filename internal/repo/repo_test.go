package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rhymeswithlimo/frost/internal/crypto"
	"github.com/rhymeswithlimo/frost/internal/snapshot"
	"github.com/rhymeswithlimo/frost/internal/storage"
	"github.com/rhymeswithlimo/frost/internal/storage/storagetest"
)

func TestConcurrentInitHasOneWinner(t *testing.T) {
	m := storagetest.NewMem()
	var successes atomic.Int32
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			k, _ := crypto.NewKey()
			if _, err := Init(context.Background(), m, k); err == nil {
				successes.Add(1)
			}
		})
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("successful initializations: %d", successes.Load())
	}
}

func TestMetadataValidationAndImmutability(t *testing.T) {
	ctx := context.Background()
	k, _ := crypto.NewKey()
	m := storagetest.NewMem()
	r, err := Init(ctx, m, k)
	if err != nil {
		t.Fatal(err)
	}
	s := snapshot.Snapshot{ID: snapshot.NewID()}
	if _, err := r.SaveSnapshot(ctx, s, &snapshot.Tree{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SaveSnapshot(ctx, s, &snapshot.Tree{Files: []snapshot.File{{Path: "/changed"}}}, nil); err == nil {
		t.Fatal("overwrote snapshot")
	}
	tree, err := r.LoadTree(ctx, s.ID)
	if err != nil || len(tree.Files) != 0 {
		t.Fatal("original tree changed")
	}
	before, _ := r.ChunkIDs(ctx) // the file list's chunk
	id := k.ChunkID([]byte("x"))
	m.SetRaw("chunks/wrong/"+id.String(), []byte("x"))
	if ids, err := r.ChunkIDs(ctx); err != nil || len(ids) != len(before) {
		t.Fatal("noncanonical chunk counted")
	}
	r.Info.ID = "../../escape"
	raw, _ := json.Marshal(r.Info)
	m.SetRaw(infoKey, k.Seal(raw, infoKey))
	if _, err := Open(ctx, m, k); err == nil {
		t.Fatal("unsafe repository ID accepted")
	}
}

func TestInitRefusesOrphanedBackupObjects(t *testing.T) {
	m := storagetest.NewMem()
	m.SetRaw("trees/old", []byte("preserve"))
	k, _ := crypto.NewKey()
	if _, err := Init(context.Background(), m, k); err == nil {
		t.Fatal("initialized over existing backup objects")
	}
}

func TestChunkKeyFormat(t *testing.T) {
	for _, first := range []byte{0, 1, 0xab, 0xff} {
		var id crypto.ID
		for i := range id {
			id[i] = first + byte(i)
		}
		hex := id.String()
		if got, want := ChunkKey(id), "chunks/"+hex[:2]+"/"+hex; got != want {
			t.Fatalf("chunk key = %q, want %q", got, want)
		}
	}
}

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

type failingChunks struct {
	*storagetest.Mem
	calls atomic.Int32
	err   error
}

func (b *failingChunks) Put(ctx context.Context, key string, data []byte) error {
	if strings.HasPrefix(key, "chunks/") {
		b.calls.Add(1)
		return b.err
	}
	return b.Mem.Put(ctx, key, data)
}

func TestSaveSnapshotStopsAfterUploadFailure(t *testing.T) {
	r, m := newRepo(t)
	want := errors.New("upload failed")
	b := &failingChunks{Mem: m, err: want}
	r.Backend = b
	_, err := r.SaveSnapshot(context.Background(), snapshot.Snapshot{ID: "failed-upload"}, bigTree(200, 1000, 5), nil)
	if !errors.Is(err, want) {
		t.Fatalf("upload failure lost: %v", err)
	}
	if calls := b.calls.Load(); calls < 1 || calls > 4 {
		t.Fatalf("made %d uploads after a failure", calls)
	}
	for _, prefix := range []string{"trees/", "snapshots/"} {
		if keys, err := m.List(context.Background(), prefix); err != nil || len(keys) != 0 {
			t.Fatalf("committed metadata after a failed upload: %v, %v", keys, err)
		}
	}
}

func TestFetchWindowEdges(t *testing.T) {
	r, _ := newRepo(t)
	ctx := context.Background()
	data := []byte("small chunk")
	id := r.Key.ChunkID(data)
	if _, err := r.PutChunk(ctx, id, data); err != nil {
		t.Fatal(err)
	}
	for _, workers := range []int{-1, 1, 2, 8, 1000} {
		for _, count := range []int{0, 1, 2, 15, 16, 17, 200} {
			ids := make([]crypto.ID, count)
			for i := range ids {
				ids[i] = id
			}
			calls := 0
			if err := r.Fetch(ctx, ids, workers, func(i int, got []byte) error {
				if i != calls || string(got) != string(data) {
					t.Fatalf("workers %d, count %d, entry %d out of order", workers, count, i)
				}
				calls++
				return nil
			}); err != nil || calls != count {
				t.Fatalf("workers %d, count %d: %d calls, %v", workers, count, calls, err)
			}
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := r.Fetch(canceled, []crypto.ID{id}, 8, func(int, []byte) error {
		t.Fatal("called back after cancellation")
		return nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled fetch: %v", err)
	}
}

func TestFetchCallbackCancellation(t *testing.T) {
	r, _ := newRepo(t)
	data := []byte("cancel after callback")
	id := r.Key.ChunkID(data)
	if _, err := r.PutChunk(context.Background(), id, data); err != nil {
		t.Fatal(err)
	}
	for _, count := range []int{1, 32} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ids := make([]crypto.ID, count)
			for i := range ids {
				ids[i] = id
			}
			calls := 0
			err := r.Fetch(ctx, ids, 8, func(int, []byte) error {
				calls++
				cancel()
				return nil
			})
			if !errors.Is(err, context.Canceled) || calls != 1 {
				t.Fatalf("callback cancellation returned %v after %d calls", err, calls)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	want := errors.New("callback failed")
	err := r.Fetch(ctx, []crypto.ID{id}, 1, func(int, []byte) error {
		cancel()
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("callback error lost to cancellation: %v", err)
	}
}

func BenchmarkFetch(b *testing.B) {
	k, _ := crypto.NewKey()
	m := storagetest.NewMem()
	r := &Repo{Backend: m, Key: k}
	id := k.ChunkID([]byte("tiny"))
	if _, err := r.PutChunk(context.Background(), id, []byte("tiny")); err != nil {
		b.Fatal(err)
	}
	ids := make([]crypto.ID, 100000)
	for i := range ids {
		ids[i] = id
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if err := r.Fetch(context.Background(), ids, 8, func(int, []byte) error { return nil }); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSaveSnapshot(b *testing.B) {
	k, _ := crypto.NewKey()
	tree := bigTree(200, 1000, 1)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		r := &Repo{Backend: storagetest.NewMem(), Key: k}
		if _, err := r.SaveSnapshot(context.Background(), snapshot.Snapshot{ID: "benchmark"}, tree, nil); err != nil {
			b.Fatal(err)
		}
	}
}
