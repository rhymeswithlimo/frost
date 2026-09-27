package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/rhymeswithlimo/frost/internal/engine"
	"github.com/rhymeswithlimo/frost/internal/snapshot"
)

func TestUnusedRestoreFolder(t *testing.T) {
	base := filepath.Join(t.TempDir(), "frost-restore-snapshot")
	if err := os.Mkdir(base, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base+"-1", []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := unusedRestoreFolder(base)
	if err != nil || got != base+"-2" {
		t.Fatalf("got %q, %v", got, err)
	}
	m := model{rs: restoreState{folder: base}}
	next, cmd := m.restoreKey("enter")
	r := next.(model)
	if cmd != nil || r.rs.phase != phaseConfirm || r.rs.folder != got || r.flash == "" {
		t.Fatal("occupied destination wasn't changed and presented for confirmation")
	}
	data, err := os.ReadFile(base + "-1")
	if err != nil || string(data) != "keep" {
		t.Fatal("existing file changed")
	}
}

func TestRestoreFailureDetails(t *testing.T) {
	m := model{w: 120, h: 36, rs: restoreState{
		phase: phaseDone, folder: "restore-output", err: errors.New("missing chunk"),
		res: engine.RestoreResult{Files: 2, Bytes: 100},
	}}
	view := m.viewRestore()
	for _, want := range []string{"2 files completed", "restore-output", "Earlier changes remain", "missing chunk"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q", want)
		}
	}
}

func TestLongSnapshotListLabel(t *testing.T) {
	s := snapshot.Snapshot{ID: "ability-original-0123456789abcdef"}
	s.Stats.Files = 52
	s.Stats.Bytes = 1024
	for _, width := range []int{12, 20, 40, 50, 68, 100} {
		label := snapshotListLabel(s, "  ", width)
		if lipgloss.Width(label) != width || !strings.HasSuffix(label, "  ") {
			t.Fatalf("width %d: missing right padding in %q", width, label)
		}
		if width >= 40 && (!strings.Contains(label, "52 files") || !strings.Contains(label, humanBytes(s.Stats.Bytes))) {
			t.Fatalf("width %d: %q", width, label)
		}
	}
}
