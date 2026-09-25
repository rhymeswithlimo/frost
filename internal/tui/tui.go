// Package tui is frost's full-screen snapshot browser.
//
// Screens: home (wordmark and summary), snapshots (by date), files (the tree
// as it was at that snapshot), diff (two snapshots compared), restore
// (confirm and run), plus help and settings overlays. All styling comes from
// internal/theme.
package tui

import (
	"context"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/rhymeswithlimo/frost/internal/config"
	"github.com/rhymeswithlimo/frost/internal/engine"
	"github.com/rhymeswithlimo/frost/internal/repo"
	"github.com/rhymeswithlimo/frost/internal/snapshot"
	"github.com/rhymeswithlimo/frost/internal/sound"
	"github.com/rhymeswithlimo/frost/internal/theme"
)

//go:embed assets/frost-wordmark.txt
var wordmarkRaw string

//go:embed assets/icebreaker-wordmark.txt
var gameWordmarkRaw string

var (
	wordmark     = unixLines(wordmarkRaw)
	gameWordmark = unixLines(gameWordmarkRaw)
)

// unixLines turns CRLF line endings into LF. A Windows checkout with
// core.autocrlf embeds the assets with CRLF, and a \r left in a frame sends
// the cursor back to column 0 mid-row: the rest of that row is drawn over
// its start, and whatever the previous screen left on the right stays there.
func unixLines(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }

//go:embed assets/sfx/*.wav
var sfxFiles embed.FS

// gameSound is opened the first time the game starts, and reused after.
var (
	gameSoundOnce sync.Once
	gameSound     *sound.Player
)

func openGameSound() *sound.Player {
	gameSoundOnce.Do(func() {
		read := func(name string) []byte { b, _ := sfxFiles.ReadFile("assets/sfx/" + name); return b }
		// Pitch and Tempo are how far each play may drift, as a fraction.
		// Big, noisy sounds vary most; short cues the player learns to
		// recognise vary least.
		gameSound = sound.New(map[string]sound.Clip{
			"explosion": {WAV: read("explosion.wav"), Pitch: 0.05, Tempo: 0.04, Volume: 0.5},
			// Every hit on the ice: the start of the explosion, clipped short
			// and quieter, so the full one still stands out when ice breaks.
			"crack": {WAV: read("explosion.wav"), Pitch: 0.05, Tempo: 0.04, Volume: 0.3, Cut: 150 * time.Millisecond},
			// A bullet bouncing off bit rot: the explosion pitched way up,
			// ring-modulated into a metallic ding, crushed to 5-bit for a
			// broken-digital edge, and decayed fast like a struck bell.
			"ding": {WAV: read("explosion.wav"), Shift: 2.5, Ring: 1600, Crush: 5, Decay: 35 * time.Millisecond,
				Cut: 120 * time.Millisecond, Volume: 0.35, Pitch: 0.06, Tempo: 0.03},
			"shoot":   {WAV: read("laser-shoot.wav"), Pitch: 0.03, Tempo: 0.02},
			"hurt":    {WAV: read("hit-hurt.wav"), Pitch: 0.025, Tempo: 0.02},
			"pickup":  {WAV: read("pickup-file.wav"), Pitch: 0.02, Tempo: 0.01},
			"powerup": {WAV: read("power-up.wav"), Pitch: 0.01, Tempo: 0.01},
		})
	})
	return gameSound
}

type screen int

const (
	scrHome screen = iota
	scrSnapshots
	scrFiles
	scrDiff
	scrRestore
)

// State is what the browser needs from the local manifest. It's read up
// front so the manifest (and its lock) can be released while the browser is
// open, and a scheduled backup can still run.
type State struct {
	Known     map[string]snapshot.Snapshot
	Last      engine.LastRun
	HasLast   bool
	Verify    engine.VerifyResult
	HasVerify bool
}

// StateFrom reads State from an engine's manifest.
func StateFrom(e *engine.Engine) State {
	st := State{Known: e.Manifest.Snapshots()}
	st.Last, st.HasLast = e.LastBackup()
	st.Verify, st.HasVerify = e.LastVerify()
	return st
}

// Run opens the browser and blocks until the user quits. It only talks to
// the repository, never the manifest.
func Run(ctx context.Context, r *repo.Repo, cfg config.Config, st State) error {
	m := newModel(ctx, r, cfg, st)
	_, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx)).Run()
	if err == tea.ErrProgramKilled && ctx.Err() != nil {
		return nil
	}
	if err != nil && runtime.GOOS == "windows" && strings.Contains(err.Error(), "console mode") {
		// The console can't take VT sequences: the legacy console, or a
		// Windows 10 older than 1607.
		return fmt.Errorf("%w (this console can't draw the browser: turn off \"Use legacy console\" in its properties, or use Windows Terminal)", err)
	}
	return err
}

type model struct {
	ctx  context.Context
	repo *repo.Repo
	cfg  config.Config
	st   State

	w, h    int
	screen  screen
	overlay string // "", "help" or "settings"
	showKey bool   // the key fingerprint is covered until [v]
	spin    spinner.Model
	loading string // non-empty while waiting on the network
	err     error
	flash   string // one-line message in the footer

	snaps   []snapshot.Snapshot // newest first
	snapCur int
	snapTop int
	marked  string // snapshot ID marked as the "from" side of a diff

	// files screen
	snap    snapshot.Snapshot
	tree    *tree
	dir     string
	fileCur int
	fileTop int
	sel     map[string]bool
	trail   map[string]int // remembered cursor per folder

	selFiles int   // files covered by sel, kept by countSel
	selBytes int64 // and their total size

	// diff screen
	diffFrom, diffTo snapshot.Snapshot
	changes          []snapshot.Change
	diffTop          int
	diffAdd, diffDel int // change counts, worked out once per diff
	diffMod          int

	// restore screen
	rs restoreState

	// the easter egg, nil unless it's open
	game     *arcade
	bestPath string
}

func newModel(ctx context.Context, r *repo.Repo, cfg config.Config, st State) model {
	sp := spinner.New()
	sp.Spinner = spinner.Line
	sp.Style = theme.Bold
	return model{
		ctx: ctx, repo: r, cfg: cfg, st: st, spin: sp,
		loading:  "Loading snapshots",
		bestPath: filepath.Join(config.CacheDir(), "icebreaker.json"),
		sel:      map[string]bool{},
		trail:    map[string]int{},
	}
}

// ---- messages ----

type snapsMsg struct {
	snaps []snapshot.Snapshot
	err   error
}

type treeMsg struct {
	snap snapshot.Snapshot
	tree *snapshot.Tree
	err  error
}

type diffMsg struct {
	from, to snapshot.Snapshot
	changes  []snapshot.Change
	err      error
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.spin.Tick, m.loadSnaps())
}

func (m model) loadSnaps() tea.Cmd {
	return func() tea.Msg {
		snaps, err := m.repo.Snapshots(m.ctx, m.st.Known)
		// On error the list has empty slots for the headers that failed.
		snaps = slices.DeleteFunc(snaps, func(s snapshot.Snapshot) bool { return s.ID == "" })
		slices.SortFunc(snaps, func(a, b snapshot.Snapshot) int { return b.Time.Compare(a.Time) })
		return snapsMsg{snaps, err}
	}
}

func (m model) loadTree(s snapshot.Snapshot) tea.Cmd {
	return func() tea.Msg {
		t, err := m.repo.LoadTree(m.ctx, s.ID)
		return treeMsg{s, t, err}
	}
}

func (m model) loadDiff(from, to snapshot.Snapshot) tea.Cmd {
	if from.Time.After(to.Time) {
		from, to = to, from
	}
	return func() tea.Msg {
		a, err := m.repo.LoadTree(m.ctx, from.ID)
		if err != nil {
			return diffMsg{err: err}
		}
		b, err := m.repo.LoadTree(m.ctx, to.ID)
		if err != nil {
			return diffMsg{err: err}
		}
		return diffMsg{from: from, to: to, changes: snapshot.Diff(a, b)}
	}
}

// ---- update ----

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		if m.game != nil {
			m.game.resize(m.innerW()-2, m.areaH()-3)
		}
		return m, nil

	case spinner.TickMsg:
		if !m.spinning() {
			return m, nil // let the tick loop die; the next load starts a new one
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case snapsMsg:
		m.loading = ""
		m.snaps, m.err = msg.snaps, msg.err
		m.snapCur = max(min(m.snapCur, len(m.snaps)-1), 0) // a refresh can shrink the list
		return m, nil

	case treeMsg:
		m.loading = ""
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.snap, m.tree = msg.snap, newTree(msg.snap, msg.tree)
		m.dir, m.fileCur, m.fileTop = rootKey, 0, 0
		m.sel, m.trail = map[string]bool{}, map[string]int{}
		m.selFiles, m.selBytes = 0, 0
		if len(m.tree.roots) == 1 {
			m.dir = m.tree.roots[0] // skip a pointless one-item level
		}
		m.screen = scrFiles
		return m, nil

	case diffMsg:
		m.loading = ""
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.diffFrom, m.diffTo, m.changes, m.diffTop = msg.from, msg.to, msg.changes, 0
		m.diffAdd, m.diffDel, m.diffMod = 0, 0, 0
		for _, c := range m.changes {
			switch c.Kind {
			case snapshot.Added:
				m.diffAdd++
			case snapshot.Removed:
				m.diffDel++
			case snapshot.Modified:
				m.diffMod++
			}
		}
		m.screen = scrDiff
		return m, nil

	case arcadeTickMsg:
		if m.game != nil {
			return m, m.game.tick(msg)
		}
		return m, nil

	case restoreProgressMsg, restoreDoneMsg:
		return m.updateRestore(msg)

	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := k.String()
	m.flash = ""

	if key == "ctrl+c" {
		return m, tea.Quit
	}
	if m.game != nil {
		cmd, done := m.game.key(key)
		if done {
			m.game = nil
		}
		return m, cmd
	}
	if key == "v" {
		m.showKey = !m.showKey
		return m, nil
	}
	if m.overlay != "" {
		if key == "esc" || key == "h" || key == "s" || key == "q" || key == "?" {
			m.overlay = ""
		}
		return m, nil
	}
	if m.err != nil {
		if key == "q" {
			return m, tea.Quit
		}
		m.err = nil // any key dismisses the error
		return m, nil
	}
	if m.loading != "" {
		if key == "q" {
			return m, tea.Quit
		}
		return m, nil
	}
	if m.screen == scrRestore {
		return m.restoreKey(key)
	}

	switch key {
	case "q":
		return m, tea.Quit
	case "h", "?":
		m.overlay = "help"
		return m, nil
	case "i": // easter egg
		m.game = newArcade(m.bestPath, uint64(time.Now().UnixNano()))
		m.game.snd = openGameSound()
		m.game.resize(m.innerW()-2, m.areaH()-3)
		return m, nil
	case "s":
		m.overlay = "settings"
		return m, nil
	}

	switch m.screen {
	case scrHome:
		switch key {
		case "enter", "b":
			m.screen = scrSnapshots
		case "r":
			m.loading = "Refreshing"
			return m, tea.Batch(m.spin.Tick, m.loadSnaps())
		}
	case scrSnapshots:
		return m.snapshotsKey(key)
	case scrFiles:
		return m.filesKey(key)
	case scrDiff:
		switch key {
		case "esc", "backspace", "left":
			m.screen = scrSnapshots
		case "up", "k":
			m.diffTop = max(m.diffTop-1, 0)
		case "down", "j":
			m.diffTop = min(m.diffTop+1, m.diffMaxTop())
		case "pgup":
			m.diffTop = max(m.diffTop-m.bodyH(), 0)
		case "pgdown", " ":
			m.diffTop = min(m.diffTop+m.bodyH(), m.diffMaxTop())
		}
	}
	return m, nil
}

func (m model) snapshotsKey(key string) (tea.Model, tea.Cmd) {
	n := len(m.snaps)
	switch key {
	case "esc", "backspace", "left":
		m.screen = scrHome
	case "up", "k":
		m.snapCur = max(m.snapCur-1, 0)
	case "down", "j":
		m.snapCur = min(m.snapCur+1, n-1)
	case "pgup":
		m.snapCur = max(m.snapCur-m.bodyH(), 0)
	case "pgdown":
		m.snapCur = min(m.snapCur+m.bodyH(), n-1)
	case "home", "g":
		m.snapCur = 0
	case "end", "G":
		m.snapCur = n - 1
	case "enter", "right", "l":
		if n > 0 {
			m.loading = "Loading files for " + m.snaps[m.snapCur].ID
			return m, tea.Batch(m.spin.Tick, m.loadTree(m.snaps[m.snapCur]))
		}
	case "m":
		if n == 0 {
			break
		}
		if id := m.snaps[m.snapCur].ID; m.marked == id {
			m.marked = ""
		} else {
			m.marked = id
			m.flash = "Marked " + id + ". Move to another snapshot and press [d] to compare."
		}
	case "d":
		if n == 0 {
			break
		}
		cur := m.snaps[m.snapCur]
		var from snapshot.Snapshot
		switch {
		case m.marked != "" && m.marked != cur.ID:
			for _, s := range m.snaps {
				if s.ID == m.marked {
					from = s
				}
			}
		case m.snapCur+1 < n:
			from = m.snaps[m.snapCur+1] // no mark: compare with the one before
		default:
			m.flash = "This is the oldest snapshot, nothing to compare it with."
			return m, nil
		}
		m.loading = "Comparing snapshots"
		return m, tea.Batch(m.spin.Tick, m.loadDiff(from, cur))
	}
	m.snapCur = max(m.snapCur, 0) // moving in an empty list
	return m, nil
}

func (m model) filesKey(key string) (tea.Model, tea.Cmd) {
	kids := m.tree.children[m.dir]
	n := len(kids)
	switch key {
	case "esc":
		m.screen = scrSnapshots
	case "up", "k":
		m.fileCur = max(m.fileCur-1, 0)
	case "down", "j":
		m.fileCur = min(m.fileCur+1, n-1)
	case "pgup":
		m.fileCur = max(m.fileCur-m.bodyH(), 0)
	case "pgdown":
		m.fileCur = min(m.fileCur+m.bodyH(), n-1)
	case "home", "g":
		m.fileCur = 0
	case "end", "G":
		m.fileCur = max(n-1, 0)
	case "enter", "right", "l":
		if n > 0 && m.tree.isDir(kids[m.fileCur]) {
			m.trail[m.dir] = m.fileCur
			m.dir, m.fileCur, m.fileTop = kids[m.fileCur], m.trail[kids[m.fileCur]], 0
		}
	case "left", "backspace":
		if m.dir == rootKey || (len(m.tree.roots) == 1 && m.dir == m.tree.roots[0]) {
			m.screen = scrSnapshots
			break
		}
		m.trail[m.dir] = m.fileCur
		up := m.tree.parent(m.dir)
		m.fileCur = max(slices.Index(m.tree.children[up], m.dir), 0)
		m.dir, m.fileTop = up, 0
	case " ", "x":
		if n == 0 {
			break
		}
		p := kids[m.fileCur]
		if !m.sel[p] && covered(p, m.sel) {
			m.flash = "Its folder is already selected."
			break
		}
		if m.sel[p] {
			delete(m.sel, p)
		} else {
			m.sel[p] = true
			// A folder's selection replaces any selections inside it.
			for q := range m.sel {
				if q != p && strings.HasPrefix(q, p+"/") {
					delete(m.sel, q)
				}
			}
		}
		m.fileCur = min(m.fileCur+1, n-1)
	case "a":
		all := true
		for _, p := range kids {
			all = all && m.sel[p]
		}
		for _, p := range kids {
			if all {
				delete(m.sel, p)
			} else if !covered(p, m.sel) {
				m.sel[p] = true
			}
		}
	case "c":
		m.sel = map[string]bool{}
	case "r":
		paths := m.selectedPaths()
		if len(paths) == 0 && n > 0 {
			paths = []string{kids[m.fileCur]}
		}
		if len(paths) == 0 {
			break
		}
		m.rs = newRestoreState(m.snap, paths, m.tree)
		m.screen = scrRestore
	}
	m.fileCur = max(m.fileCur, 0) // moving in an empty folder
	switch key {
	case " ", "x", "a", "c":
		m.countSel()
	}
	return m, nil
}

// countSel works out selFiles and selBytes. It walks the whole tree, so it
// runs when the selection changes, not on every frame.
func (m *model) countSel() {
	m.selFiles, m.selBytes = 0, 0
	if len(m.sel) == 0 || m.tree == nil {
		return
	}
	for p, f := range m.tree.files {
		if f.Type == snapshot.TypeFile && covered(p, m.sel) {
			m.selFiles++
			m.selBytes += f.Size
		}
	}
}

// spinning reports whether anything on screen shows the spinner.
func (m model) spinning() bool {
	return m.loading != "" || (m.screen == scrRestore && m.rs.phase == phaseRunning)
}

func (m model) selectedPaths() []string {
	var out []string
	for p := range m.sel {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// ---- layout ----

// innerW is the usable width inside the screen margins.
func (m model) innerW() int { return max(m.w-2*theme.PadX, 20) }

// areaH is the height between the header and footer rows.
func (m model) areaH() int { return max(m.h-2*theme.PadY-5, 5) }

// bodyH is the content height of a bordered panel filling the area.
func (m model) bodyH() int { return m.areaH() - 2 }

// diffMaxTop is the furthest the diff list scrolls: the last change on the
// last row. Two rows of the panel are taken by the summary and its rule.
func (m model) diffMaxTop() int { return max(len(m.changes)-(m.bodyH()-2), 0) }

func (m model) View() string {
	if m.w == 0 {
		return ""
	}
	var body string
	switch {
	case m.err != nil:
		body = m.viewError()
	case m.loading != "" && m.screen == scrHome:
		body = m.viewHome()
	case m.loading != "":
		body = m.center(theme.Bold.Render(m.spin.View()) + theme.Text.Render(" "+m.loading+"..."))
	case m.game != nil:
		body = m.center(m.game.view())
	case m.overlay == "help":
		body = m.viewHelp()
	case m.overlay == "settings":
		body = m.viewSettings()
	default:
		switch m.screen {
		case scrHome:
			body = m.viewHome()
		case scrSnapshots:
			body = m.viewSnapshots()
		case scrFiles:
			body = m.viewFiles()
		case scrDiff:
			body = m.viewDiff()
		case scrRestore:
			body = m.viewRestore()
		}
	}

	w := m.innerW()
	body = clip(body, w, m.areaH())
	body = lipgloss.Place(w, m.areaH(), lipgloss.Left, lipgloss.Top, body,
		lipgloss.WithWhitespaceBackground(theme.Bg))

	page := lipgloss.JoinVertical(lipgloss.Left, m.header(), fill(w, 1), body, fill(w, 1), m.footer())
	page = lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center, page,
		lipgloss.WithWhitespaceBackground(theme.Bg))
	return clip(page, m.w, m.h)
}

func (m model) header() string {
	left := theme.Title.Render("FROST")
	crumb := ""
	switch m.screen {
	case scrSnapshots:
		crumb = "snapshots"
	case scrFiles:
		crumb = m.snap.ID + "  " + shortPath(m.dir, m.innerW()-40)
	case scrDiff:
		crumb = "compare " + m.diffFrom.ID + " > " + m.diffTo.ID
	case scrRestore:
		crumb = "restore from " + m.rs.snap.ID
	}
	if m.game != nil {
		crumb = "icebreaker"
	} else if m.overlay != "" {
		crumb = m.overlay
	}
	if crumb != "" {
		left += theme.Dim.Render("  " + crumb)
	}
	right := theme.Dim.Render(m.repo.Backend.String()+"  key ") + m.keyLabel()
	gap := m.innerW() - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return left + fill(max(m.innerW()-lipgloss.Width(left), 0), 1)
	}
	return left + fill(gap, 1) + right
}

func (m model) footer() string {
	w := m.innerW()
	rule := theme.Faded.Render(strings.Repeat("─", w))
	if m.flash != "" {
		return rule + "\n" + pad(theme.Caution.Render(m.flash), w)
	}
	var hints []string
	switch {
	case m.game != nil:
		hints = []string{theme.Hint("← →", "move"), theme.Hint("space", "shoot"), theme.Hint("p", "pause")}
		if m.game.snd.Available() {
			label := "mute"
			if m.game.muted {
				label = "unmute"
			}
			hints = append(hints, theme.Hint("m", label))
		}
		hints = append(hints, theme.Hint("esc", "back"))
	case m.err != nil:
		hints = []string{theme.Hint("any key", "dismiss"), theme.Hint("q", "quit")}
	case m.overlay != "":
		hints = []string{theme.Hint("esc", "close"), theme.Hint("v", m.keyHint())}
	case m.loading != "":
		hints = []string{theme.Hint("q", "quit")}
	default:
		switch m.screen {
		case scrHome:
			hints = []string{theme.Hint("enter", "browse snapshots"), theme.Hint("r", "refresh")}
		case scrSnapshots:
			hints = []string{theme.Hint("enter", "open"), theme.Hint("d", "diff"), theme.Hint("m", "mark"), theme.Hint("esc", "back")}
		case scrFiles:
			hints = []string{theme.Hint("enter", "open"), theme.Hint("space", "select"), theme.Hint("r", "restore"), theme.Hint("←", "up"), theme.Hint("esc", "snapshots")}
		case scrDiff:
			hints = []string{theme.Hint("↑↓", "scroll"), theme.Hint("esc", "back")}
		case scrRestore:
			hints = m.restoreHints()
		}
		if m.screen != scrRestore {
			// Optional hints go first when space runs out; help and quit stay.
			optional := []string{theme.Hint("v", m.keyHint()), theme.Hint("s", "settings")}
			keep := []string{theme.Hint("h", "help"), theme.Hint("q", "quit")}
			for len(optional) > 0 && lipgloss.Width(strings.Join(slices.Concat(hints, optional, keep), "   ")) > w {
				optional = optional[:len(optional)-1]
			}
			for len(hints) > 0 && lipgloss.Width(strings.Join(slices.Concat(hints, keep), "   ")) > w {
				hints = hints[:len(hints)-1]
			}
			hints = slices.Concat(hints, optional, keep)
		}
	}
	return rule + "\n" + pad(joinFit(hints, w), w)
}

// keyLabel is the key fingerprint, or a black cover over it while hidden.
func (m model) keyLabel() string {
	fp := m.repo.Key.Fingerprint()
	if m.showKey {
		return theme.Text.Render(fp)
	}
	return theme.Redacted.Render(strings.Repeat(" ", len(fp)))
}

func (m model) keyHint() string {
	if m.showKey {
		return "hide key"
	}
	return "show key"
}

func (m model) viewError() string {
	box := theme.Box(true).BorderForeground(theme.Bad).Width(min(m.innerW(), 70)).Render(
		theme.Error.Render("Something went wrong") + "\n\n" + theme.Text.Render(wrap(printable(m.err.Error()), min(m.innerW(), 70)-4)))
	return m.center(box)
}

func (m model) center(s string) string {
	return lipgloss.Place(m.innerW(), m.areaH(), lipgloss.Center, lipgloss.Center, s,
		lipgloss.WithWhitespaceBackground(theme.Bg))
}

// solid pads every line of a block to the block's width with background,
// filling the gaps lipgloss's joins leave uncoloured.
func solid(s string) string {
	w := lipgloss.Width(s)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = pad(l, w)
	}
	return strings.Join(lines, "\n")
}

// stack puts blocks on top of each other, left-aligned, on a solid background.
func stack(blocks ...string) string {
	w := 0
	for _, b := range blocks {
		w = max(w, lipgloss.Width(b))
	}
	var lines []string
	for _, b := range blocks {
		for _, l := range strings.Split(b, "\n") {
			lines = append(lines, pad(l, w))
		}
	}
	return strings.Join(lines, "\n")
}

// side puts blocks next to each other, top-aligned, with gap cells between.
func side(gap int, blocks ...string) string {
	h := 0
	for _, b := range blocks {
		h = max(h, lipgloss.Height(b))
	}
	var parts []string
	for i, b := range blocks {
		b = solid(b)
		w := lipgloss.Width(b)
		if d := h - lipgloss.Height(b); d > 0 {
			b += "\n" + fill(w, d)
		}
		if i > 0 {
			parts = append(parts, fill(gap, h))
		}
		parts = append(parts, b)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

// clip cuts a block down to at most w by h cells.
func clip(s string, w, h int) string {
	return lipgloss.NewStyle().MaxWidth(w).MaxHeight(h).Render(s)
}

// ---- small helpers ----

// fill is a block of background-coloured spaces.
func fill(w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	line := theme.Base.Render(strings.Repeat(" ", w))
	return strings.TrimSuffix(strings.Repeat(line+"\n", h), "\n")
}

// pad fits a styled line to exactly w cells: cut if longer, padded with
// background if shorter.
func pad(s string, w int) string {
	switch d := w - lipgloss.Width(s); {
	case d > 0:
		return s + fill(d, 1)
	case d < 0:
		return lipgloss.NewStyle().MaxWidth(w).Render(s)
	}
	return s
}

// joinFit joins hints with spacing, dropping from the end if they don't fit.
func joinFit(hints []string, w int) string {
	sep := theme.Base.Render("   ")
	for len(hints) > 0 {
		s := strings.Join(hints, sep)
		if lipgloss.Width(s) <= w {
			return s
		}
		hints = hints[:len(hints)-1]
	}
	return ""
}

func wrap(s string, w int) string {
	return lipgloss.NewStyle().Width(max(w, 10)).Render(s)
}

func shortPath(p string, w int) string {
	if p == rootKey {
		return "/"
	}
	if home, _ := os.UserHomeDir(); home != "" {
		// Snapshot paths use forward slashes, local ones may not.
		for _, h := range []string{filepath.ToSlash(home), home} {
			if n := len(h); len(p) >= n && samePath(p[:n], h) && (len(p) == n || p[n] == '/' || p[n] == filepath.Separator) {
				p = "~" + p[n:]
				break
			}
		}
	}
	return truncateLeft(p, max(w, 12))
}

// samePath compares paths the way the OS does: case-insensitively on Windows.
func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func humanBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
