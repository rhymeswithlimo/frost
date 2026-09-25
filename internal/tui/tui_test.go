package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/rhymeswithlimo/frost/internal/config"
	"github.com/rhymeswithlimo/frost/internal/crypto"
	"github.com/rhymeswithlimo/frost/internal/engine"
	"github.com/rhymeswithlimo/frost/internal/manifest"
	"github.com/rhymeswithlimo/frost/internal/repo"
	"github.com/rhymeswithlimo/frost/internal/storage/storagetest"
)

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
	e.Verify(ctx, 5)
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

	for _, size := range [][2]int{{120, 36}, {80, 24}, {50, 20}} {
		var m tea.Model = newModel(context.Background(), e.Repo, config.Default(), StateFrom(e))
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
		shots["8-restore-inplace"] = step(t, r, key("2"))
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
				gm.tick(arcadeTickMsg{gm.gen})
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
			if footer := lastLine(v); s.(model).overlay == "" && s.(model).game == nil && s.(model).screen != scrRestore && !strings.Contains(footer, "quit") {
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

func itoa(n int) string {
	return strings.TrimSpace(strings.Repeat(" ", 0) + string(rune('0'+n/100)) + string(rune('0'+n/10%10)) + string(rune('0'+n%10)))
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
