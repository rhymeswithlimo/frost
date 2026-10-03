package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/rhymeswithlimo/frost/internal/config"
)

func TestLongErrorRemainsReachable(t *testing.T) {
	e, _ := testEngine(t)
	m := newModel(context.Background(), e.Repo, config.Default(), State{})
	m.w, m.h, m.loading = 50, 20, ""
	m.err = errors.New(strings.Repeat("a long storage error explanation ", 50) + "lastdetail")
	if strings.Contains(stripANSI(m.View()), "lastdetail") {
		t.Fatal("error fixture doesn't overflow")
	}
	next, _ := m.key(key("pgdown"))
	m = next.(model)
	if m.err == nil {
		t.Fatal("page down dismissed an overflowing error")
	}
	if !strings.Contains(stripANSI(m.footer()), "scroll") {
		t.Fatal("overflowing error has no scroll hint")
	}
	for range 200 {
		next, _ = m.key(key("down"))
		m = next.(model)
	}
	if view := m.View(); !strings.Contains(stripANSI(view), "lastdetail") || lipgloss.Width(view) != 50 || lipgloss.Height(view) != 20 {
		t.Fatalf("end of error isn't visible in the frame:\n%s", stripANSI(view))
	}
	next, _ = m.key(key("home"))
	m = next.(model)
	if !strings.Contains(stripANSI(m.View()), "Something went wrong") {
		t.Fatal("home didn't return to the start of the error")
	}
	next, _ = m.key(key("end"))
	m = next.(model)
	if !strings.Contains(stripANSI(m.View()), "lastdetail") {
		t.Fatal("end didn't return to the end of the error")
	}
	next, _ = m.Update(tea.WindowSizeMsg{Width: 250, Height: 100})
	m = next.(model)
	if view := stripANSI(m.View()); !strings.Contains(view, "Something went wrong") || !strings.Contains(view, "lastdetail") {
		t.Fatal("resize left the error scrolled past its contents")
	}
	next, _ = m.key(key("esc"))
	if next.(model).err != nil {
		t.Fatal("escape didn't dismiss the error")
	}
}

func TestErrorKeysMatchVisibleDialog(t *testing.T) {
	m := model{w: 50, h: 20, overlay: "help", err: errors.New(strings.Repeat("long error ", 100))}
	next, _ := m.key(key("pgdown"))
	m = next.(model)
	if m.err == nil || m.overlayTop != 0 {
		t.Fatal("key moved the hidden overlay instead of the visible error")
	}
	next, cmd := m.key(key("q"))
	if next.(model).err == nil || cmd == nil {
		t.Fatal("q didn't quit from the error")
	}
	m.overlay = "settings"
	next, _ = m.key(key("v"))
	if next.(model).showKey || next.(model).err != nil {
		t.Fatal("v revealed the key behind the error instead of dismissing it")
	}
	m.overlay, m.err = "", errors.New("short error")
	next, _ = m.key(key("down"))
	if next.(model).err != nil {
		t.Fatal("a key didn't dismiss a short error")
	}
}

func TestShortPathRespectsCellBudget(t *testing.T) {
	for _, path := range []string{rootKey, "/a/long/path/to/notes.txt", "/日本語/日本語.txt"} {
		for width := 0; width <= 12; width++ {
			if got := shortPath(path, width); lipgloss.Width(got) > width {
				t.Fatalf("shortPath(%q, %d) exceeds its width: %q", path, width, got)
			}
		}
	}
}
