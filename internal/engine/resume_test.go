package engine

import (
	"bytes"
	"context"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rhymeswithlimo/frost/internal/storage"
)

// flakyBackend delays chunk downloads by a random amount, so they finish out
// of order, and fails every one after the first okGets when okGets >= 0.
type flakyBackend struct {
	storage.Backend
	mu     sync.Mutex
	rnd    *rand.Rand
	gets   int // chunk downloads that returned data
	okGets int
}

var errFlaky = errors.New("connection lost")

func (b *flakyBackend) Get(ctx context.Context, key string) ([]byte, error) {
	if !strings.HasPrefix(key, "chunks/") {
		return b.Backend.Get(ctx, key)
	}
	b.mu.Lock()
	d := time.Duration(b.rnd.Intn(2000)) * time.Microsecond
	fail := b.okGets >= 0 && b.gets >= b.okGets
	if !fail {
		b.gets++
	}
	b.mu.Unlock()
	time.Sleep(d)
	if fail {
		return nil, errFlaky
	}
	return b.Backend.Get(ctx, key)
}

func (b *flakyBackend) chunkGets() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.gets
}

// restoredRoot is where e.src lands under a plain target.
func (e *env) restoredRoot(target string) string {
	rel, _ := filepath.Rel(filepath.VolumeName(e.src)+string(filepath.Separator), e.src)
	return filepath.Join(target, strings.TrimSuffix(filepath.VolumeName(e.src), ":"), rel)
}

func sameFiles(t *testing.T, want map[string][]byte, root string) {
	t.Helper()
	for name, data := range want {
		got, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("%s differs after restore", name)
		}
	}
}

func TestParallelRestoreIsExact(t *testing.T) {
	e := newEnv(t)
	want := map[string][]byte{}
	for i, size := range []int{0, 1, 300 << 10, 3 << 20, 9 << 20, 17 << 20} {
		name := filepath.Join("d", string(rune('a'+i))+".bin")
		want[name] = random(size, int64(30+i))
		e.write(name, want[name])
	}
	for i := range 40 {
		name := filepath.Join("small", string(rune('a'+i%26))+string(rune('a'+i/26))+".txt")
		want[name] = []byte(strings.Repeat("x", i))
		e.write(name, want[name])
	}
	res := e.backup(BackupOptions{})
	e.eng.Repo.Backend = &flakyBackend{Backend: e.mem, rnd: rand.New(rand.NewSource(1)), okGets: -1}
	target := t.TempDir()
	if _, err := e.eng.Restore(context.Background(), res.Snapshot.ID, RestoreOptions{Target: target}); err != nil {
		t.Fatal(err)
	}
	sameFiles(t, want, e.restoredRoot(target))
}

func TestRestoreFetchesRepeatedChunkOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("writes 200 MiB")
	}
	e := newEnv(t)
	// 25 identical 8 MiB chunks: more than the download window.
	p := filepath.Join(e.src, "disk.img")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(p, 200<<20); err != nil {
		t.Fatal(err)
	}
	res := e.backup(BackupOptions{})
	fb := &flakyBackend{Backend: e.mem, rnd: rand.New(rand.NewSource(2)), okGets: -1}
	e.eng.Repo.Backend = fb
	if _, err := e.eng.Repo.LoadTree(context.Background(), res.Snapshot.ID); err != nil {
		t.Fatal(err)
	}
	treeGets := fb.chunkGets()
	target := t.TempDir()
	if _, err := e.eng.Restore(context.Background(), res.Snapshot.ID, RestoreOptions{Target: target}); err != nil {
		t.Fatal(err)
	}
	if n := fb.chunkGets() - 2*treeGets; n != 1 {
		t.Fatalf("restoring 200 MiB of zeros downloaded %d chunks, want 1", n)
	}
	if info, err := os.Stat(filepath.Join(e.restoredRoot(target), "disk.img")); err != nil || info.Size() != 200<<20 {
		t.Fatalf("restored disk: %v", err)
	}
}

func TestInterruptedRestoreCarriesOn(t *testing.T) {
	for _, tamper := range []bool{false, true} {
		name := "clean"
		if tamper {
			name = "tampered"
		}
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			want := map[string][]byte{
				"a.txt":   []byte("first"),
				"big.bin": random(40<<20, 40),
				"z.txt":   []byte("last"),
			}
			for n, d := range want {
				e.write(n, d)
			}
			res := e.backup(BackupOptions{})
			id := res.Snapshot.ID
			include := []string{filepath.ToSlash(e.src)}
			parent := t.TempDir()
			target, resume, err := NewRestoreFolder(parent, id, include)
			if err != nil || resume {
				t.Fatalf("new folder: %q, %v, %v", target, resume, err)
			}
			opts := RestoreOptions{Target: target, NewTarget: true, Base: filepath.ToSlash(e.src), Include: include}

			fb := &flakyBackend{Backend: e.mem, rnd: rand.New(rand.NewSource(3)), okGets: 25}
			e.eng.Repo.Backend = fb
			out, err := e.eng.Restore(context.Background(), id, opts)
			if !errors.Is(err, errFlaky) || !out.Unfinished {
				t.Fatalf("interrupted restore: %+v, %v", out, err)
			}
			if _, err := os.Stat(filepath.Join(target, restoreMarker)); err != nil {
				t.Fatalf("marker gone after an interrupted restore: %v", err)
			}
			partial := filepath.Join(target, partialName(id, filepath.ToSlash(filepath.Join(e.src, "big.bin"))))
			info, err := os.Stat(partial)
			if err != nil || info.Size() == 0 {
				t.Fatalf("no partial file kept: %v", err)
			}
			if tamper {
				f, err := os.OpenFile(partial, os.O_RDWR, 0)
				if err != nil {
					t.Fatal(err)
				}
				f.WriteAt([]byte("tampered"), info.Size()/2)
				f.Close()
			}

			again, resume, err := NewRestoreFolder(parent, id, include)
			if err != nil || !resume || again != target {
				t.Fatalf("rerun picked %q (resume %v, %v), want %q", again, resume, err, target)
			}
			if other, resume, _ := NewRestoreFolder(parent, id, []string{"/somewhere/else"}); resume || other == target {
				t.Fatal("a different selection reused the unfinished folder")
			}
			fb = &flakyBackend{Backend: e.mem, rnd: rand.New(rand.NewSource(4)), okGets: -1}
			e.eng.Repo.Backend = fb
			if _, err := e.eng.Restore(context.Background(), id, opts); err != nil {
				t.Fatal(err)
			}
			sameFiles(t, want, target)
			entries, _ := os.ReadDir(target)
			for _, en := range entries {
				if strings.HasPrefix(en.Name(), ".frost-") {
					t.Fatalf("%s left behind", en.Name())
				}
			}
			// big.bin is about 40 chunks and a.txt one. The rerun only
			// fetches what wasn't kept, and a tampered partial is cut back
			// to before the damage.
			total := 0
			tree, _ := e.eng.Repo.LoadTree(context.Background(), id)
			for _, f := range tree.Files {
				total += len(f.Chunks)
			}
			if got := fb.chunkGets(); got >= total {
				t.Fatalf("rerun downloaded %d of %d chunks, nothing was reused", got, total)
			}
		})
	}
}

func TestRestoreRefusesBusyFolderAndPartial(t *testing.T) {
	e := newEnv(t)
	e.write("big.bin", random(4<<20, 50))
	res := e.backup(BackupOptions{})
	id := res.Snapshot.ID
	include := []string{filepath.ToSlash(e.src)}
	parent := t.TempDir()

	// A folder whose restore another process is running isn't reused.
	busy := filepath.Join(parent, RestoreFolderName(id))
	os.Mkdir(busy, 0o700)
	m, _ := os.Create(filepath.Join(busy, restoreMarker))
	m.WriteString(`{"snapshot":"` + id + `","include":["` + include[0] + `"]}`)
	if err := lockFile(m); err != nil {
		t.Fatal(err)
	}
	if got, resume, _ := NewRestoreFolder(parent, id, include); got == busy || resume {
		t.Fatal("reused a folder another restore holds")
	}
	m.Close()
	if got, resume, _ := NewRestoreFolder(parent, id, include); got != busy || !resume {
		t.Fatalf("didn't reuse the unfinished folder once it was free: %q", got)
	}

	// A partial file another process is writing isn't touched.
	target := t.TempDir()
	dir := e.restoredRoot(target)
	os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, partialName(id, filepath.ToSlash(filepath.Join(e.src, "big.bin"))))
	f, _ := os.Create(p)
	f.WriteString("someone else's")
	if err := lockFile(f); err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := e.eng.Restore(context.Background(), id, RestoreOptions{Target: target}); err == nil || !strings.Contains(err.Error(), "another restore") {
		t.Fatalf("restore over a locked partial: %v", err)
	}
}

func TestOverwriteSkipsUnchangedOriginals(t *testing.T) {
	e := newEnv(t)
	if resolved, err := filepath.EvalSymlinks(e.src); err == nil {
		e.src = resolved
	}
	e.write("same.bin", random(6<<20, 60))
	e.write("changed.txt", []byte("saved"))
	res := e.backup(BackupOptions{})
	e.write("changed.txt", []byte("edited"))
	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	os.Chtimes(filepath.Join(e.src, "same.bin"), past, past) // content still right

	fb := &flakyBackend{Backend: e.mem, rnd: rand.New(rand.NewSource(5)), okGets: -1}
	e.eng.Repo.Backend = fb
	if _, err := e.eng.Repo.LoadTree(context.Background(), res.Snapshot.ID); err != nil {
		t.Fatal(err)
	}
	treeGets := fb.chunkGets()
	if _, err := e.eng.Restore(context.Background(), res.Snapshot.ID, RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := fb.chunkGets() - 2*treeGets; n != 1 {
		t.Fatalf("overwrite downloaded %d chunks, want 1 (only the changed file)", n)
	}
	if got, _ := os.ReadFile(filepath.Join(e.src, "changed.txt")); string(got) != "saved" {
		t.Fatalf("changed file: %q", got)
	}
	if info, _ := os.Stat(filepath.Join(e.src, "same.bin")); info.ModTime().Equal(past) {
		t.Fatal("unchanged file kept the wrong mtime")
	}
}
