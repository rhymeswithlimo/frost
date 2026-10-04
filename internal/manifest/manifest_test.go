package manifest

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/rhymeswithlimo/frost/internal/crypto"
	"github.com/rhymeswithlimo/frost/internal/snapshot"
)

func newManifest(t testing.TB) *Manifest {
	t.Helper()
	m, err := Open(filepath.Join(t.TempDir(), "manifest.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

func TestChunkPresenceAndSampling(t *testing.T) {
	m := newManifest(t)
	if m.AnyChunks() || m.ChunkCount() != 0 || !m.HasChunks(nil) {
		t.Fatal("empty chunk cache has incorrect presence")
	}
	ids := []crypto.ID{{1}, {2}, {3}}
	if err := m.AddChunks(map[crypto.ID]int{ids[0]: 10, ids[1]: 20, ids[2]: 30}); err != nil {
		t.Fatal(err)
	}
	if !m.AnyChunks() || m.ChunkCount() != len(ids) {
		t.Fatal("uploaded chunks weren't recorded")
	}
	for _, tc := range []struct {
		chunks []string
		want   bool
	}{
		{[]string{ids[0].String(), ids[1].String(), ids[0].String()}, true},
		{[]string{ids[0].String(), (crypto.ID{4}).String()}, false},
		{[]string{ids[0].String(), "invalid"}, false},
	} {
		if got := m.HasChunks(tc.chunks); got != tc.want {
			t.Errorf("HasChunks(%v) = %v, want %v", tc.chunks, got, tc.want)
		}
	}
	for _, n := range []int{-1, 0, 1, 2, 3, 10} {
		sample := m.SampleChunks(n)
		if len(sample) != min(max(n, 0), len(ids)) {
			t.Errorf("sample of %d returned %d IDs", n, len(sample))
		}
		seen := make(map[crypto.ID]bool)
		for _, id := range sample {
			if seen[id] || !m.HasChunk(id) {
				t.Fatalf("sample contains duplicate or unknown ID %s", id)
			}
			seen[id] = true
		}
	}
	if err := m.ReplaceChunks(ids[:1]); err != nil {
		t.Fatal(err)
	}
	if m.HasChunks([]string{ids[1].String()}) || !m.HasChunk(ids[0]) || m.ChunkCount() != 1 {
		t.Fatal("rebuild kept stale chunks")
	}
	if err := m.ReplaceChunks(nil); err != nil {
		t.Fatal(err)
	}
	if m.AnyChunks() {
		t.Fatal("empty rebuild still reports chunks")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if m.HasChunks([]string{ids[0].String()}) {
		t.Fatal("an unreadable cache reported known chunks")
	}
}

func TestSnapshotUpsert(t *testing.T) {
	m := newManifest(t)
	first := snapshot.Snapshot{ID: "first", Host: "one", Time: time.Now().UTC()}
	second := snapshot.Snapshot{ID: "second", Host: "two", Time: first.Time.Add(time.Hour)}
	if err := m.SetSnapshots([]snapshot.Snapshot{first}); err != nil {
		t.Fatal(err)
	}
	if err := m.PutSnapshot(second); err != nil {
		t.Fatal(err)
	}
	second.Stats.Files = 4
	if err := m.PutSnapshot(second); err != nil {
		t.Fatal(err)
	}
	want := map[string]snapshot.Snapshot{first.ID: first, second.ID: second}
	if got := m.Snapshots(); !reflect.DeepEqual(got, want) {
		t.Fatalf("upsert changed other headers: %+v", got)
	}
	if err := m.SetSnapshots([]snapshot.Snapshot{second}); err != nil {
		t.Fatal(err)
	}
	if got := m.Snapshots(); len(got) != 1 || !reflect.DeepEqual(got[second.ID], second) {
		t.Fatalf("replacement kept a removed header: %+v", got)
	}
}

func BenchmarkCachedChunks(b *testing.B) {
	m := newManifest(b)
	var ids []string
	chunks := make(map[crypto.ID]int)
	for i := range 1000 {
		id := crypto.ID{byte(i), byte(i >> 8)}
		chunks[id] = 100
		ids = append(ids, id.String())
	}
	if err := m.AddChunks(chunks); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if !m.HasChunks(ids) {
			b.Fatal("known chunks missing")
		}
	}
}

func BenchmarkSnapshotUpsert(b *testing.B) {
	m := newManifest(b)
	var snaps []snapshot.Snapshot
	for i := range 1000 {
		snaps = append(snaps, snapshot.Snapshot{ID: fmt.Sprintf("snapshot-%d", i), Host: "host"})
	}
	if err := m.SetSnapshots(snaps); err != nil {
		b.Fatal(err)
	}
	s := snapshot.Snapshot{ID: "new-snapshot", Host: "host"}
	b.ReportAllocs()
	for b.Loop() {
		if err := m.PutSnapshot(s); err != nil {
			b.Fatal(err)
		}
	}
}
