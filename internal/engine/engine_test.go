package engine

import (
	"bytes"
	"context"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/rhymeswithlimo/frost/internal/crypto"
	"github.com/rhymeswithlimo/frost/internal/manifest"
	"github.com/rhymeswithlimo/frost/internal/repo"
	"github.com/rhymeswithlimo/frost/internal/snapshot"
	"github.com/rhymeswithlimo/frost/internal/storage/storagetest"
)

type env struct {
	t     *testing.T
	src   string
	mem   *storagetest.Mem
	key   *crypto.Key
	eng   *Engine
	mpath string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	key, _ := crypto.NewKey()
	mem := storagetest.NewMem()
	r, err := repo.Init(ctx, mem, key)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, src: t.TempDir(), mem: mem, key: key, mpath: filepath.Join(t.TempDir(), "m.db")}
	e.eng = e.open(r)
	return e
}

func (e *env) open(r *repo.Repo) *Engine {
	m, err := manifest.Open(e.mpath)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { m.Close() })
	return &Engine{Repo: r, Manifest: m}
}

func (e *env) write(rel string, data []byte) {
	p := filepath.Join(e.src, rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) backup(opts BackupOptions) BackupResult {
	e.t.Helper()
	if opts.Paths == nil {
		opts.Paths = []string{e.src}
	}
	res, err := e.eng.Backup(context.Background(), opts)
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

func random(n int, seed int64) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

func TestRoundTrip(t *testing.T) {
	e := newEnv(t)
	big := random(5<<20, 1)
	e.write("docs/a.txt", []byte("hello"))
	e.write("docs/sub/big.bin", big)
	e.write("empty", nil)
	// Creating symlinks needs extra privileges on Windows.
	haveLink := os.Symlink("docs/a.txt", filepath.Join(e.src, "link")) == nil

	res := e.backup(BackupOptions{})
	if res.Snapshot.Stats.Files != 3 {
		t.Fatalf("files = %d, want 3", res.Snapshot.Stats.Files)
	}

	target := t.TempDir()
	if _, err := e.eng.Restore(context.Background(), res.Snapshot.ID, RestoreOptions{Target: target}); err != nil {
		t.Fatal(err)
	}
	rel, _ := filepath.Rel(filepath.VolumeName(e.src)+string(filepath.Separator), e.src)
	root := filepath.Join(target, strings.TrimSuffix(filepath.VolumeName(e.src), ":"), rel)

	for name, want := range map[string][]byte{"docs/a.txt": []byte("hello"), "docs/sub/big.bin": big, "empty": {}} {
		got, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s differs after restore", name)
		}
	}
	if !haveLink {
		return
	}
	tree, err := e.eng.Repo.LoadTree(context.Background(), res.Snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range tree.Files {
		if f.Type == snapshot.TypeSymlink && f.Target != "docs/a.txt" {
			t.Errorf("stored symlink target = %q, want docs/a.txt", f.Target)
		}
	}
	// Windows returns the target with backslashes.
	if l, err := os.Readlink(filepath.Join(root, "link")); err != nil || filepath.ToSlash(l) != "docs/a.txt" {
		t.Errorf("symlink = %q, %v", l, err)
	}
}

func TestIncrementalUploadsOnlyChanges(t *testing.T) {
	e := newEnv(t)
	data := random(12<<20, 2)
	e.write("big.bin", data)
	e.write("other.txt", []byte("unchanged"))
	first := e.backup(BackupOptions{})
	if first.Snapshot.Stats.NewChunks == 0 {
		t.Fatal("first backup uploaded nothing")
	}

	// Nothing changed: nothing uploaded, only snapshot objects written.
	putsBefore := e.mem.Puts
	second := e.backup(BackupOptions{})
	if second.Snapshot.Stats.NewChunks != 0 {
		t.Fatalf("unchanged backup uploaded %d chunks", second.Snapshot.Stats.NewChunks)
	}
	if got := e.mem.Puts - putsBefore; got != 2 {
		t.Fatalf("unchanged backup made %d puts, want 2 (tree + header)", got)
	}

	// Edit a few bytes in the middle: only one or two chunks change.
	copy(data[6<<20:], []byte("an edit in the middle"))
	e.write("big.bin", data)
	third := e.backup(BackupOptions{})
	if n := third.Snapshot.Stats.NewChunks; n < 1 || n > 2 {
		t.Fatalf("small edit uploaded %d chunks, want 1 or 2", n)
	}
}

func TestDryRunUploadsNothing(t *testing.T) {
	e := newEnv(t)
	e.write("a.bin", random(2<<20, 3))
	puts := e.mem.Puts
	res := e.backup(BackupOptions{DryRun: true})
	if e.mem.Puts != puts {
		t.Fatalf("dry run made %d puts", e.mem.Puts-puts)
	}
	if len(res.Planned) != 1 || res.Planned[0].NewBytes != 2<<20 {
		t.Fatalf("planned = %+v", res.Planned)
	}
	// A real run afterwards still uploads everything.
	if real := e.backup(BackupOptions{}); real.Snapshot.Stats.NewBytes != 2<<20 {
		t.Fatalf("real run after dry run uploaded %d bytes", real.Snapshot.Stats.NewBytes)
	}
}

func TestExclude(t *testing.T) {
	e := newEnv(t)
	e.write("keep.txt", []byte("x"))
	e.write("skip.tmp", []byte("x"))
	e.write("node_modules/pkg/index.js", []byte("x"))
	e.write("cache/big", []byte("x"))
	res := e.backup(BackupOptions{Exclude: []string{"*.tmp", "node_modules", filepath.ToSlash(filepath.Join(e.src, "cache"))}})
	if res.Snapshot.Stats.Files != 1 {
		t.Fatalf("files = %d, want 1", res.Snapshot.Stats.Files)
	}
}

func TestVerifyCatchesTampering(t *testing.T) {
	e := newEnv(t)
	e.write("a.bin", random(3<<20, 4))
	e.backup(BackupOptions{})

	v, err := e.eng.Verify(context.Background(), 100)
	if err != nil || !v.OK() {
		t.Fatalf("clean verify: %+v, %v", v, err)
	}

	keys, _ := e.mem.List(context.Background(), "chunks/")
	blob := e.mem.Raw(keys[0])
	bad := append([]byte(nil), blob...)
	bad[len(bad)/2] ^= 0xff
	e.mem.SetRaw(keys[0], bad)

	v, _ = e.eng.Verify(context.Background(), 100)
	if v.OK() {
		t.Fatal("verify missed a tampered chunk")
	}
	if last, ok := e.eng.LastVerify(); !ok || last.OK() {
		t.Fatal("failed verify wasn't recorded")
	}
}

func TestWrongKeyRejected(t *testing.T) {
	e := newEnv(t)
	other, _ := crypto.NewKey()
	if _, err := repo.Open(context.Background(), e.mem, other); !errors.Is(err, repo.ErrWrongKey) {
		t.Fatalf("Open with wrong key: %v", err)
	}
	if _, err := repo.Open(context.Background(), e.mem, e.key); err != nil {
		t.Fatalf("Open with right key: %v", err)
	}
}

func TestSwappedObjectRejected(t *testing.T) {
	e := newEnv(t)
	e.write("a", []byte("aaaa"))
	e.write("b", []byte("bbbb"))
	res := e.backup(BackupOptions{})
	keys, _ := e.mem.List(context.Background(), "chunks/")
	if len(keys) != 2 {
		t.Fatalf("want 2 chunks, got %d", len(keys))
	}
	// Provider swaps two valid ciphertexts.
	a, b := e.mem.Raw(keys[0]), e.mem.Raw(keys[1])
	e.mem.SetRaw(keys[0], b)
	e.mem.SetRaw(keys[1], a)
	if _, err := e.eng.Restore(context.Background(), res.Snapshot.ID, RestoreOptions{Target: t.TempDir()}); err == nil {
		t.Fatal("restore accepted swapped chunks")
	}
}

func TestManifestRebuild(t *testing.T) {
	e := newEnv(t)
	e.write("a.bin", random(2<<20, 5))
	e.backup(BackupOptions{})

	// New machine: fresh manifest, same repo.
	e.eng.Manifest.Close()
	os.Remove(e.mpath)
	r, err := repo.Open(context.Background(), e.mem, e.key)
	if err != nil {
		t.Fatal(err)
	}
	e.eng = e.open(r)
	if res := e.backup(BackupOptions{}); res.Snapshot.Stats.NewChunks != 0 {
		t.Fatalf("rebuilt manifest still uploaded %d chunks", res.Snapshot.Stats.NewChunks)
	}
}

func TestRestoreInclude(t *testing.T) {
	e := newEnv(t)
	e.write("want/a", []byte("a"))
	e.write("skip/b", []byte("b"))
	res := e.backup(BackupOptions{})
	target := t.TempDir()
	out, err := e.eng.Restore(context.Background(), res.Snapshot.ID, RestoreOptions{
		Target:  target,
		Include: []string{filepath.ToSlash(filepath.Join(e.src, "want"))},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Files != 1 {
		t.Fatalf("restored %d files, want 1", out.Files)
	}
}

// Progress counts files only, so it ends at total even when folders are
// part of the selection.
func TestRestoreProgressReachesTotal(t *testing.T) {
	e := newEnv(t)
	e.write("docs/a", []byte("a"))
	e.write("docs/deep/b", []byte("b"))
	res := e.backup(BackupOptions{})
	var done, total int
	_, err := e.eng.Restore(context.Background(), res.Snapshot.ID, RestoreOptions{
		Target:   t.TempDir(),
		Progress: func(_ string, d, n int) { done, total = d, n },
	})
	if err != nil {
		t.Fatal(err)
	}
	if done != 2 || total != 2 {
		t.Fatalf("progress ended at %d of %d, want 2 of 2", done, total)
	}
}

func TestLocked(t *testing.T) {
	e := newEnv(t)
	if _, err := manifest.Open(e.mpath); !errors.Is(err, manifest.ErrLocked) {
		t.Fatalf("second open: %v", err)
	}
}

func TestUnreadable(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("needs Unix permissions and a non-root user")
	}
	e := newEnv(t)
	e.write("ok.txt", []byte("fine"))
	e.write("locked/secret.txt", []byte("nope"))
	locked := filepath.Join(e.src, "locked")
	os.Chmod(locked, 0)
	t.Cleanup(func() { os.Chmod(locked, 0o755) })

	// An unreadable folder inside a root is skipped and counted.
	res := e.backup(BackupOptions{})
	if len(res.Snapshot.Warnings) != 1 {
		t.Fatalf("warnings = %v", res.Snapshot.Warnings)
	}
	if last, _ := e.eng.LastBackup(); last.Skipped != 1 || last.Error != "" {
		t.Fatalf("last run = %+v", last)
	}

	// An unreadable root fails the whole backup.
	_, err := e.eng.Backup(context.Background(), BackupOptions{Paths: []string{locked}})
	if err == nil {
		t.Fatal("backup of an unreadable root succeeded")
	}
	if last, _ := e.eng.LastBackup(); last.Error == "" {
		t.Fatal("failed backup not recorded")
	}
}
