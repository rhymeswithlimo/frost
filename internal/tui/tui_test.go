package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/rhymeswithlimo/frost/internal/config"
	"github.com/rhymeswithlimo/frost/internal/crypto"
	"github.com/rhymeswithlimo/frost/internal/engine"
	"github.com/rhymeswithlimo/frost/internal/manifest"
	"github.com/rhymeswithlimo/frost/internal/repo"
	"github.com/rhymeswithlimo/frost/internal/snapshot"
	"github.com/rhymeswithlimo/frost/internal/storage/storagetest"
	"github.com/rhymeswithlimo/frost/internal/theme"
)

func TestHelpKeysUseLightTextOnBlue(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	view := (model{w: 120, h: 36}).viewHelp()
	if !strings.Contains(view, theme.Bold.Render("[h]")) {
		t.Fatal("help key is missing the light-on-blue style")
	}
	if strings.Contains(view, theme.Key.Render("[h]")) {
		t.Fatal("help key still uses the inverted button style")
	}
}

func TestHelpSectionsAlign(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	view := (model{w: 120, h: 36}).viewHelp()
	for _, heading := range []string{"Everywhere", "Snapshots", "Moving", "Files"} {
		if !strings.Contains(view, theme.Dim.Render(heading)) {
			t.Fatalf("%s heading is not grey", heading)
		}
	}
	lines := strings.Split(view, "\n")
	for _, pair := range [][2]string{{"Everywhere", "Snapshots"}, {"Moving", "Files"}, {"[↑↓]", "[space]"}} {
		found := false
		for i, line := range lines {
			if strings.Contains(line, pair[0]) && strings.Contains(line, pair[1]) {
				found = true
				if pair[0] != "[↑↓]" {
					first, second := "[h]", "[d]"
					if pair[0] == "Moving" {
						first, second = "[↑↓]", "[space]"
					}
					if i+1 >= len(lines) || !strings.Contains(lines[i+1], first) || !strings.Contains(lines[i+1], second) {
						t.Fatalf("shortcuts do not follow %s and %s", pair[0], pair[1])
					}
				}
				break
			}
		}
		if !found {
			t.Fatalf("%s and %s are not aligned", pair[0], pair[1])
		}
	}
}

func TestFileRowsStayOnOneLine(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	s := snapshot.Snapshot{Paths: []string{"/backup"}}
	files := []snapshot.File{{Path: "/backup", Type: snapshot.TypeDir}}
	for i := range 40 {
		files = append(files, snapshot.File{Path: fmt.Sprintf("/backup/%02d-日本語-long-filename.txt", i), Type: snapshot.TypeFile, Size: 123456789, ModTime: time.Date(2026, 10, 1, 10, 30, 0, 0, time.UTC)})
	}
	for _, width := range []int{40, 50, 62, 80, 120} {
		m := model{w: width, h: 24, tree: newTree(s, &snapshot.Tree{Files: files}), dir: "/backup", sel: map[string]bool{}}
		view := m.viewFiles()
		if lipgloss.Width(view) != m.innerW() || lipgloss.Height(view) != m.areaH() {
			t.Fatalf("%d columns: file rows expanded the panel to %dx%d", width, lipgloss.Width(view), lipgloss.Height(view))
		}
		if !strings.Contains(stripANSI(view), fmt.Sprintf("%02d-", m.bodyH()-3)) {
			t.Fatalf("%d columns: wrapping pushed the last visible entry out of view", width)
		}
	}
}

func TestBackupRootNamesKeepTheirEnd(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	s := snapshot.Snapshot{Paths: []string{"/very/long/backup/location/Documents", "/very/long/backup/location/Pictures"}}
	files := []snapshot.File{{Path: s.Paths[0], Type: snapshot.TypeDir}, {Path: s.Paths[1], Type: snapshot.TypeDir}}
	for _, width := range []int{40, 50, 80} {
		m := model{w: width, h: 24, tree: newTree(s, &snapshot.Tree{Files: files})}
		plain := stripANSI(m.viewFiles())
		for _, name := range []string{"Documents/", "Pictures/"} {
			if !strings.Contains(plain, name) {
				t.Fatalf("%d columns: root lost %q", width, name)
			}
		}
	}
}

func TestHelpAndSettingsRemainReachable(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	e, _ := testEngine(t)
	cfg := config.Default()
	cfg.Paths = []string{"/backup/" + strings.Repeat("long-path/", 12) + "last-folder"}
	cfg.Exclude = append(cfg.Exclude, "last-exclusion")
	m := newModel(context.Background(), e.Repo, cfg, StateFrom(e))
	m.w, m.h, m.loading = 50, 20, ""
	for _, overlay := range []string{"help", "settings"} {
		m.overlay, m.overlayTop = overlay, 0
		if m.overlayMaxTop() == 0 {
			t.Fatalf("%s doesn't account for its overflow", overlay)
		}
		var seen strings.Builder
		for range 200 {
			seen.WriteString(stripANSI(m.View()))
			next, _ := m.key(key("down"))
			m = next.(model)
		}
		for _, want := range map[string][]string{"help": {"Files", "[space]", "[r]", "getfro.st/docs"}, "settings": {"last-folder", "last-exclusion", "key fingerprint", "frost config edit"}}[overlay] {
			if !strings.Contains(seen.String(), want) {
				t.Fatalf("%s: %q can't be reached by scrolling", overlay, want)
			}
		}
		next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 70})
		m = next.(model)
		if m.overlayTop != 0 {
			t.Fatalf("%s didn't clamp scrolling after resize", overlay)
		}
		m.w, m.h = 50, 20
	}
	compact := m
	compact.w, compact.h, compact.overlay, compact.overlayTop = 80, 24, "help", 0
	for _, want := range []string{"Everywhere", "Snapshots", "Moving", "Files", "getfro.st/docs"} {
		if !strings.Contains(stripANSI(compact.View()), want) {
			t.Fatalf("80x24 help clips %q", want)
		}
	}
}

func TestDiffCountsAndFileActionsFitCompactScreens(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	m := model{w: 50, h: 20, screen: scrDiff, diffAdd: 2, diffDel: 3, diffMod: 4}
	for _, want := range []string{"+2 added", "-3 removed", "~4 changed"} {
		if !strings.Contains(stripANSI(m.viewDiff()), want) {
			t.Fatalf("compact diff clips %q", want)
		}
	}
	m.screen = scrFiles
	for _, want := range []string{"[space] select", "[r] restore", "[h] help", "[q] quit"} {
		if !strings.Contains(stripANSI(m.footer()), want) {
			t.Fatalf("compact files footer drops %q", want)
		}
	}
	m.screen = scrSnapshots
	footer := stripANSI(m.footer())
	if strings.Contains(footer, "[enter]") || strings.Contains(footer, "[d]") || !strings.Contains(footer, "[esc] back") {
		t.Fatal("empty snapshots advertise unavailable actions or lose the way back")
	}
}

func TestCompactRestoreKeepsWarningAndLongResultsReachable(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	m := model{w: 50, h: 20, screen: scrRestore, rs: restoreState{snap: snapshot.Snapshot{ID: "sample-snapshot-123456"}, phase: phaseConfirm, dest: destOverwrite, paths: []string{"/backup/notes.txt"}, files: 1}}
	if !strings.Contains(stripANSI(m.viewRestore()), "overwritten.") {
		t.Fatal("compact overwrite confirmation clips the warning")
	}
	lines := strings.Split(stripANSI(m.restoreContent(min(m.innerW()-8, 80))), "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "Files at the original paths") && (i == 0 || strings.TrimSpace(lines[i-1]) != "") {
			t.Fatal("overwrite warning needs a blank line above it")
		}
	}
	m.rs.phase, m.rs.err = phaseDone, fmt.Errorf("%s lastdetail", strings.Repeat("long error explanation ", 40))
	if !strings.Contains(stripANSI(m.footer()), "[pgup pgdn] scroll") {
		t.Fatal("compact restore lost the scroll action")
	}
	next, _ := m.restoreKey("pgdown")
	m = next.(model)
	if m.rs.top == 0 || m.rs.phase != phaseDone {
		t.Fatal("page down didn't scroll the result without changing phase")
	}
	for range 100 {
		next, _ = m.restoreKey("pgdown")
		m = next.(model)
	}
	if !strings.Contains(stripANSI(m.viewRestore()), "lastdetail") {
		t.Fatalf("the end of a long restore error can't be reached:\n%s", stripANSI(m.viewRestore()))
	}
}

func testEngine(t *testing.T) (*engine.Engine, string) {
	t.Helper()
	ctx := context.Background()
	key, _ := crypto.NewKey()
	r, err := repo.Init(ctx, storagetest.NewMem(), key)
	if err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	e := &engine.Engine{Repo: r, Manifest: m}

	src := t.TempDir()
	write := func(rel, data string) {
		p := filepath.Join(src, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(data), 0o644)
	}
	write("Documents/taxes/2025.pdf", strings.Repeat("x", 120000))
	write("Documents/notes.md", "hello")
	write("Pictures/cat.jpg", strings.Repeat("c", 3000000))
	e.Backup(ctx, engine.BackupOptions{Paths: []string{src}})
	write("Documents/notes.md", "hello, changed")
	write("Documents/new.txt", "new file")
	os.Remove(filepath.Join(src, "Documents/taxes/2025.pdf"))
	e.Backup(ctx, engine.BackupOptions{Paths: []string{src}})
	e.Verify(ctx, 5, false)
	return e, src
}

// step feeds a message and runs any command it returns, synchronously, so
// the test sees the resulting state.
func step(t *testing.T, m tea.Model, msg tea.Msg) tea.Model {
	t.Helper()
	m, cmd := m.Update(msg)
	for i := 0; cmd != nil && i < 5; i++ {
		out := cmd()
		if out == nil {
			break
		}
		if b, ok := out.(tea.BatchMsg); ok {
			for _, c := range b {
				if r := c(); r != nil {
					if _, tick := r.(interface{ Tag() int }); !tick {
						m, cmd = m.Update(r)
					}
				}
			}
			continue
		}
		m, cmd = m.Update(out)
	}
	return m
}

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// TestScreens walks every screen at a few sizes and checks each renders
// something sensible. With FROST_TUI_DUMP=dir it also writes each screen's
// ANSI output there for eyeballing.
func TestScreens(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Setenv("FROST_CACHE_DIR", t.TempDir()) // the game's best score lands here
	t.Setenv("FROST_NO_SOUND", "1")          // never open the audio device in tests
	e, _ := testEngine(t)
	dump := os.Getenv("FROST_TUI_DUMP")

	// Down to absurdly small, where things may be cut but must not break.
	st := StateFrom(e)
	st.Version, st.Updates = "v0.1.0", "automatic, v0.2.0 installs after the next backup"
	for _, size := range [][2]int{{120, 36}, {80, 24}, {50, 20}, {250, 70}, {40, 12}, {24, 8}, {10, 4}, {1, 1}} {
		var m tea.Model = newModel(context.Background(), e.Repo, config.Default(), st)
		m = step(t, m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m = step(t, m, m.(model).loadSnaps()())

		shots := map[string]tea.Model{"1-home": m}
		m = step(t, m, key("enter"))
		shots["2-snapshots"] = m
		m = step(t, m, key("enter"))
		shots["3-files-root"] = m
		m = step(t, m, key("enter"))
		m = step(t, m, key(" "))
		shots["4-files-selected"] = m
		shots["5-help"] = step(t, m, key("h"))
		shots["6-settings"] = step(t, m, key("s"))
		r := step(t, m, key("r"))
		shots["7-restore"] = r
		shots["8-restore-new"] = step(t, r, key("2"))
		shots["8-restore-overwrite"] = step(t, r, key("3"))
		shots["8-restore-typing"] = step(t, step(t, r, key("2")), key("enter"))
		picking := r.(model)
		picking.rs.phase = phasePicking
		shots["8-restore-picking"] = picking
		noBeside := r.(model)
		noBeside.rs.besideErr, noBeside.rs.dest = fmt.Errorf("the selection only shares the top of the drive"), destNew
		shots["8-restore-no-beside"] = noBeside
		noOver := r.(model)
		noOver.rs.overErr = fmt.Errorf("/Users/me/Shared is a link owned by another user")
		shots["8-restore-no-overwrite"] = noOver
		neither := noBeside
		neither.rs.overErr = noOver.rs.overErr
		shots["8-restore-neither"] = neither
		picked := r.(model)
		picked.rs.dest, picked.rs.picked, picked.rs.chosen = destNew, "/somewhere/else", "/somewhere/else/frost-restore-x"
		shots["8-restore-picked"] = picked
		ready := picked
		ready.rs.phase, ready.rs.tops = phaseReady, []string{"notes.md", "taxes/", "a/", "b/", "c/"}
		shots["8-restore-ready"] = ready
		failed := r.(model)
		failed.rs.phase = phaseDone
		failed.rs.err = fmt.Errorf("missing chunk")
		failed.rs.res = engine.RestoreResult{Files: 2, Bytes: 100}
		shots["8-restore-failed"] = failed
		running := r.(model)
		running.rs.phase, running.rs.done, running.rs.total = phaseRunning, 2, 5
		running.rs.current = "/backup/Documents/notes.md"
		shots["8-restore-running"] = running
		done := r.(model)
		done.rs.phase, done.rs.folder = phaseDone, "/backup/frost-restore-demo"
		done.rs.res = engine.RestoreResult{Files: 5, Bytes: 120000}
		shots["8-restore-done"] = done
		unfinished := failed
		unfinished.rs.res.Unfinished = true
		shots["8-restore-unfinished"] = unfinished
		kept := shots["1-home"].(model)
		kept.st.HasLast, kept.st.Last = true, engine.LastRun{Time: time.Now().Add(-time.Hour), Skipped: 3, Kept: 2}
		shots["1-home-kept"] = kept
		keptSnap := shots["2-snapshots"].(model)
		keptSnap.snaps = slices.Clone(keptSnap.snaps)
		keptSnap.snaps[0].Stats.Kept = 2
		shots["2-snapshots-kept"] = keptSnap
		m = step(t, m, key("esc"))
		shots["9-diff"] = step(t, m, key("d"))

		g := step(t, m, key("i"))
		shots["10-game-title"] = g
		if g.(model).game == nil {
			t.Fatal("[i] didn't open the game")
		}
		g = step(t, g, key(" "))
		for range 30 {
			g = step(t, g, key("right"))
			g = step(t, g, key(" "))
			if gm := g.(model).game; gm != nil {
				gm.tick(arcadeTickMsg{gen: gm.gen})
			}
		}
		shots["11-game-play"] = g
		g.(model).game.gameOver()
		shots["12-game-over"] = g
		if back := step(t, g, key("esc")); back.(model).game != nil {
			t.Fatal("esc didn't leave the game")
		}

		for name, s := range shots {
			v := s.View()
			if got := lipgloss.Height(v); got != size[1] {
				t.Errorf("%dx%d %s: height %d", size[0], size[1], name, got)
			}
			if got := lipgloss.Width(v); got != size[0] {
				t.Errorf("%dx%d %s: width %d", size[0], size[1], name, got)
			}
			// Width checks can't see a \r: it counts as zero cells, but on a
			// real terminal it moves the cursor and leaves stale cells.
			if i := strings.IndexFunc(v, func(r rune) bool { return r < 0x20 && r != '\n' && r != 0x1b }); i >= 0 {
				t.Errorf("%dx%d %s: control character %q in the frame at byte %d", size[0], size[1], name, v[i], i)
			}
			if footer := lastLine(v); size[0] >= 50 && s.(model).overlay == "" && s.(model).game == nil && s.(model).screen != scrRestore && !strings.Contains(footer, "quit") {
				t.Errorf("%dx%d %s: footer lost [q] quit: %q", size[0], size[1], name, footer)
			}
			if s.(model).err != nil {
				t.Errorf("%s: error screen: %v", name, s.(model).err)
			}
			if dump != "" {
				os.WriteFile(filepath.Join(dump, fmt.Sprintf("%s-%03d.ans", name, size[0])), []byte(v), 0o644)
			}
		}
	}
}

func lastLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(stripANSI(lines[i])) != "" {
			return stripANSI(lines[i])
		}
	}
	return ""
}

func stripANSI(s string) string {
	var b strings.Builder
	esc := false
	for _, r := range s {
		switch {
		case r == 0x1b:
			esc = true
		case esc && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'):
			esc = false
		case !esc:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func TestUnixLines(t *testing.T) {
	if got := unixLines("ab\r\ncd\r\n"); got != "ab\ncd\n" {
		t.Errorf("unixLines = %q", got)
	}
	// Whatever line endings the checkout gave the assets, none survive.
	for name, s := range map[string]string{"wordmark": wordmark, "gameWordmark": gameWordmark} {
		if strings.ContainsRune(s, '\r') {
			t.Errorf("%s still has a carriage return", name)
		}
	}
}

func TestEmptyListCursor(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	e, _ := testEngine(t)
	full, err := e.Repo.Snapshots(context.Background(), nil)
	if err != nil || len(full) == 0 {
		t.Fatal("no snapshots", err)
	}
	var m tea.Model = newModel(context.Background(), e.Repo, config.Default(), State{})
	m = step(t, m, tea.WindowSizeMsg{Width: 120, Height: 36})
	m = step(t, m, snapsMsg{})
	m = step(t, m, key("enter"))
	for _, k := range []string{"down", "G", "pgdown"} {
		m = step(t, m, key(k))
		if c := m.(model).snapCur; c != 0 {
			t.Fatalf("after %s on an empty list the cursor is %d", k, c)
		}
	}
	// A refresh that brings snapshots in must not leave the cursor outside.
	m = step(t, m, snapsMsg{snaps: full})
	m.View()
	m.(model).snapshotsKey("enter")

	// And a refresh that shrinks the list clamps the cursor.
	mm := m.(model)
	mm.snapCur = len(full) + 3
	m = step(t, mm, snapsMsg{snaps: full[:1]})
	if c := m.(model).snapCur; c != 0 {
		t.Fatalf("cursor %d after the list shrank to one", c)
	}
	m.View()
}

func TestDiffScrollStopsAtLastChange(t *testing.T) {
	m := model{w: 80, h: 24, screen: scrDiff}
	for range 50 {
		m.changes = append(m.changes, snapshot.Change{Kind: snapshot.Added})
	}
	for range 100 {
		next, _ := m.key(key("down"))
		m = next.(model)
	}
	listH := m.bodyH() - 2 // the summary and its rule take two rows
	if want := len(m.changes) - listH; m.diffTop != want {
		t.Errorf("diffTop = %d, want %d (last change on the last row)", m.diffTop, want)
	}
}

func TestTruncateCells(t *testing.T) {
	cases := []struct {
		in   string
		w    int
		want string
	}{
		{"short", 10, "short"},
		{"abcdefghij", 6, "abc..."},
		{"日本語のファイル名.txt", 10, "日本語..."},
		{"a\rb\x1b[2Jc", 20, "a?b?[2Jc"},
	}
	for _, c := range cases {
		got := truncate(c.in, c.w)
		if got != c.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", c.in, c.w, got, c.want)
		}
		if lipgloss.Width(got) > c.w {
			t.Errorf("truncate(%q, %d) is %d cells wide", c.in, c.w, lipgloss.Width(got))
		}
	}
	if got := truncateLeft("日本語のファイル名.txt", 10); lipgloss.Width(got) > 10 || !strings.HasSuffix(got, ".txt") {
		t.Errorf("truncateLeft = %q (%d cells)", got, lipgloss.Width(got))
	}
}

func TestShortPathHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	slash := filepath.ToSlash(home)
	if got := shortPath(slash+"/Documents", 80); got != "~/Documents" {
		t.Errorf("snapshot path under home: %q", got)
	}
	if got := shortPath(filepath.Join(home, "x"), 80); got != "~"+string(filepath.Separator)+"x" {
		t.Errorf("local path under home: %q", got)
	}
	// A sibling that merely shares the prefix isn't under home.
	if got := shortPath(slash+"-other/x", 200); strings.HasPrefix(got, "~") {
		t.Errorf("sibling of home shortened: %q", got)
	}
}

// The spinner's tick loop stops once nothing is loading, so an idle browser
// doesn't redraw ten times a second, and starts again with the next load.
func TestSpinnerStopsWhenIdle(t *testing.T) {
	e, _ := testEngine(t)
	m := newModel(context.Background(), e.Repo, config.Default(), StateFrom(e))
	m.w, m.h = 80, 24
	tick := m.spin.Tick()
	if _, cmd := m.Update(tick); cmd == nil {
		t.Fatal("spinner stopped while loading")
	}
	m.loading = ""
	if _, cmd := m.Update(tick); cmd != nil {
		t.Fatal("spinner kept ticking while idle")
	}
}

func TestSelectionCount(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	e, _ := testEngine(t)
	var m tea.Model = newModel(context.Background(), e.Repo, config.Default(), StateFrom(e))
	m = step(t, m, tea.WindowSizeMsg{Width: 120, Height: 36})
	m = step(t, m, m.(model).loadSnaps()())
	m = step(t, m, key("enter"))
	m = step(t, m, key("enter"))
	m = step(t, m, key("a")) // everything in the folder
	mm := m.(model)
	if mm.selFiles == 0 || mm.selBytes == 0 {
		t.Fatalf("selecting a folder counted %d files, %d bytes", mm.selFiles, mm.selBytes)
	}
	if !strings.Contains(stripANSI(m.View()), "files selected") {
		t.Error("the files screen doesn't show the selection")
	}
	m = step(t, m, key("c"))
	if mm := m.(model); mm.selFiles != 0 || mm.selBytes != 0 {
		t.Errorf("clearing left %d files, %d bytes counted", mm.selFiles, mm.selBytes)
	}
}

// File names on Unix can hold control characters. Shown raw, a \r or an
// escape sequence in a name would move the cursor or restyle the screen.
func TestControlCharsInNames(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows file names can't hold control characters")
	}
	lipgloss.SetColorProfile(termenv.TrueColor)
	ctx := context.Background()
	k, _ := crypto.NewKey()
	r, err := repo.Init(ctx, storagetest.NewMem(), k)
	if err != nil {
		t.Fatal(err)
	}
	mf, err := manifest.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mf.Close()
	e := &engine.Engine{Repo: r, Manifest: mf}
	src := t.TempDir()
	for _, name := range []string{"evil\rname\x1b[2J.txt", "日本語のとても長いファイル名です.txt"} {
		if err := os.WriteFile(filepath.Join(src, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.Backup(ctx, engine.BackupOptions{Paths: []string{src}}); err != nil {
		t.Fatal(err)
	}

	var m tea.Model = newModel(ctx, r, config.Default(), StateFrom(e))
	m = step(t, m, tea.WindowSizeMsg{Width: 60, Height: 20})
	m = step(t, m, m.(model).loadSnaps()())
	m = step(t, m, key("enter"))
	m = step(t, m, key("enter"))
	v := m.View()
	if strings.ContainsRune(v, '\r') || strings.Contains(v, "\x1b[2J") {
		t.Fatalf("a file name's control characters reached the screen:\n%q", v)
	}
	if !strings.Contains(stripANSI(v), "evil?name?[2J.txt") {
		t.Errorf("the name isn't shown with its control characters replaced:\n%s", stripANSI(v))
	}
	if got := lipgloss.Width(v); got != 60 {
		t.Errorf("wide names pushed the frame to %d cells", got)
	}
}
