package cli

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/rhymeswithlimo/frost/internal/snapshot"
)

func TestPrintSnapshots(t *testing.T) {
	for _, tc := range []struct {
		count    int
		all      bool
		shown    int
		overflow string
	}{
		{count: 0},
		{count: 1, shown: 1},
		{count: 9, shown: 9},
		{count: 10, shown: 10},
		{count: 11, shown: 10, overflow: "+1"},
		{count: 13, shown: 10, overflow: "+3"},
		{count: 15, shown: 10, overflow: "+5"},
		{count: 20, shown: 10, overflow: "+10"},
		{count: 100, shown: 10, overflow: "+90"},
		{count: 191, shown: 10, overflow: "+181"},
		{count: 1300, shown: 10, overflow: "+1290"},
		{count: 0, all: true},
		{count: 10, all: true, shown: 10},
		{count: 13, all: true, shown: 13},
		{count: 100, all: true, shown: 100},
	} {
		t.Run(fmt.Sprintf("%d/all=%t", tc.count, tc.all), func(t *testing.T) {
			snaps := make([]snapshot.Snapshot, tc.count)
			for i := range snaps {
				snaps[i] = snapshot.Snapshot{ID: fmt.Sprintf("test%04d", i)}
			}
			var out bytes.Buffer
			b := newBlock(&out)
			b.open("frost", "dev")
			printSnapshots(b, snaps, snapshot.Shorten(snaps), tc.all)
			text := ansi.Strip(out.String())
			rows := snapshotRows(text)
			want := make([]string, tc.shown)
			for i := range want {
				want[i] = snaps[i].ID
			}
			if !slices.Equal(rows, want) {
				t.Fatalf("snapshot rows = %v, want %v", rows, want)
			}
			if tc.overflow == "" {
				if strings.Contains(text, "\n│  +") || strings.Contains(text, "frost status --all") {
					t.Fatalf("unneeded overflow or hint:\n%s", text)
				}
			} else {
				lines := strings.Split(text, "\n")
				index := slices.Index(lines, "│  "+tc.overflow)
				if index < 1 || !strings.HasPrefix(lines[index-1], "│  "+want[len(want)-1]+" ") {
					t.Fatalf("%s isn't directly below the last snapshot:\n%s", tc.overflow, text)
				}
				if strings.Count(text, "\n│  +") != 1 || strings.Contains(text, "older") || !strings.Contains(text, "└  See all snapshots with frost status --all") {
					t.Fatalf("overflow or hint:\n%s", text)
				}
			}
			if tc.count == 0 && !strings.Contains(text, "No snapshots yet. Run frost backup.") {
				t.Fatalf("missing empty-state hint:\n%s", text)
			}
			if blockOpen {
				t.Fatal("snapshot list didn't close the block")
			}
		})
	}
}

func TestStatusRecentSnapshots(t *testing.T) {
	f := setup(t)
	must(t, f.initAnswers(f.phrase[2], f.phrase[17]), "init")
	ctx := context.Background()
	a, err := openApp(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	snaps := make([]snapshot.Snapshot, 13)
	for i := range snaps {
		snaps[i] = snapshot.Snapshot{
			ID:    fmt.Sprintf("test-snapshot-%04x0000000", i),
			Time:  time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC).Add(-time.Duration(i) * time.Hour),
			Stats: snapshot.Stats{Files: 1, Bytes: 10},
		}
	}
	// A hidden snapshot shares the newest snapshot's short ID.
	snaps[0].ID = "maple-absurd-3f1c9a0b2e7"
	snaps[12].ID = "maple-absurd-3f1c0000000"
	// Save out of time order so status has to pick the newest snapshots.
	for i := range snaps {
		if _, err := a.engine.Repo.SaveSnapshot(ctx, snaps[(i*5)%len(snaps)], &snapshot.Tree{}, nil); err != nil {
			t.Fatal(err)
		}
	}
	a.Close()
	short := snapshot.Shorten(snaps)
	for _, tc := range []struct {
		name  string
		flags []string
		shown int
	}{
		{name: "default", shown: 10},
		{name: "all", flags: []string{"--all"}, shown: 13},
		{name: "a", flags: []string{"-a"}, shown: 13},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := ansi.Strip(must(t, "", append([]string{"status"}, tc.flags...)...))
			want := make([]string, tc.shown)
			for i := range want {
				want[i] = short.Of(snaps[i].ID)
			}
			if got := snapshotRows(out); !slices.Equal(got, want) {
				t.Fatalf("snapshot rows = %v, want %v\n%s", got, want, out)
			}
			if !strings.Contains(out, "in 13 snapshots") {
				t.Fatalf("protected count lost hidden snapshots:\n%s", out)
			}
			if got := strings.Contains(out, "\n│  +3\n"); got != (tc.shown == 10) {
				t.Fatalf("overflow shown = %t:\n%s", got, out)
			}
		})
	}
}

func snapshotRows(out string) []string {
	var rows []string
	listing := false
	for _, line := range strings.Split(out, "\n") {
		if line == "├  snapshots" {
			listing = true
			continue
		}
		fields := strings.Fields(line)
		if listing && len(fields) > 1 && fields[0] == "│" && fields[1] != "snapshot" && snapshot.ValidID(fields[1]) {
			rows = append(rows, fields[1])
		}
	}
	return rows
}
