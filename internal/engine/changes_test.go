package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rhymeswithlimo/frost/internal/manifest"
)

func TestUnchangedBackupSavesNoSnapshot(t *testing.T) {
	e := newEnv(t)
	e.write("notes/todo.txt", []byte("buy milk"))
	e.write("photo.jpg", random(300_000, 1))
	first := e.backup(BackupOptions{})
	if first.Unchanged || first.Compared {
		t.Fatalf("first backup compared with nothing: %+v", first)
	}

	// A folder's mtime alone doesn't count: temporary and excluded files
	// change it all the time.
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(e.src, "notes"), later, later); err != nil {
		t.Fatal(err)
	}
	puts := e.mem.Puts
	second := e.backup(BackupOptions{})
	if !second.Unchanged || second.Snapshot.ID != first.Snapshot.ID || !second.Snapshot.Time.Equal(first.Snapshot.Time) || e.mem.Puts != puts {
		t.Fatalf("unchanged backup saved something: %+v, %d puts", second, e.mem.Puts-puts)
	}
	if ids, _ := e.eng.Repo.SnapshotIDs(context.Background()); len(ids) != 1 {
		t.Fatalf("%d snapshots after an unchanged backup, want 1", len(ids))
	}
	if last, _ := e.eng.LastBackup(); !last.Unchanged || last.SnapshotID != first.Snapshot.ID || last.Error != "" {
		t.Fatalf("last run = %+v", last)
	}
	// The ID it hands back restores what was backed up.
	target := t.TempDir()
	if _, err := e.eng.Restore(context.Background(), second.Snapshot.ID, RestoreOptions{Target: target}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(e.restoredRoot(target), "notes", "todo.txt")); string(got) != "buy milk" {
		t.Fatalf("restored %q", got)
	}

	// A file's mtime does count, even with the same contents.
	if err := os.Chtimes(filepath.Join(e.src, "photo.jpg"), later, later); err != nil {
		t.Fatal(err)
	}
	third := e.backup(BackupOptions{})
	if third.Unchanged || third.Snapshot.ID == first.Snapshot.ID || third.Snapshot.Stats.NewChunks != 0 {
		t.Fatalf("touched file: %+v", third)
	}
	if want := (Changes{Files: ChangeCount{Changed: 1}}); third.Changes != want {
		t.Fatalf("changes = %+v, want %+v", third.Changes, want)
	}
}

func TestBackupCountsChanges(t *testing.T) {
	e := newEnv(t)
	e.write("keep.txt", []byte("same"))
	e.write("edit.txt", []byte("before"))
	e.write("gone.txt", []byte("bye"))
	os.Mkdir(filepath.Join(e.src, "old"), 0o755)
	e.backup(BackupOptions{})

	e.write("edit.txt", []byte("after, and longer"))
	e.write("new.txt", []byte("hello"))
	os.Remove(filepath.Join(e.src, "gone.txt"))
	os.Remove(filepath.Join(e.src, "old"))
	os.Mkdir(filepath.Join(e.src, "fresh"), 0o755)

	// A dry run counts the same and saves nothing.
	puts := e.mem.Puts
	before, _ := e.eng.LastBackup()
	dry := e.backup(BackupOptions{DryRun: true})
	want := Changes{Files: ChangeCount{Added: 1, Changed: 1, Removed: 1}, Folders: ChangeCount{Added: 1, Removed: 1}}
	if !dry.Compared || dry.Unchanged || dry.Changes != want {
		t.Fatalf("dry run: compared %v, unchanged %v, changes %+v, want %+v", dry.Compared, dry.Unchanged, dry.Changes, want)
	}
	if after, _ := e.eng.LastBackup(); e.mem.Puts != puts || !after.Time.Equal(before.Time) {
		t.Fatal("dry run saved something")
	}

	res := e.backup(BackupOptions{})
	if res.Unchanged || res.Changes != want {
		t.Fatalf("backup: unchanged %v, changes %+v, want %+v", res.Unchanged, res.Changes, want)
	}

	// Right after, a dry run finds nothing to do.
	if dry := e.backup(BackupOptions{DryRun: true}); !dry.Unchanged || dry.Snapshot.ID != res.Snapshot.ID || !dry.Changes.None() {
		t.Fatalf("dry run after the backup: %+v", dry)
	}
}

// Anything that leaves the last snapshot in doubt saves a new one.
func TestUnchangedOnlyWhenSure(t *testing.T) {
	ctx := context.Background()

	t.Run("newer snapshot of other folders", func(t *testing.T) {
		// Otherwise `latest` would mean the --path run, not the backup.
		e := newEnv(t)
		e.write("a/one.txt", []byte("1"))
		e.write("b/two.txt", []byte("2"))
		e.backup(BackupOptions{})
		e.backup(BackupOptions{Paths: []string{filepath.Join(e.src, "a")}})
		if res := e.backup(BackupOptions{}); res.Unchanged {
			t.Fatal("skipped a snapshot while a newer one of other folders exists")
		}
	})

	t.Run("newer snapshot from another machine", func(t *testing.T) {
		e := newEnv(t)
		e.write("a.txt", []byte("a"))
		e.backup(BackupOptions{})
		m, err := manifest.Open(filepath.Join(t.TempDir(), "other.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer m.Close()
		other := &Engine{Repo: e.eng.Repo, Manifest: m}
		if _, err := other.Backup(ctx, BackupOptions{Paths: []string{t.TempDir()}}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := e.eng.RefreshSnapshots(ctx); err != nil {
			t.Fatal(err)
		}
		if res := e.backup(BackupOptions{}); res.Unchanged {
			t.Fatal("skipped a snapshot while another machine's is newer")
		}
	})

	t.Run("header gone from storage", func(t *testing.T) {
		e := newEnv(t)
		e.write("a.txt", []byte("a"))
		first := e.backup(BackupOptions{})
		if err := e.mem.Delete(ctx, "snapshots/"+first.Snapshot.ID); err != nil {
			t.Fatal(err)
		}
		if res := e.backup(BackupOptions{}); res.Unchanged {
			t.Fatal("skipped a snapshot whose header is gone")
		}
	})

	t.Run("fresh manifest", func(t *testing.T) {
		e := newEnv(t)
		e.write("a.txt", []byte("a"))
		e.backup(BackupOptions{})
		e.eng.Manifest.Close()
		os.Remove(e.mpath)
		e.eng = e.open(e.eng.Repo)
		if res := e.backup(BackupOptions{}); res.Unchanged || res.Compared {
			t.Fatal("a new manifest compared with a snapshot it never saved")
		}
	})
}

func TestVerifyDue(t *testing.T) {
	e := newEnv(t)
	e.write("a.txt", []byte("a"))
	e.backup(BackupOptions{})
	if !e.eng.VerifyDue() {
		t.Fatal("never checked, but not due")
	}
	if _, err := e.eng.Verify(context.Background(), 5, false); err != nil {
		t.Fatal(err)
	}
	if e.eng.VerifyDue() {
		t.Fatal("just checked, but due")
	}
	for name, v := range map[string]VerifyResult{
		"a day old":      {Time: time.Now().Add(-25 * time.Hour), Checked: 5},
		"found problems": {Time: time.Now(), Checked: 5, Failures: []string{"chunk missing"}},
	} {
		e.eng.Manifest.PutMeta(metaVerify, v)
		if !e.eng.VerifyDue() {
			t.Errorf("last check %s, but not due", name)
		}
	}
}
