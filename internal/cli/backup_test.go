package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/rhymeswithlimo/frost/internal/engine"
	"github.com/rhymeswithlimo/frost/internal/snapshot"
)

func TestBackupSaysWhatChanged(t *testing.T) {
	f := setup(t)
	must(t, f.initAnswers(f.phrase[2], f.phrase[17]), "init")
	must(t, "", "backup")

	// Nothing changed: the dry run says so, and the backup saves nothing
	// and reuses the spot check it just ran.
	if out := must(t, "", "backup", "--dry-run"); !strings.Contains(out, "Nothing has changed since") || !strings.Contains(out, "nothing to back up") {
		t.Fatalf("dry run with nothing changed:\n%s", out)
	}
	out := must(t, "", "backup")
	if !strings.Contains(out, "Already backed up") || !regexp.MustCompile(`verified\s+ok moments ago, \d+ objects checked`).MatchString(out) || strings.Contains(out, "re-downloaded") {
		t.Fatalf("backup with nothing changed:\n%s", out)
	}

	// One file changed and one added.
	os.WriteFile(filepath.Join(f.src, "notes", "todo.txt"), []byte("buy oat milk"), 0o644)
	os.WriteFile(filepath.Join(f.src, "notes", "new.txt"), []byte("hello"), 0o644)
	changes := regexp.MustCompile(`changes\s+1 added, 1 changed`)
	if out := must(t, "", "backup", "--dry-run"); !changes.MatchString(out) || !strings.Contains(out, "Nothing was uploaded") {
		t.Fatalf("dry run with changes:\n%s", out)
	}
	if out := must(t, "", "backup"); !changes.MatchString(out) || !strings.Contains(out, "Saved snapshot") || !strings.Contains(out, "re-downloaded") {
		t.Fatalf("backup with changes:\n%s", out)
	}

	// A deletion needs no new data, but it's still a change worth saving.
	os.Remove(filepath.Join(f.src, "notes", "new.txt"))
	out = must(t, "", "backup")
	if !regexp.MustCompile(`changes\s+1 removed`).MatchString(out) || !regexp.MustCompile(`new data\s+none`).MatchString(out) || !strings.Contains(out, "Saved snapshot") {
		t.Fatalf("backup after a deletion:\n%s", out)
	}
}

// Data uploaded by a run that changed nothing was missing from storage,
// unless it came from a busy file that kept its previous copy.
func TestUnchangedUploadsSayWhy(t *testing.T) {
	for kept, want := range map[int]bool{0: true, 1: false} {
		res := engine.BackupResult{Unchanged: true}
		res.Snapshot.Stats = snapshot.Stats{Files: 2, NewChunks: 1, NewBytes: 100, Kept: kept}
		var out bytes.Buffer
		printBackup(newBlock(&out), res)
		if got := strings.Contains(out.String(), "storage was missing them"); got != want {
			t.Errorf("kept %d: says storage was missing data: %v, want %v\n%s", kept, got, want, out.String())
		}
	}
}
