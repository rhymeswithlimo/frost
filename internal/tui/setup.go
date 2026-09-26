package tui

// The full-screen `frost init` wizard. It's only the screens: everything
// that touches storage, keys or the scheduler comes in through SetupDeps.

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/rhymeswithlimo/frost/internal/config"
	"github.com/rhymeswithlimo/frost/internal/crypto"
	"github.com/rhymeswithlimo/frost/internal/theme"
)

// RepoState is what setup found in the storage, relative to the key on
// this machine.
type RepoState int

const (
	RepoNew         RepoState = iota // no backups there yet
	RepoLocalOK                      // backups there, and this machine's key opens them
	RepoNeedsPhrase                  // backups there, and no key on this machine
	RepoLocalWrong                   // backups there, made with a different key
)

// SetupDeps is what the wizard calls out to. Errors from Connect and Unlock
// are shown to the user as they are, so they should be in plain words.
type SetupDeps struct {
	LocalKey  *crypto.Key // the key on this machine, or nil
	Connect   func(ctx context.Context, s config.Storage) (RepoState, error)
	NewKey    func() (*crypto.Key, error)
	Unlock    func(ctx context.Context, s config.Storage, phrase string) (*crypto.Key, error)
	Finish    func(ctx context.Context, cfg config.Config, key *crypto.Key, newRepo bool) ([][2]string, error)
	PickWords func() (int, int)
	DirExists func(path string) bool
	Scheduler string // "launchd", "systemd", ...
}

// ConnectError is a connect failure caused by one answer in particular, so
// setup can go back to that question. About matches a field's about.
type ConnectError struct {
	About string // "key", "secret", "bucket" or "address"
	Msg   string
}

func (e *ConnectError) Error() string { return e.Msg }

// SetupResult is what happened. Rows are label/value pairs describing what
// was saved, for printing after the screen closes.
type SetupResult struct {
	Saved bool
	Rows  [][2]string
}

// Setup runs the wizard. cfg holds the current config (or defaults), and
// existing says whether it came from a saved config.toml.
func Setup(ctx context.Context, deps SetupDeps, cfg config.Config, existing bool) (SetupResult, error) {
	final, err := tea.NewProgram(newSetup(ctx, deps, cfg, existing), tea.WithAltScreen(), tea.WithContext(ctx)).Run()
	if err := programErr(ctx, err); err != nil {
		return SetupResult{}, err
	}
	m, ok := final.(setupModel)
	if !ok {
		return SetupResult{}, nil
	}
	return SetupResult{Saved: m.saved, Rows: m.savedRows}, nil
}

type setupStep int

const (
	stWelcome setupStep = iota
	stStorage
	stDetails
	stFolders
	stSkip
	stSchedule
	stPhrase
	stCheck
	stUnlock
	stReview
	stDone
)

// stepOf places a screen in the progress bar. 0 means it isn't counted.
func stepOf(s setupStep) int {
	switch s {
	case stStorage, stDetails:
		return 1
	case stFolders, stSkip:
		return 2
	case stSchedule:
		return 3
	case stPhrase, stCheck, stUnlock:
		return 4
	case stReview:
		return 5
	}
	return 0
}

var stepNames = []string{"", "storage", "folders", "schedule", "recovery phrase", "review"}

const setupSteps = 5

type setupModel struct {
	ctx      context.Context
	deps     SetupDeps
	cfg      config.Config
	existing bool

	w, h int
	step setupStep
	back []setupStep // for esc
	spin spinner.Model
	busy string // non-empty while waiting on storage
	err  string // shown on the current screen until the next key press
	note string // the same, for news that isn't a problem

	// storage
	provCur   int  // cursor in the provider list
	prov      int  // the provider being asked about
	details   form // its questions, one at a time
	pending   config.Storage
	connected bool
	state     RepoState
	autoTried bool // a saved config gets one silent connect

	// folders: the input box has focus while folderSel is -1
	folderIn  form
	folderSel int
	skipIn    form

	// schedule
	schedCur int

	// key
	key       *crypto.Key
	newRepo   bool
	keyReady  bool
	showWords bool
	seenWords bool
	check     form
	checkIdx  [2]int
	phrase    form

	// review
	revCur  int
	showKey bool
	visited map[setupStep]bool

	saved     bool
	savedRows [][2]string
}

func newSetup(ctx context.Context, deps SetupDeps, cfg config.Config, existing bool) setupModel {
	sp := spinner.New()
	sp.Spinner = spinner.Line
	sp.Style = theme.Bold
	m := setupModel{ctx: ctx, deps: deps, cfg: cfg, existing: existing, spin: sp, visited: map[setupStep]bool{}, folderSel: -1}
	if existing {
		m.visited[stFolders] = true
		m.visited[stSchedule] = true
	}
	m.provCur = matchProvider(cfg.Storage)
	m.schedCur = max(slices.Index(m.schedOptions(), m.schedValue()), 0)
	m.folderIn = form{fields: []field{{placeholder: "type a path, like ~/Pictures"}}}
	return m
}

// ---- messages ----

type connectMsg struct {
	state RepoState
	err   error
}

type unlockMsg struct {
	key *crypto.Key
	err error
}

type finishMsg struct {
	rows [][2]string
	err  error
}

func (m setupModel) Init() tea.Cmd { return nil }

// ---- navigation ----

func (m setupModel) goTo(s setupStep) setupModel {
	if s != m.step {
		m.back = append(m.back, m.step)
	}
	m.step, m.err = s, ""
	return m
}

func (m setupModel) goBack() setupModel {
	if n := len(m.back); n > 0 {
		m.step, m.back, m.err = m.back[n-1], m.back[:n-1], ""
		m.showWords = false
	}
	return m
}

// advance goes to the first thing that still needs an answer, or to the
// review once everything has one.
func (m setupModel) advance() (tea.Model, tea.Cmd) {
	switch {
	case !m.connected:
		// A saved config gets one silent try, if it names any storage.
		if m.existing && !m.autoTried && m.cfg.Storage.Backend != "" {
			m.autoTried = true
			return m.connect(m.cfg.Storage)
		}
		return m.goTo(stStorage), nil
	case !m.visited[stFolders]:
		m.folderSel = -1
		return m.goTo(stFolders), nil
	case !m.visited[stSchedule]:
		return m.goTo(stSchedule), nil
	case !m.keyReady:
		return m.keyStep()
	}
	return m.goTo(stReview), nil
}

// keyStep sorts out the key for the storage just connected to.
func (m setupModel) keyStep() (tea.Model, tea.Cmd) {
	local := m.deps.LocalKey
	switch m.state {
	case RepoLocalOK:
		m.key, m.newRepo, m.keyReady = local, false, true
		return m.advance()
	case RepoNew:
		m.newRepo = true
		if local != nil {
			m.key, m.keyReady = local, true
			return m.advance()
		}
		if m.key == nil || m.key == local {
			k, err := m.deps.NewKey()
			if err != nil {
				m.err = err.Error()
				return m, nil
			}
			m.key = k
		}
		m.showWords, m.seenWords = false, false
		return m.goTo(stPhrase), nil
	}
	m.phrase = form{fields: []field{{secret: true, placeholder: "all 24 words, separated by spaces"}}}
	return m.goTo(stUnlock), nil
}

func (m setupModel) connect(s config.Storage) (tea.Model, tea.Cmd) {
	m.busy, m.err, m.pending = "Connecting to your storage", "", s
	ctx, fn := m.ctx, m.deps.Connect
	return m, tea.Batch(m.spin.Tick, func() tea.Msg {
		st, err := fn(ctx, s)
		return connectMsg{st, err}
	})
}

// ---- update ----

func (m setupModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil

	case spinner.TickMsg:
		if m.busy == "" {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case connectMsg:
		m.busy = ""
		if msg.err != nil {
			// Straight from a saved config: show the questions so it can be fixed.
			if m.step != stDetails {
				m = m.openDetails(matchProvider(m.pending))
				m = m.goTo(stDetails)
			}
			// Go back to the question the problem is about, if there's one.
			var ce *ConnectError
			if errors.As(msg.err, &ce) {
				for i, f := range m.details.fields {
					if f.about == ce.About {
						m.details.focus = i
						break
					}
				}
			}
			m.err = msg.err.Error()
			return m, nil
		}
		if m.cfg.Storage != m.pending || !m.connected || m.state != msg.state {
			m.keyReady = false
			if m.key == m.deps.LocalKey {
				m.key = nil
			}
		}
		m.cfg.Storage, m.connected, m.state = m.pending, true, msg.state
		return m.advance()

	case unlockMsg:
		m.busy = ""
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.key, m.newRepo, m.keyReady = msg.key, false, true
		return m.advance()

	case finishMsg:
		m.busy = ""
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.saved, m.savedRows, m.back = true, msg.rows, nil
		m.step, m.err = stDone, ""
		return m, nil

	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			return m, tea.Quit
		}
		if m.busy != "" {
			return m, nil
		}
		return m.onKey(msg)
	}
	return m, nil
}

// typing reports whether the screen has a text box, where letters are text
// and not shortcuts.
func (m setupModel) typing() bool {
	switch m.step {
	case stDetails, stCheck, stUnlock, stSkip:
		return true
	case stFolders:
		return m.folderSel < 0
	}
	return false
}

func (m setupModel) onKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	m.err, m.note = "", "" // a message lasts until the next key press
	if !m.typing() && s == "q" && m.step != stDone {
		return m, tea.Quit
	}

	switch m.step {
	case stWelcome:
		switch s {
		case "enter":
			return m.advance()
		case "esc":
			return m, tea.Quit
		}

	case stStorage:
		switch s {
		case "up", "k":
			m.provCur = max(m.provCur-1, 0)
		case "down", "j":
			m.provCur = min(m.provCur+1, len(providers)-1)
		case "enter":
			m = m.openDetails(m.provCur)
			return m.goTo(stDetails), nil
		case "esc":
			return m.goBack(), nil
		default:
			if n := int(s[0]) - '1'; len(s) == 1 && n >= 0 && n < len(providers) {
				m.provCur = n
			}
		}

	case stDetails:
		d := &m.details
		switch {
		case s == "esc" && d.focus > 0:
			d.focus--
			return m, nil
		case s == "esc":
			return m.goBack(), nil
		case k.Type == tea.KeyTab && d.fields[d.focus].secret:
			d.reveal = !d.reveal
			return m, nil
		case !d.key(k):
			return m, nil
		}
		if msg := d.fields[d.focus].problem(); msg != "" {
			m.err = msg
			return m, nil
		}
		if d.focus < len(d.fields)-1 {
			d.focus++
			return m, nil
		}
		st := m.cfg.Storage
		providers[m.prov].apply(d.values(), &st)
		return m.connect(st)

	case stFolders:
		return m.foldersKey(k)

	case stSkip:
		switch {
		case s == "esc":
			return m.goBack(), nil
		case m.skipIn.key(k):
			list := splitList(m.skipIn.values()[0])
			for _, p := range list {
				if _, err := path.Match(p, ""); err != nil {
					m.err = fmt.Sprintf("%q isn't a valid pattern. Check its brackets.", p)
					return m, nil
				}
			}
			m.cfg.Exclude = list
			return m.advance()
		}

	case stSchedule:
		opts := m.schedOptions()
		switch s {
		case "up", "k":
			m.schedCur = max(m.schedCur-1, 0)
		case "down", "j":
			m.schedCur = min(m.schedCur+1, len(opts)-1)
		case "esc":
			return m.goBack(), nil
		case "enter":
			if v := opts[m.schedCur]; v == "off" {
				m.cfg.Schedule.Enabled = false
			} else {
				m.cfg.Schedule.Enabled, m.cfg.Schedule.Every = true, v
			}
			m.visited[stSchedule] = true
			return m.advance()
		}

	case stPhrase:
		switch s {
		case "v":
			m.showWords = !m.showWords
			m.seenWords = true
		case "esc":
			return m.goBack(), nil
		case "enter":
			if !m.seenWords {
				m.err = "Press [v] to show the words, and write them down first."
				return m, nil
			}
			i, j := m.deps.PickWords()
			m.checkIdx = [2]int{i, j}
			m.check = form{fields: []field{
				{question: "What's word " + strconv.Itoa(i+1) + "?"},
				{question: "And word " + strconv.Itoa(j+1) + "?"},
			}}
			m.showWords = false
			return m.goTo(stCheck), nil
		}

	case stCheck:
		c := &m.check
		switch {
		case s == "esc": // back to the words, from either question
			return m.goBack(), nil
		case !c.key(k):
			return m, nil
		}
		n := m.checkIdx[c.focus] + 1
		got := c.values()[c.focus]
		switch {
		case got == "":
			m.err = fmt.Sprintf("Type word %d from your copy.", n)
			return m, nil
		case !strings.EqualFold(got, strings.Fields(m.key.Phrase())[n-1]):
			c.fields[c.focus].value = ""
			m.err = fmt.Sprintf("That's not word %d. Check your copy, or press [esc] to see the words again.", n)
			return m, nil
		case c.focus == 0:
			c.focus = 1
			return m, nil
		}
		m.keyReady = true
		return m.advance()

	case stUnlock:
		switch {
		case s == "esc":
			return m.goBack(), nil
		case k.Type == tea.KeyTab:
			m.phrase.reveal = !m.phrase.reveal
			return m, nil
		case !m.phrase.key(k):
			return m, nil
		}
		words := strings.Fields(m.phrase.values()[0])
		switch n := len(words); {
		case n == 0:
			m.err = "Type your recovery phrase."
			return m, nil
		case n != 24:
			m.err = fmt.Sprintf("That's %d words. A recovery phrase has 24.", n)
			return m, nil
		}
		phrase := strings.Join(words, " ")
		m.busy = "Checking the phrase"
		ctx, fn, st := m.ctx, m.deps.Unlock, m.cfg.Storage
		return m, tea.Batch(m.spin.Tick, func() tea.Msg {
			key, err := fn(ctx, st, phrase)
			return unlockMsg{key, err}
		})

	case stReview:
		rows := m.reviewRows()
		switch s {
		case "up", "k":
			m.revCur = max(m.revCur-1, 0)
		case "down", "j":
			m.revCur = min(m.revCur+1, len(rows)-1)
		case "v":
			m.showKey = !m.showKey
		case "esc":
			return m.goBack(), nil
		case "e", " ", "right", "l":
			switch rows[m.revCur].edit {
			case stStorage:
				m.provCur = matchProvider(m.cfg.Storage)
				return m.goTo(stStorage), nil
			case stFolders:
				m.visited[stFolders], m.folderSel = false, -1
				return m.goTo(stFolders), nil
			case stSkip:
				m.skipIn = form{fields: []field{{
					value:       strings.Join(m.cfg.Exclude, ", "),
					placeholder: "nothing, back up every file",
				}}}
				return m.goTo(stSkip), nil
			case stSchedule:
				m.visited[stSchedule] = false
				return m.goTo(stSchedule), nil
			}
		case "enter":
			m.busy, m.err = "Saving", ""
			ctx, fn, cfg, key, isNew := m.ctx, m.deps.Finish, m.cfg, m.key, m.newRepo
			return m, tea.Batch(m.spin.Tick, func() tea.Msg {
				rows, err := fn(ctx, cfg, key, isNew)
				return finishMsg{rows, err}
			})
		}

	case stDone:
		if s == "enter" || s == "q" || s == "esc" {
			return m, tea.Quit
		}
	}
	return m, nil
}

// foldersKey: the input box adds a folder, and up moves into the list
// above it to pick one to remove.
func (m setupModel) foldersKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	if s == "esc" && m.folderSel < 0 {
		return m.goBack(), nil
	}
	if m.folderSel >= 0 {
		switch s {
		case "esc", "enter":
			m.folderSel = -1
		case "up", "k":
			m.folderSel = max(m.folderSel-1, 0)
		case "down", "j":
			if m.folderSel++; m.folderSel >= len(m.cfg.Paths) {
				m.folderSel = -1
			}
		case "x", "backspace", "delete":
			m.cfg.Paths = slices.Delete(slices.Clone(m.cfg.Paths), m.folderSel, m.folderSel+1)
			if m.folderSel >= len(m.cfg.Paths) {
				m.folderSel = len(m.cfg.Paths) - 1
			}
		}
		return m, nil
	}
	if s == "up" && len(m.cfg.Paths) > 0 {
		m.folderSel = len(m.cfg.Paths) - 1
		return m, nil
	}
	if !m.folderIn.key(k) {
		return m, nil
	}
	typed := m.folderIn.values()[0]
	if typed == "" {
		if len(m.cfg.Paths) == 0 {
			m.err = "Add at least one folder to continue."
			return m, nil
		}
		m.visited[stFolders] = true
		return m.advance()
	}
	clean, inside, err := config.AddPath(typed, m.cfg.Paths)
	if err != nil {
		m.err = err.Error()
		return m, nil
	}
	var paths, dropped []string
	for i, p := range m.cfg.Paths {
		if slices.Contains(inside, i) {
			dropped = append(dropped, p)
		} else {
			paths = append(paths, p)
		}
	}
	m.cfg.Paths = append(paths, clean)
	if len(dropped) > 0 {
		m.note = "Took " + strings.Join(dropped, ", ") + " off the list, " + clean + " includes it."
	}
	m.folderIn.fields = []field{{placeholder: m.folderIn.fields[0].placeholder}}
	return m, nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" && p != "-" {
			out = append(out, p)
		}
	}
	return out
}

// ---- schedule ----

func (m setupModel) schedOptions() []string {
	opts := []string{"hourly", "6h", "12h", "daily", "weekly", "off"}
	if v := m.schedValue(); !slices.Contains(opts, v) {
		opts = slices.Insert(opts, len(opts)-1, v)
	}
	return opts
}

func (m setupModel) schedValue() string {
	if !m.cfg.Schedule.Enabled {
		return "off"
	}
	if m.cfg.Schedule.Every == "" {
		return "daily"
	}
	return m.cfg.Schedule.Every
}

func schedLabel(v string) string {
	switch v {
	case "hourly", "daily", "weekly":
		return strings.ToUpper(v[:1]) + v[1:]
	case "off":
		return "Off, I'll run `frost backup` myself"
	}
	return "Every " + strings.TrimSuffix(v, "h") + " hours"
}

// ---- review ----

type reviewRow struct {
	label, value string
	edit         setupStep // the step that edits it
}

func (m setupModel) reviewRows() []reviewRow {
	sched := "off"
	if m.cfg.Schedule.Enabled {
		sched = strings.ToLower(schedLabel(m.schedValue()))
		if m.deps.Scheduler != "" {
			sched += ", via " + m.deps.Scheduler
		}
	}
	skip := strings.Join(m.cfg.Exclude, ", ")
	if skip == "" {
		skip = "nothing"
	}
	return []reviewRow{
		{"storage", describeStorage(m.cfg.Storage), stStorage},
		{"folders", strings.Join(m.cfg.Paths, ", "), stFolders},
		{"skip", skip, stSkip},
		{"schedule", sched, stSchedule},
	}
}

// ---- text input ----

// field is one question with a one-line answer.
type field struct {
	question, help, value, placeholder string
	secret, optional                   bool
	name                               string              // what the answer is, for "Type the bucket name"
	about                              string              // the ConnectError.About it can cause
	check                              func(string) string // a problem with the answer, or ""
}

// problem says what's wrong with the answer, or "" if nothing is.
func (f field) problem() string {
	v := strings.TrimSpace(f.value)
	switch {
	case v == "" && f.optional:
		return ""
	case v == "":
		return "Type the " + f.name + " to continue."
	case f.check != nil:
		return f.check(v)
	}
	return ""
}

// form is a run of questions asked one at a time. focus is the one showing.
type form struct {
	fields []field
	focus  int
	reveal bool // secret answers are shown as typed
}

// key edits the current answer and reports whether enter was pressed.
func (f *form) key(k tea.KeyMsg) bool {
	if len(f.fields) == 0 {
		return k.Type == tea.KeyEnter
	}
	// Models are values, so copy before editing: an earlier copy of the
	// model mustn't see this one's typing.
	f.fields = slices.Clone(f.fields)
	cur := &f.fields[f.focus]
	switch k.Type {
	case tea.KeyEnter:
		return true
	case tea.KeyBackspace:
		if r := []rune(cur.value); len(r) > 0 {
			cur.value = string(r[:len(r)-1])
		}
	case tea.KeyCtrlU:
		cur.value = ""
	case tea.KeyCtrlW:
		v := strings.TrimRight(cur.value, " ")
		cur.value = v[:strings.LastIndex(v, " ")+1]
	case tea.KeySpace:
		cur.value += " "
	case tea.KeyRunes:
		// Pastes arrive here too, newlines and all.
		cur.value += strings.Map(func(r rune) rune {
			if r == '\n' || r == '\r' || r == '\t' {
				return ' '
			}
			if r < 0x20 {
				return -1
			}
			return r
		}, string(k.Runes))
	}
	return false
}

func (f *form) values() []string {
	out := make([]string, len(f.fields))
	for i, fl := range f.fields {
		out[i] = strings.TrimSpace(fl.value)
	}
	return out
}

func (m setupModel) openDetails(i int) setupModel {
	m.prov, m.provCur = i, i
	p := providers[i]
	fields := slices.Clone(p.fields)
	if matchProvider(m.cfg.Storage) == i && m.cfg.Storage.Backend != "" {
		for j, v := range p.read(m.cfg.Storage) {
			fields[j].value = v
		}
	}
	m.details = form{fields: fields}
	return m
}
