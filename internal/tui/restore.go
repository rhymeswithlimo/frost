package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/rhymeswithlimo/frost/internal/config"
	"github.com/rhymeswithlimo/frost/internal/desktop"
	"github.com/rhymeswithlimo/frost/internal/engine"
	"github.com/rhymeswithlimo/frost/internal/snapshot"
	"github.com/rhymeswithlimo/frost/internal/theme"
)

type restorePhase int

const (
	phaseConfirm restorePhase = iota
	phasePicking              // the system's folder picker is open
	phaseTyping               // typing a folder, when there's no picker
	phaseReady                // a new location is chosen: restore, change it or cancel
	phaseRunning
	phaseDone
)

// restoreDest is where a restore goes: the three choices on the confirm
// screen, in order.
type restoreDest int

const (
	destBeside    restoreDest = iota // a new folder next to the originals
	destNew                          // a new folder inside one you choose
	destOverwrite                    // over the originals
)

type restoreState struct {
	snap  snapshot.Snapshot
	paths []string
	files int
	bytes int64
	dest  restoreDest
	phase restorePhase

	// Options 1 and 2 restore into a new folder, with paths relative to
	// base (see snapshot.RestoreBase).
	base      string
	beside    string   // option 1's folder
	besideErr error    // why there's no option 1
	overErr   error    // why there's no option 3
	picked    string   // the folder chosen for option 2
	chosen    string   // option 2's new folder inside it
	tops      []string // what lands at the top of the new folder, folders ending in /
	folder    string   // the new folder the restore went to

	pickSeq    int // tells a stale picker's answer from the current one
	pickCancel context.CancelFunc
	input      form
	inputErr   string

	// What to open when the restore is done: the deepest folder holding
	// everything selected, or the one selected file.
	show     string
	showFile bool

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

type folderOpenedMsg struct{ err error }

type folderPickedMsg struct {
	seq  int
	path string
	err  error
}

// openFolder shows a restored file or folder in the file manager, when
// canOpen says there's a desktop. pickFolder shows the folder picker, when
// canPick says there is one. They're variables so tests never open a real
// window.
var (
	canOpen    = desktop.Available
	canPick    = desktop.CanPick
	pickFolder = desktop.PickFolder
)

var openFolder = func(path string, file bool) error {
	if file {
		return desktop.Reveal(path)
	}
	return desktop.Open(path)
}

func newRestoreState(s snapshot.Snapshot, paths []string, t *tree) restoreState {
	sel := map[string]bool{}
	for _, p := range paths {
		sel[p] = true
	}
	rs := restoreState{snap: s, paths: paths, show: snapshot.CommonDir(paths), base: snapshot.RestoreBase(paths)}
	if len(paths) == 1 {
		f := t.files[paths[0]]
		rs.showFile = f != nil && f.Type != snapshot.TypeDir
	}
	for p, f := range t.files {
		if f.Type == snapshot.TypeFile && covered(p, sel) {
			rs.files++
			rs.bytes += f.Size
		}
	}
	seen := map[string]bool{}
	for _, p := range paths {
		rel, err := snapshot.RestoreRel(p, rs.base)
		if err != nil {
			continue
		}
		top, _, deeper := strings.Cut(rel, "/")
		if f := t.files[p]; deeper || (f != nil && f.Type == snapshot.TypeDir) {
			top += "/"
		}
		if !seen[top] {
			seen[top] = true
			rs.tops = append(rs.tops, top)
		}
	}
	slices.Sort(rs.tops)
	rs.beside, rs.besideErr = engine.BesideFolder(rs.base, s.ID)
	rs.overErr = engine.CanOverwrite(paths)
	if rs.besideErr != nil {
		rs.dest = destNew // never overwrite by default
	}
	return rs
}

// showPath is where the restored selection ended up on disk, and whether
// it's a single file. It's "" when there's nothing useful to open.
func (rs restoreState) showPath() (string, bool) {
	if rs.dest == destOverwrite {
		// A filesystem root or a whole drive is noise, not what was restored.
		if rs.show == "" || snapshot.IsRoot(rs.show) {
			return "", false
		}
		return filepath.FromSlash(rs.show), rs.showFile
	}
	rel, err := snapshot.RestoreRel(rs.show, rs.base)
	if err != nil {
		return rs.folder, false // everything restored is right in the folder
	}
	return filepath.Join(rs.folder, filepath.FromSlash(rel)), rs.showFile
}

// unavailable says why an option can't be used, or returns nil.
func (rs restoreState) unavailable(d restoreDest) error {
	switch d {
	case destBeside:
		return rs.besideErr
	case destOverwrite:
		return rs.overErr
	}
	return nil
}

// chooseDest picks one of the three options, if it's available.
func (m *model) chooseDest(d restoreDest) {
	if err := m.rs.unavailable(d); err != nil {
		m.flash = fmt.Sprintf("Option %d isn't available: %s.", d+1, printable(err.Error()))
		return
	}
	m.rs.dest = d
}

// moveDest moves the choice up or down, skipping options that aren't
// available. Option 2 always is.
func (m *model) moveDest(by int) {
	for d := (m.rs.dest + 3 + restoreDest(by)) % 3; ; d = (d + 3 + restoreDest(by)) % 3 {
		if m.rs.unavailable(d) == nil {
			m.rs.dest = d
			return
		}
	}
}

func (m model) restoreKey(key string) (tea.Model, tea.Cmd) {
	switch m.rs.phase {
	case phaseConfirm:
		switch key {
		case "esc", "q":
			m.screen = scrFiles
		case "1":
			m.chooseDest(destBeside)
		case "2":
			m.chooseDest(destNew)
		case "3":
			m.chooseDest(destOverwrite)
		case "up", "k", "shift+tab":
			m.moveDest(-1)
		case "down", "j", "tab":
			m.moveDest(1)
		case "c":
			if m.rs.dest == destNew {
				return m.startPick()
			}
		case "enter", "y":
			if m.rs.dest == destNew {
				// Choose the folder, or review the one already chosen.
				if m.rs.picked == "" {
					return m.startPick()
				}
				m.rs.phase = phaseReady
				return m, nil
			}
			return m.confirmRestore(key)
		}
	case phaseReady:
		switch key {
		case "enter", "y":
			return m.confirmRestore(key)
		case "c":
			return m.startPick()
		case "esc", "q":
			m.rs.phase = phaseConfirm
			m.screen = scrFiles
		}
	case phasePicking:
		switch key {
		case "esc", "q":
			m.stopPick()
			m.rs.phase = m.rs.afterPick()
		case "t":
			m.stopPick()
			m.startTyping()
		}
	case phaseRunning:
		// Nothing to do but wait. ctrl+c still quits.
	case phaseDone:
		switch key {
		case "q":
			return m, tea.Quit
		default:
			m.sel, m.selFiles, m.selBytes = map[string]bool{}, 0, 0
			m.screen = scrFiles
		}
	}
	return m, nil
}

// confirmRestore starts the restore, once the chosen destination is settled.
func (m model) confirmRestore(key string) (tea.Model, tea.Cmd) {
	switch m.rs.dest {
	case destOverwrite:
		if key != "y" {
			m.flash = "Overwriting replaces existing files. Press [y] to confirm."
			return m, nil
		}
		m.rs.folder = ""
	case destBeside, destNew:
		current, parent := m.rs.beside, filepath.Dir(m.rs.beside)
		if m.rs.dest == destNew {
			current, parent = m.rs.chosen, m.rs.picked
		}
		// Something may have taken the name since it was shown.
		folder, err := engine.NewRestoreFolder(parent, m.rs.snap.ID)
		if err != nil {
			m.rs.err, m.rs.phase = err, phaseDone
			return m, nil
		}
		if folder != current {
			if m.rs.dest == destNew {
				m.rs.chosen = folder
			} else {
				m.rs.beside = folder
			}
			m.flash = "That folder now exists. Review the new destination and press [enter]."
			return m, nil
		}
		m.rs.folder = folder
	}
	m.rs.phase = phaseRunning
	cmd := m.startRestore()
	return m, cmd
}

// startPick opens the folder picker, or asks for a folder to be typed when
// there's no picker.
func (m model) startPick() (tea.Model, tea.Cmd) {
	if !canPick() {
		m.startTyping()
		m.flash = "There's no folder picker here, so type the folder instead."
		return m, nil
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.rs.pickSeq++
	m.rs.pickCancel = cancel
	m.rs.phase = phasePicking
	seq, start := m.rs.pickSeq, m.pickStart()
	return m, func() tea.Msg {
		p, err := pickFolder(ctx, "Restore to", start)
		return folderPickedMsg{seq, p, err}
	}
}

// pickStart is where the picker opens: the last folder chosen, or beside
// the originals, or home.
func (m model) pickStart() string {
	if m.rs.picked != "" {
		return m.rs.picked
	}
	if dir := filepath.FromSlash(m.rs.base); m.rs.base != "" && filepath.IsAbs(dir) {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	home, _ := os.UserHomeDir()
	return home
}

func (m *model) stopPick() {
	if m.rs.pickCancel != nil {
		m.rs.pickCancel()
		m.rs.pickCancel = nil
	}
}

func (m *model) startTyping() {
	m.rs.phase = phaseTyping
	m.rs.inputErr = ""
	value := m.rs.picked
	if value != "" {
		value = shortPath(value, 1000)
	}
	m.rs.input = form{fields: []field{{name: "folder", placeholder: "~/Desktop", value: value}}}
}

// restoreTypingKey edits the typed folder. It gets every key, so typing a
// [v] or [q] doesn't reach the rest of the browser.
func (m model) restoreTypingKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.Type == tea.KeyEsc {
		m.rs.phase = m.rs.afterPick()
		return m, nil
	}
	m.rs.inputErr = ""
	if !m.rs.input.key(k) {
		return m, nil
	}
	v := strings.TrimSpace(m.rs.input.fields[0].value)
	if v == "" {
		m.rs.inputErr = "Type the folder to restore into."
		return m, nil
	}
	if err := m.rs.setPicked(config.Expand(v)); err != nil {
		m.rs.inputErr = err.Error()
		return m, nil
	}
	m.rs.phase = phaseReady
	return m, nil
}

// afterPick is where choosing a folder gives up to: the review screen if a
// folder was chosen before, the options if not.
func (rs restoreState) afterPick() restorePhase {
	if rs.picked != "" {
		return phaseReady
	}
	return phaseConfirm
}

// setPicked makes dir the folder for option 2.
func (rs *restoreState) setPicked(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		return fmt.Errorf("There's no folder at %s.", shortPath(abs, 60))
	}
	chosen, err := engine.NewRestoreFolder(abs, rs.snap.ID)
	if err != nil {
		return err
	}
	rs.picked, rs.chosen, rs.dest = abs, chosen, destNew
	return nil
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
	if m.rs.dest != destOverwrite {
		opts.Target, opts.NewTarget, opts.Base = m.rs.folder, true, m.rs.base
	}
	eng, ctx, id := &engine.Engine{Repo: m.repo}, m.ctx, m.rs.snap.ID
	go func() {
		res, err := eng.Restore(ctx, id, opts)
		select {
		case ch <- restoreDoneMsg{res, err}:
		case <-ctx.Done():
		}
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
		if msg.err != nil || !canOpen() {
			return m, nil
		}
		if p, file := m.rs.showPath(); p != "" {
			return m, func() tea.Msg { return folderOpenedMsg{openFolder(p, file)} }
		}
	case folderOpenedMsg:
		if msg.err != nil {
			m.flash = "Couldn't open the restored files: " + printable(msg.err.Error())
		}
	case folderPickedMsg:
		if m.rs.phase != phasePicking || msg.seq != m.rs.pickSeq {
			return m, nil // a picker that was already given up on
		}
		m.stopPick()
		m.rs.phase = m.rs.afterPick()
		switch {
		case errors.Is(msg.err, desktop.ErrCanceled):
		case msg.err != nil:
			m.startTyping()
			m.flash = "Couldn't open the folder picker, so type the folder instead: " + printable(msg.err.Error())
		default:
			if err := m.rs.setPicked(msg.path); err != nil {
				m.flash = printable(err.Error())
			} else {
				m.rs.phase = phaseReady
			}
		}
	}
	return m, nil
}

func (m model) restoreHints() []string {
	switch m.rs.phase {
	case phaseConfirm:
		choose, cancel := theme.Hint("1 2 3", "choose"), theme.Hint("esc", "cancel")
		switch {
		case m.rs.dest == destOverwrite:
			return []string{theme.Hint("y", "overwrite"), choose, cancel}
		case m.rs.dest == destNew && m.rs.picked == "":
			return []string{theme.Hint("enter", "choose folder"), choose, cancel}
		case m.rs.dest == destNew:
			return []string{theme.Hint("enter", "continue"), choose, cancel}
		}
		return []string{theme.Hint("enter", "restore"), choose, cancel}
	case phaseReady:
		return readyActions()
	case phasePicking:
		return []string{theme.Hint("t", "type a path"), theme.Hint("esc", "cancel")}
	case phaseTyping:
		return []string{theme.Hint("enter", "use folder"), theme.Hint("esc", "back")}
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
	para := func(st lipgloss.Style, s string) {
		for _, l := range wrapWords(s, w) {
			lines = append(lines, line(st.Render(l)))
		}
	}

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
		const labelW = 28 // the longest label, so the notes line up
		opt := func(d restoreDest, label, note string) string {
			mark := theme.Text.Render("( ) ")
			if rs.dest == d {
				mark = theme.Key.Render("(*)") + theme.Base.Render(" ")
			}
			text := theme.Text.Render(label)
			if rs.unavailable(d) != nil {
				text = theme.Faded.Render(label)
			}
			return line(theme.Key.Render(fmt.Sprintf("[%d]", d+1)) + theme.Base.Render(" ") + mark + text +
				theme.Base.Render(strings.Repeat(" ", labelW-len(label)+2)) + theme.Dim.Render(note))
		}
		// The parent, which is the part that differs: the folder's own name
		// is on the other screens, and a long snapshot ID would crowd it out.
		noteW := w - labelW - 13
		beside := "in " + shortPath(filepath.Dir(rs.beside), noteW)
		if rs.besideErr != nil {
			beside = "not available"
		}
		chosen := "choose a folder"
		if rs.picked != "" {
			chosen = "in " + shortPath(rs.picked, noteW)
		}
		over := "replaces what's there now"
		if rs.overErr != nil {
			over = "not available"
		}
		lines = append(lines,
			opt(destBeside, "Restore to original location", beside),
			opt(destNew, "Restore to new location", chosen),
			opt(destOverwrite, "Overwrite original files", over),
		)
		// Why, for anything greyed out.
		gap := true
		for _, d := range []restoreDest{destBeside, destOverwrite} {
			if err := rs.unavailable(d); err != nil {
				if gap {
					lines, gap = append(lines, fill(w, 1)), false
				}
				para(theme.Dim, fmt.Sprintf("Option %d isn't available: %s.", d+1, printable(err.Error())))
			}
		}
		if rs.dest == destOverwrite {
			lines = append(lines, fill(w, 1), line(theme.Caution.Render("Files at the original paths will be overwritten.")))
		}

	case phasePicking:
		lines = append(lines, line(theme.Bold.Render("Choose a folder")), fill(w, 1))
		para(theme.Text, "Pick where to restore in the window that opened.")
		para(theme.Dim, "frost makes a new frost-restore-"+rs.snap.ID+" folder inside it.")

	case phaseReady:
		lines = append(lines, line(theme.Bold.Render(restoreTitle(rs))))
		para(theme.Dim, "into a new folder, so nothing already there is touched")
		lines = append(lines, fill(w, 1))
		lines = append(lines, landing(rs, w)...)
		lines = append(lines, fill(w, 1))
		// The actions on as few rows as fit.
		row := ""
		for _, a := range readyActions() {
			switch {
			case row == "":
				row = a
			case lipgloss.Width(row)+3+lipgloss.Width(a) <= w:
				row += theme.Base.Render("   ") + a
			default:
				lines, row = append(lines, line(row)), a
			}
		}
		lines = append(lines, line(row))

	case phaseTyping:
		lines = append(lines, line(theme.Bold.Render("Type a folder")))
		para(theme.Dim, "frost makes a new frost-restore-"+rs.snap.ID+" folder inside it.")
		lines = append(lines, fill(w, 1), inputBox(rs.input.fields[0], false, true, w))
		if rs.inputErr != "" {
			para(theme.Error, printable(rs.inputErr))
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
		// A restore that failed before it started has no folder yet.
		to := line(theme.Dim.Render("to ") + theme.Text.Render("their original locations"))
		if rs.dest != destOverwrite {
			to = line(theme.Dim.Render("to ") + theme.Text.Render(shortPath(rs.folder, w-10)))
			if rs.folder == "" {
				to = ""
			}
		}
		if rs.err != nil {
			lines = append(lines, line(theme.Error.Render("Restore failed")))
			if rs.res.Files == 0 {
				lines = append(lines, line(theme.Text.Render("No files were restored.")))
			} else {
				lines = append(lines, line(theme.Text.Render(fmt.Sprintf("%d files completed (%s)", rs.res.Files, humanBytes(rs.res.Bytes)))))
			}
			if to != "" {
				lines = append(lines, to)
			}
			if rs.res.Files > 0 {
				para(theme.Caution, "Earlier changes remain. A file may have been written before a metadata error.")
			}
			lines = append(lines, fill(w, 1))
			para(theme.Text, printable(rs.err.Error()))
			break
		}
		lines = append(lines,
			line(theme.Good.Render("Restored ")+theme.Text.Render(fmt.Sprintf("%d files (%s)", rs.res.Files, humanBytes(rs.res.Bytes)))),
			to,
			fill(w, 1),
			line(theme.Dim.Render("Every chunk was decrypted and checked against its hash.")),
		)
	}

	box := theme.Box(true).Padding(1, 2).Render(stack(lines...))
	return m.center(box)
}

// readyActions are the three things to do once a new location is chosen,
// shown on the screen and in the footer.
func readyActions() []string {
	return []string{theme.Hint("enter", "restore"), theme.Hint("c", "change location"), theme.Hint("esc", "cancel")}
}

// landing draws where a restore to a new location puts things: the chosen
// folder, the new folder frost makes in it, and what goes at its top.
func landing(rs restoreState, w int) []string {
	line := func(s string) string { return pad(truncate2(s, w), w) }
	branch := func(last bool) string {
		if last {
			return "└─ "
		}
		return "├─ "
	}
	lines := []string{
		line(theme.Text.Render(shortPath(rs.picked, w))),
		line(theme.Faded.Render("└─ ") + theme.Bold.Render(filepath.Base(rs.chosen)+"/") + theme.Dim.Render("  new")),
	}
	const most = 4
	tops := rs.tops
	more := 0
	if len(tops) > most {
		tops, more = tops[:most-1], len(tops)-(most-1)
	}
	for i, t := range tops {
		last := i == len(tops)-1 && more == 0
		lines = append(lines, line(theme.Faded.Render("   "+branch(last))+theme.Text.Render(printable(t))))
	}
	if more > 0 {
		lines = append(lines, line(theme.Faded.Render("   └─ ")+theme.Dim.Render(fmt.Sprintf("%d more", more))))
	}
	return lines
}

func restoreTitle(rs restoreState) string {
	if rs.files == 0 {
		return "Restore empty folders from " + rs.snap.ID
	}
	return fmt.Sprintf("Restore %d files (%s) from %s", rs.files, humanBytes(rs.bytes), rs.snap.ID)
}
