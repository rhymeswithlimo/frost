package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// keepBusy makes every read of the files named in busy look like it was
// interrupted by a write, by moving their mtime after each chunk.
func keepBusy(t *testing.T, busy func(path string) bool) {
	t.Helper()
	var mu sync.Mutex
	bump := time.Now()
	chunkRead = func(p string) {
		if !busy(p) {
			return
		}
		mu.Lock()
		bump = bump.Add(time.Second)
		at := bump
		mu.Unlock()
		os.Chtimes(p, at, at)
	}
	t.Cleanup(func() { chunkRead = nil })
}

func TestBusyFileKeepsPreviousCopy(t *testing.T) {
	e := newEnv(t)
	e.write("db.sqlite", []byte("clean copy"))
	e.write("other.txt", []byte("fine"))
	first := e.backup(BackupOptions{})
	p := filepath.Join(e.src, "db.sqlite")
	before, _ := os.Stat(p)

	e.write("db.sqlite", []byte("half written"))
	keepBusy(t, func(path string) bool { return path == p })
	res := e.backup(BackupOptions{})
	s := res.Snapshot
	if s.Stats.Kept != 1 || len(s.Kept) != 1 || s.Kept[0] != filepath.ToSlash(p) || s.Stats.Skipped != 0 || s.Stats.Files != 2 {
		t.Fatalf("busy file: %+v", s)
	}
	if last, _ := e.eng.LastBackup(); last.Kept != 1 || last.Skipped != 0 {
		t.Fatalf("last run = %+v", last)
	}

	target := t.TempDir()
	if _, err := e.eng.Restore(context.Background(), s.ID, RestoreOptions{Target: target}); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(e.restoredRoot(target), "db.sqlite")
	if got, _ := os.ReadFile(out); string(got) != "clean copy" {
		t.Fatalf("restored %q, want the previous copy", got)
	}
	if info, _ := os.Stat(out); !info.ModTime().Equal(before.ModTime()) {
		t.Fatal("previous copy restored with the wrong mtime")
	}

	// Once it settles, the next backup reads it again. (Its data from the
	// interrupted reads was already uploaded, so it isn't sent twice.)
	chunkRead = nil
	third := e.backup(BackupOptions{})
	if third.Snapshot.Stats.Kept != 0 {
		t.Fatalf("settled file still kept its old copy: %+v", third.Snapshot)
	}
	target = t.TempDir()
	if _, err := e.eng.Restore(context.Background(), third.Snapshot.ID, RestoreOptions{Target: target}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(e.restoredRoot(target), "db.sqlite")); string(got) != "half written" {
		t.Fatalf("settled file restored as %q", got)
	}
	if first.Snapshot.ID == third.Snapshot.ID {
		t.Fatal("snapshots share an ID")
	}
}

func TestBusyFileWithoutCopyIsLeftOut(t *testing.T) {
	e := newEnv(t)
	e.write("download.part", []byte("still coming"))
	e.write("other.txt", []byte("fine"))
	p := filepath.Join(e.src, "download.part")
	keepBusy(t, func(path string) bool { return path == p })
	s := e.backup(BackupOptions{}).Snapshot
	if s.Stats.Files != 1 || s.Stats.Skipped != 1 || s.Stats.Kept != 0 || !strings.Contains(s.Warnings[0], "no earlier copy") {
		t.Fatalf("busy file with no copy: %+v", s)
	}
}

func TestHeaderListsAreCapped(t *testing.T) {
	e := newEnv(t)
	for i := range 2 * maxListed {
		e.write(fmt.Sprintf("f%03d", i), []byte("x"))
	}
	keepBusy(t, func(string) bool { return true })
	s := e.backup(BackupOptions{}).Snapshot
	if s.Stats.Skipped != 2*maxListed || len(s.Warnings) != maxListed {
		t.Fatalf("%d warnings stored, %d counted", len(s.Warnings), s.Stats.Skipped)
	}
}

func TestBackupSkipsRestorePartials(t *testing.T) {
	e := newEnv(t)
	e.write("notes.txt", []byte("kept"))
	e.write(partialName("snap", "/notes.txt"), []byte("left by a stopped restore"))
	e.write(".frost-partial-notes", []byte("just a file with a similar name"))
	if !isPartial(partialName("x", "/y")) || isPartial(".frost-partial-notes") || isPartial(".frost-partial-ABCDEF0123456789") {
		t.Fatal("isPartial matches the wrong names")
	}
	if n := e.backup(BackupOptions{}).Snapshot.Stats.Files; n != 2 {
		t.Fatalf("backed up %d files, want 2", n)
	}
}
