package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rhymeswithlimo/frost/internal/engine"
	"github.com/rhymeswithlimo/frost/internal/snapshot"
	"github.com/rhymeswithlimo/frost/internal/theme"
)

type restorePhase int

const (
	phaseConfirm restorePhase = iota
	phaseRunning
	phaseDone
)

type restoreState struct {
	snap    snapshot.Snapshot
	paths   []string
	files   int
	bytes   int64
	folder  string // absolute path of the "new folder" option
	inPlace bool
	phase   restorePhase

	ch          chan tea.Msg
	done, total int
	current     string
	res         engine.RestoreResult
	err         error
}

type restoreProgressMsg struct {
	done, total int
	path        string
}

type restoreDoneMsg struct {
	res engine.RestoreResult
	err error
}

func newRestoreState(s snapshot.Snapshot, paths []string, t *tree) restoreState {
	sel := map[string]bool{}
	for _, p := range paths {
		sel[p] = true
	}
	rs := restoreState{snap: s, paths: paths}
	for p, f := range t.files {
		if f.Type == snapshot.TypeFile && covered(p, sel) {
			rs.files++
			rs.bytes += f.Size
		}
	}
	rs.folder, _ = filepath.Abs("frost-restore-" + s.ID)
	return rs
}

func (m model) restoreKey(key string) (tea.Model, tea.Cmd) {
	switch m.rs.phase {
	case phaseConfirm:
		switch key {
		case "esc", "q":
			m.screen = scrFiles
		case "1":
			m.rs.inPlace = false
		case "2":
			m.rs.inPlace = true
		case "up", "down", "tab", "k", "j":
			m.rs.inPlace = !m.rs.inPlace
		case "enter", "y":
			if m.rs.inPlace && key != "y" {
				m.flash = "Restoring in place replaces existing files. Press [y] to confirm."
				return m, nil
			}
			m.rs.phase = phaseRunning
			cmd := m.startRestore()
			return m, cmd
		}
	case phaseRunning:
		// Nothing to do but wait. ctrl+c still quits.
	case phaseDone:
		switch key {
		case "q":
			return m, tea.Quit
		default:
			m.sel = map[string]bool{}
			m.screen = scrFiles
		}
	}
	return m, nil
}

func (m *model) startRestore() tea.Cmd {
	ch := make(chan tea.Msg, 16)
	m.rs.ch = ch
	opts := engine.RestoreOptions{
		Include: m.rs.paths,
		Progress: func(p string, done, total int) {
			select {
			case ch <- restoreProgressMsg{done, total, p}:
			default: // the UI is behind, skip this update
			}
		},
	}
	if !m.rs.inPlace {
		opts.Target = m.rs.folder
	}
	eng, ctx, id := &engine.Engine{Repo: m.repo}, m.ctx, m.rs.snap.ID
	go func() {
		res, err := eng.Restore(ctx, id, opts)
		ch <- restoreDoneMsg{res, err}
		close(ch)
	}()
	return tea.Batch(m.spin.Tick, waitFor(ch))
}

func waitFor(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

func (m model) updateRestore(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case restoreProgressMsg:
		m.rs.done, m.rs.total, m.rs.current = msg.done, msg.total, msg.path
		return m, waitFor(m.rs.ch)
	case restoreDoneMsg:
		m.rs.phase = phaseDone
		m.rs.res, m.rs.err = msg.res, msg.err
	}
	return m, nil
}

func (m model) restoreHints() []string {
	switch m.rs.phase {
	case phaseConfirm:
		if m.rs.inPlace {
			return []string{theme.Hint("y", "restore in place"), theme.Hint("1 2", "choose"), theme.Hint("esc", "cancel")}
		}
		return []string{theme.Hint("enter", "restore"), theme.Hint("1 2", "choose"), theme.Hint("esc", "cancel")}
	case phaseRunning:
		return []string{theme.Hint("ctrl+c", "abort")}
	}
	return []string{theme.Hint("any key", "back to files"), theme.Hint("q", "quit")}
}

func (m model) viewRestore() string {
	rs := m.rs
	w := min(m.innerW()-8, 80) // border and padding take 6
	line := func(s string) string { return pad(truncate2(s, w), w) }
	var lines []string

	switch rs.phase {
	case phaseConfirm:
		lines = append(lines,
			line(theme.Bold.Render(restoreTitle(rs))),
			line(theme.Dim.Render("taken "+rs.snap.Time.Local().Format("2006-01-02 15:04")+", "+ago(rs.snap.Time))),
			fill(w, 1),
		)
		for i, p := range rs.paths {
			if i == 5 {
				lines = append(lines, line(theme.Dim.Render(fmt.Sprintf("  ... and %d more", len(rs.paths)-5))))
				break
			}
			lines = append(lines, line(theme.Text.Render("  "+shortPath(p, w-2))))
		}
		lines = append(lines, fill(w, 1), line(theme.Bold.Render("Where to?")))
		opt := func(n, label, note string, on bool) string {
			mark := theme.Text.Render("( ) ")
			if on {
				mark = theme.Key.Render("(*)") + theme.Base.Render(" ")
			}
			return line(theme.Key.Render("["+n+"]") + theme.Base.Render(" ") + mark + theme.Text.Render(label) + theme.Dim.Render(note))
		}
		lines = append(lines,
			opt("1", "a new folder", "  "+shortPath(rs.folder, w-24), !rs.inPlace),
			opt("2", "original locations", "  replaces what's there now", rs.inPlace),
		)
		if rs.inPlace {
			lines = append(lines, fill(w, 1), line(theme.Caution.Render("Files at the original paths will be overwritten.")))
		}

	case phaseRunning:
		pct := 0
		if rs.total > 0 {
			pct = rs.done * 100 / rs.total
		}
		barW := w - 8
		filled := barW * pct / 100
		bar := theme.Selected.Render(strings.Repeat(" ", filled)) + theme.Faded.Render(strings.Repeat("·", barW-filled))
		lines = append(lines,
			line(theme.Bold.Render(m.spin.View())+theme.Text.Render(" Restoring and checking every chunk...")),
			fill(w, 1),
			line(bar+theme.Text.Render(fmt.Sprintf(" %3d%%", pct))),
			line(theme.Dim.Render(fmt.Sprintf("%d of %d  %s", rs.done, rs.total, shortPath(rs.current, w-20)))),
		)

	case phaseDone:
		if rs.err != nil {
			lines = append(lines, line(theme.Error.Render("Restore failed")), fill(w, 1), wrap(theme.Text.Render(rs.err.Error()), w))
			break
		}
		where := "their original locations"
		if !rs.inPlace {
			where = shortPath(rs.folder, w-10)
		}
		lines = append(lines,
			line(theme.Good.Render("Restored ")+theme.Text.Render(fmt.Sprintf("%d files (%s)", rs.res.Files, humanBytes(rs.res.Bytes)))),
			line(theme.Dim.Render("to ")+theme.Text.Render(where)),
			fill(w, 1),
			line(theme.Dim.Render("Every chunk was decrypted and checked against its hash.")),
		)
	}

	box := theme.Box(true).Padding(1, 2).Render(stack(lines...))
	return m.center(box)
}

func restoreTitle(rs restoreState) string {
	if rs.files == 0 {
		return "Restore empty folders from " + rs.snap.ID
	}
	return fmt.Sprintf("Restore %d files (%s) from %s", rs.files, humanBytes(rs.bytes), rs.snap.ID)
}
