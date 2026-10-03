package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/rhymeswithlimo/frost/internal/config"
	"github.com/rhymeswithlimo/frost/internal/snapshot"
)

func TestNarrowSettingsValuesRemainReachable(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	e, _ := testEngine(t)
	cfg := config.Default()
	cfg.Paths = []string{"/backup/" + strings.Repeat("nested/", 20) + "documents"}
	cfg.Exclude = []string{strings.Repeat("exclude-pattern", 20), "lastpattern"}
	version := "v1.2.3-" + strings.Repeat("long-version-", 10) + "last-version"
	for _, width := range []int{24, 30, 40, 50, 120} {
		m := newModel(context.Background(), e.Repo, cfg, State{Version: version})
		m.w, m.h, m.loading, m.overlay = width, 24, "", "settings"
		var seen strings.Builder
		for top := 0; top <= m.overlayMaxTop(); top++ {
			m.overlayTop = top
			seen.WriteString(stripANSI(m.viewSettings()))
		}
		plain := seen.String()
		for _, want := range []string{"documents", "lastpattern", "last-version", "fingerprint", "frost config edit"} {
			if want == "frost config edit" && width < 30 {
				continue // the command wraps across rows at the narrowest width
			}
			if !strings.Contains(plain, want) {
				t.Fatalf("%d columns: settings lost %q", width, want)
			}
		}
		for _, line := range strings.Split(m.settingsContent(m.overlayW()), "\n") {
			if lipgloss.Width(line) > m.overlayW() {
				t.Fatalf("%d columns: settings row is %d cells wide", width, lipgloss.Width(line))
			}
		}
	}
}

func TestNarrowHelpWrapsWordsAndDocumentation(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	m := model{w: 24, h: 24, overlay: "help"}
	content := m.helpContent(m.overlayW())
	for _, line := range strings.Split(content, "\n") {
		if lipgloss.Width(line) > m.overlayW() {
			t.Fatalf("help row exceeds %d cells: %q", m.overlayW(), stripANSI(line))
		}
	}
	if !strings.Contains(stripANSI(content), "getfro.st/docs") {
		t.Fatal("narrow help loses the documentation address")
	}
	var seen strings.Builder
	for top := 0; top <= m.overlayMaxTop(); top++ {
		m.overlayTop = top
		seen.WriteString(stripANSI(m.viewHelp()))
	}
	if !strings.Contains(seen.String(), "getfro.st/docs") {
		t.Fatal("help's documentation address can't be reached by scrolling")
	}
}

func TestLargeSelectionSummaryAndLastFileStayVisible(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	s := snapshot.Snapshot{Paths: []string{"/backup"}}
	files := []snapshot.File{{Path: "/backup", Type: snapshot.TypeDir}}
	for i := range 100 {
		files = append(files, snapshot.File{Path: fmt.Sprintf("/backup/entry-%03d.txt", i), Type: snapshot.TypeFile, Size: 1})
	}
	for _, width := range []int{40, 50, 80, 120} {
		m := model{w: width, h: 24, screen: scrFiles, tree: newTree(s, &snapshot.Tree{Files: files}), dir: "/backup", selFiles: 123456789, selBytes: 999000000000}
		view := m.viewFiles()
		for _, want := range []string{"123456789", "files selected", "999.0", "GB", "size"} {
			if !strings.Contains(stripANSI(view), want) {
				t.Fatalf("%d columns: selected summary lost %q:\n%s", width, want, stripANSI(view))
			}
		}
		if lipgloss.Width(view) != m.innerW() || lipgloss.Height(view) != m.areaH() {
			t.Fatalf("%d columns: selected summary expanded the panel", width)
		}
		next, _ := m.filesKey("pgdown")
		if got := next.(model).fileCur; got != m.fileListH() {
			t.Fatalf("%d columns: file page moved %d rows, want %d", width, got, m.fileListH())
		}
		next, _ = m.filesKey("G")
		m = next.(model)
		if !strings.Contains(stripANSI(m.viewFiles()), "entry-099") {
			t.Fatalf("%d columns: the last selected-list entry can't be reached", width)
		}
	}
}

func TestLargeDiffCountsKeepLastChangeReachable(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	for _, width := range []int{40, 50, 80, 120} {
		m := model{w: width, h: 24, screen: scrDiff, diffAdd: 1234567, diffDel: 2345678, diffMod: 3456789}
		for i := range 100 {
			m.changes = append(m.changes, snapshot.Change{Kind: snapshot.Added, Path: fmt.Sprintf("/backup/change-%03d.txt", i), New: &snapshot.File{Size: 1}})
		}
		view := m.viewDiff()
		for _, want := range []string{"+1234567 added", "-2345678 removed", "~3456789 changed"} {
			if !strings.Contains(stripANSI(view), want) {
				t.Fatalf("%d columns: diff summary lost %q:\n%s", width, want, stripANSI(view))
			}
		}
		if lipgloss.Width(view) != m.innerW() || lipgloss.Height(view) != m.areaH() {
			t.Fatalf("%d columns: diff summary expanded the panel", width)
		}
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
		if got := next.(model).diffTop; got != m.diffListH() {
			t.Fatalf("%d columns: diff page moved %d rows, want %d", width, got, m.diffListH())
		}
		for range 100 {
			next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
			m = next.(model)
		}
		if m.diffTop != len(m.changes)-m.diffListH() {
			t.Fatalf("%d columns: diff scroll ends at %d, want %d", width, m.diffTop, len(m.changes)-m.diffListH())
		}
		if !strings.Contains(stripANSI(m.viewDiff()), "change-099.txt") {
			t.Fatalf("%d columns: diff's final change can't be reached", width)
		}
		next, _ = m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
		m = next.(model)
		if m.diffTop != len(m.changes)-m.diffListH() {
			t.Fatalf("%d columns: diff resize didn't clamp to the new viewport", width)
		}
		if !strings.Contains(stripANSI(m.viewDiff()), "change-099.txt") {
			t.Fatalf("%d columns: widening the diff hid the final change", width)
		}
	}
}

func TestSnapshotDetailBoundsRootsAndKeepsWarnings(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	s := snapshot.Snapshot{ID: "maple-absurd-3f1c9a0b2e7", Time: time.Now(), Host: strings.Repeat("host", 20), Stats: snapshot.Stats{Files: 1290, Skipped: 3, Kept: 2}}
	for i := range 100 {
		s.Paths = append(s.Paths, fmt.Sprintf("/backup/long-location/%03d-documents", i))
	}
	for _, height := range []int{20, 24, 36} {
		m := model{w: 120, h: height, snaps: []snapshot.Snapshot{s}, marked: "another-snapshot-123456789ab"}
		m.indexSnapshots()
		view := m.viewSnapshots()
		if lipgloss.Width(view) != m.innerW() || lipgloss.Height(view) != m.areaH() {
			t.Fatalf("120x%d: snapshot roots expanded the panel to %dx%d", height, lipgloss.Width(view), lipgloss.Height(view))
		}
		plain := stripANSI(view)
		for _, want := range []string{"+", "3 items couldn't be read", "2 busy files kept", "[d] compares with"} {
			if !strings.Contains(plain, want) {
				t.Fatalf("120x%d: snapshot details lost %q:\n%s", height, want, plain)
			}
		}
		shown := strings.Count(plain, "-documents")
		if shown >= len(s.Paths) || !strings.Contains(plain, fmt.Sprintf("+%d", len(s.Paths)-shown)) {
			t.Fatalf("120x%d: missing roots have no accurate overflow count:\n%s", height, plain)
		}
	}
}

func TestBrowserOverflowResizeMatrix(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	e, _ := testEngine(t)
	cfg := config.Default()
	cfg.Paths = nil
	for i := range 100 {
		cfg.Paths = append(cfg.Paths, "/backup/"+strings.Repeat("日本語-👩‍💻/", 10)+fmt.Sprintf("documents-%03d", i))
	}
	cfg.Exclude = []string{strings.Repeat("日本語👩‍💻", 40), "FINALPATTERN"}
	m := newModel(context.Background(), e.Repo, cfg, State{Version: "v1.2.3-" + strings.Repeat("日本語", 40) + "VERSIONEND"})
	m.loading = ""
	for i := range 100 {
		m.snaps = append(m.snaps, snapshot.Snapshot{
			ID: fmt.Sprintf("maple-absurd-%011x", i<<28), Time: time.Now().Add(-time.Duration(i) * 4 * time.Hour),
			Host: strings.Repeat("日本語-👩‍💻", 20), Paths: cfg.Paths,
			Stats: snapshot.Stats{Files: 123456789, Bytes: 999000000000, Skipped: 3, Kept: 2},
		})
	}
	m.snaps[len(m.snaps)-1].ID = "zeta-tail-fff10000000"
	m.indexSnapshots()
	m.snapCur = len(m.snaps) - 1
	m.marked = m.snaps[0].ID
	fileSnap := snapshot.Snapshot{ID: m.snaps[0].ID, Paths: []string{"/backup"}}
	files := []snapshot.File{{Path: "/backup", Type: snapshot.TypeDir}}
	for i := range 100 {
		files = append(files, snapshot.File{Path: fmt.Sprintf("/backup/entry-%03d-", i) + strings.Repeat("日本語-👩‍💻", 12) + ".txt", Type: snapshot.TypeFile, Size: 1})
		m.changes = append(m.changes, snapshot.Change{Kind: snapshot.Added, Path: "/backup/" + strings.Repeat("日本語-👩‍💻/", 12) + fmt.Sprintf("change-%03d.txt", i), New: &snapshot.File{Size: 1}})
	}
	m.snap, m.tree, m.dir, m.fileCur = fileSnap, newTree(fileSnap, &snapshot.Tree{Files: files}), "/backup", len(files)-2
	m.selFiles, m.selBytes = 123456789, 999000000000
	m.diffAdd, m.diffDel, m.diffMod = 1234567, 2345678, 3456789
	m.diffFrom, m.diffTo = m.snaps[0], m.snaps[1]
	sizes := [][2]int{{50, 20}, {80, 24}, {93, 20}, {94, 20}, {95, 20}, {120, 24}, {1, 1}, {0, 20}, {50, 0}, {0, 0}, {94, 20}, {80, 24}, {50, 20}}
	for _, view := range []string{"home", "snapshots", "files", "diff", "help", "settings", "error"} {
		t.Run(view, func(t *testing.T) {
			mm := m
			switch view {
			case "snapshots":
				mm.screen = scrSnapshots
			case "files":
				mm.screen = scrFiles
			case "diff":
				mm.screen = scrDiff
			case "help", "settings":
				mm.overlay = view
			case "error":
				mm.err = errors.New(strings.Repeat("日本語-👩‍💻 diagnostic explanation ", 100) + "ERROR-END")
			}
			for _, size := range sizes {
				next, _ := mm.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				mm = next.(model)
				checkBrowserFrame(t, mm, view)
				if size[0] < 50 || size[1] < 20 {
					continue
				}
				switch view {
				case "snapshots":
					plain := stripANSI(mm.View())
					if !strings.Contains(plain, "zeta-tail") {
						t.Fatalf("%dx%d: last snapshot isn't visible", mm.w, mm.h)
					}
					if mm.innerW() >= 90 {
						for _, want := range []string{"3 items couldn't be read", "2 busy files kept", "[d] compares with"} {
							if !strings.Contains(plain, want) {
								t.Fatalf("%dx%d: snapshot detail hides %q", mm.w, mm.h, want)
							}
						}
						shown := strings.Count(plain, "documents-")
						if !strings.Contains(plain, fmt.Sprintf("+%d", len(cfg.Paths)-shown)) {
							t.Fatalf("%dx%d: snapshot detail hides its omitted root count", mm.w, mm.h)
						}
					}
				case "files":
					if !strings.Contains(stripANSI(mm.View()), "entry-099") {
						t.Fatalf("%dx%d: last file isn't visible", mm.w, mm.h)
					}
				case "diff":
					mm.diffTop = mm.diffMaxTop()
					checkBrowserFrame(t, mm, view)
					if !strings.Contains(stripANSI(mm.View()), "change-099.txt") {
						t.Fatalf("%dx%d: last diff row isn't visible", mm.w, mm.h)
					}
				case "help", "settings":
					if view == "settings" {
						next, _ = mm.key(tea.KeyMsg{Type: tea.KeyHome})
						mm = next.(model)
						foundRoot, foundPattern := false, false
						for {
							plain := stripANSI(mm.View())
							foundRoot = foundRoot || strings.Contains(plain, "documents-099")
							foundPattern = foundPattern || strings.Contains(plain, "FINALPATTERN")
							if mm.overlayTop == mm.overlayMaxTop() {
								break
							}
							next, _ = mm.key(tea.KeyMsg{Type: tea.KeyPgDown})
							mm = next.(model)
						}
						if !foundRoot || !foundPattern {
							t.Fatalf("%dx%d: settings page navigation hides the final root or exclusion", mm.w, mm.h)
						}
					}
					next, _ = mm.key(tea.KeyMsg{Type: tea.KeyEnd})
					mm = next.(model)
					checkBrowserFrame(t, mm, view)
					want := "getfro.st/docs"
					if view == "settings" {
						want = "VERSIONEND"
					}
					plain := stripANSI(mm.View())
					if view == "settings" {
						plain = browserDialogText(mm.View())
					}
					if !strings.Contains(plain, want) {
						t.Fatalf("%dx%d: %s can't reach %q", mm.w, mm.h, view, want)
					}
				case "error":
					next, _ = mm.key(tea.KeyMsg{Type: tea.KeyEnd})
					mm = next.(model)
					checkBrowserFrame(t, mm, view)
					if mm.err == nil || !strings.Contains(stripANSI(mm.View()), "ERROR-END") {
						t.Fatalf("%dx%d: error end can't be reached", mm.w, mm.h)
					}
				}
			}
		})
	}
}

func browserDialogText(v string) string {
	var text strings.Builder
	for _, line := range strings.Split(ansi.Strip(v), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "│") && strings.HasSuffix(line, "│") {
			line = strings.TrimSuffix(strings.TrimPrefix(line, "│"), "│")
			text.WriteString(strings.Join(strings.Fields(line), ""))
		}
	}
	return text.String()
}

func checkBrowserFrame(t *testing.T, m model, name string) {
	t.Helper()
	v := m.View()
	if m.w <= 0 || m.h <= 0 {
		if v != "" {
			t.Fatalf("%s %dx%d: a zero-sized frame has content", name, m.w, m.h)
		}
		return
	}
	if lipgloss.Width(v) != m.w || lipgloss.Height(v) != m.h {
		t.Fatalf("%s %dx%d: frame is %dx%d", name, m.w, m.h, lipgloss.Width(v), lipgloss.Height(v))
	}
	if strings.Contains(v, "\x1b[2J") || strings.ContainsRune(v, '\r') {
		t.Fatalf("%s %dx%d: unsafe cursor control in frame", name, m.w, m.h)
	}
	assertPaintedBackground(t, fmt.Sprintf("%s-%dx%d", name, m.w, m.h), v)
}
