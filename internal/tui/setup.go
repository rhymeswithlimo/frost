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
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/rhymeswithlimo/frost/internal/config"
	"github.com/rhymeswithlimo/frost/internal/crypto"
	"github.com/rhymeswithlimo/frost/internal/storage/permafrost"
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
	// Elsewhere, if set, names where this machine's backups are when that
	// isn't s, so setup can say it's starting a separate set. "" otherwise.
	Elsewhere func(s config.Storage) string
	// Checkout opens the page for getting a Permafrost key. page is its
	// address, to show in case the browser didn't open. wait blocks until
	// the access key comes back and is saved, or ctx is cancelled. It
	// returns the key whenever there is one, even with an error saying it
	// couldn't be saved.
	Checkout func(ctx context.Context, s config.Storage) (page string, wait func() (string, error), err error)
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
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
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
	stPermaChoice // Permafrost: have a key, or get one
	stCheckout    // waiting on the browser
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
	case stStorage, stPermaChoice, stCheckout, stDetails:
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
	quit bool   // asking whether to quit
	err  string // shown on the current screen until the next key press
	note string // the same, for news that isn't a problem

	// storage
	provCur   int  // cursor in the provider list
	prov      int  // the provider being asked about
	details   form // its questions, one at a time
	pending   config.Storage
	connected bool
	state     RepoState
	elsewhere string // where this machine's backups are, if not in the storage just connected
	autoTried bool   // a saved config gets one silent connect

	// Permafrost without a key: getting one in the browser
	permaCur int // 0 has a key, 1 doesn't
	co       checkoutRun

	// folders and skip: the input box has focus while the selection is -1
	folderIn  form
	folderSel int
	skipIn    form
	skipSel   int

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
	m := setupModel{ctx: ctx, deps: deps, cfg: cfg, existing: existing, spin: sp, visited: map[setupStep]bool{}, folderSel: -1, skipSel: -1}
	if existing {
		m.visited[stFolders] = true
		m.visited[stSkip] = true
		m.visited[stSchedule] = true
	}
	m.provCur = matchProvider(cfg.Storage)
	m.schedCur = max(slices.Index(m.schedOptions(), m.schedValue()), 0)
	m.folderIn = form{fields: []field{{placeholder: "type a path, like ~/Pictures"}}}
	m.skipIn = form{fields: []field{{placeholder: "type a name or pattern, like *.iso"}}}
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

// checkoutRun is the checkout in progress, or the last one.
type checkoutRun struct {
	id      int // results from an earlier run are ignored
	page    string
	until   time.Time
	cancel  context.CancelFunc
	waiting bool
	failed  string
}

type checkoutStartedMsg struct {
	id     int
	page   string
	wait   func() (string, error)
	cancel context.CancelFunc
	err    error
}

type checkoutMsg struct {
	id    int
	token string
	err   error
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
	case !m.visited[stSkip]:
		m.skipSel = -1
		return m.goTo(stSkip), nil
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
		m.w, m.h = max(msg.Width, 0), max(msg.Height, 0)
		return m, nil

	case spinner.TickMsg:
		if m.busy == "" && !m.co.waiting {
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
				p := matchProvider(m.pending)
				m = m.openDetails(p)
				if p == 0 { // so esc offers getting a key
					m.back, m.step = append(m.back, m.step), stPermaChoice
				}
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
		m.elsewhere = ""
		if msg.state == RepoNew && m.deps.Elsewhere != nil {
			m.elsewhere = m.deps.Elsewhere(m.pending)
		}
		return m.advance()

	case unlockMsg:
		m.busy = ""
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.key, m.newRepo, m.keyReady = msg.key, false, true
		return m.advance()

	case checkoutStartedMsg:
		if msg.id != m.co.id { // cancelled while the browser was opening
			msg.cancel()
			if wait := msg.wait; wait != nil {
				return m, func() tea.Msg { wait(); return nil } // closes the listener
			}
			return m, nil
		}
		if msg.err == nil && msg.wait == nil {
			msg.err = errors.New("checkout didn't start")
		}
		if msg.err != nil {
			msg.cancel()
			m.co.waiting, m.co.failed, m.co.cancel = false, msg.err.Error(), nil
			return m, nil
		}
		m.co.page, m.co.cancel = msg.page, msg.cancel
		id, wait := msg.id, msg.wait
		return m, func() tea.Msg {
			token, err := wait()
			return checkoutMsg{id, token, err}
		}

	case checkoutMsg:
		if msg.id != m.co.id || !m.co.waiting {
			return m, nil
		}
		m.co.waiting = false
		if msg.token == "" {
			m.co.failed = "checkout didn't return an access key"
			if msg.err != nil {
				m.co.failed = msg.err.Error()
			}
			if errors.Is(msg.err, context.Canceled) {
				m.co.failed = ""
			}
			return m, nil
		}
		if msg.err != nil {
			m.note = msg.err.Error()
		}
		m = m.pasteKey(msg.token)
		st := m.cfg.Storage
		providers[0].apply([]string{msg.token}, &st)
		return m.connect(st)

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
	case stDetails, stCheck, stUnlock:
		return true
	case stFolders:
		return m.folderSel < 0
	case stSkip:
		return m.skipSel < 0
	}
	return false
}

func (m setupModel) onKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	if m.quit {
		switch s {
		case "y":
			return m, tea.Quit
		case "n", "esc":
			m.quit = false
		}
		return m, nil
	}
	m.err, m.note = "", "" // a message lasts until the next key press
	if !m.typing() && s == "q" && m.step != stDone {
		if m.step == stWelcome { // nothing's been asked yet
			return m, tea.Quit
		}
		m.quit = true
		return m, nil
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
			if m.provCur == 0 {
				m.permaCur = 0
				return m.goTo(stPermaChoice), nil
			}
			m = m.openDetails(m.provCur)
			return m.goTo(stDetails), nil
		case "esc":
			return m.goBack(), nil
		default:
			if len(s) == 1 {
				if n := int(s[0]) - '1'; n >= 0 && n < len(providers) {
					m.provCur = n
				}
			}
		}

	case stPermaChoice:
		switch s {
		case "up", "k", "1":
			m.permaCur = 0
		case "down", "j", "2":
			m.permaCur = 1
		case "esc":
			return m.goBack(), nil
		case "enter":
			if m.permaCur == 1 {
				m = m.goTo(stCheckout)
				return m.startCheckout()
			}
			m = m.openDetails(0)
			return m.goTo(stDetails), nil
		}

	case stCheckout:
		switch {
		case s == "p":
			return m.pasteKey(""), nil
		case s == "r" && !m.co.waiting:
			return m.startCheckout()
		case s == "esc":
			m = m.stopCheckout()
			return m.goBack(), nil
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
		return m.skipKey(k)

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
				{question: "Let's check, what's word " + strconv.Itoa(i+1) + "?"},
				{question: "What about word " + strconv.Itoa(j+1) + "?"},
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
				m.visited[stSkip], m.skipSel = false, -1
				return m.goTo(stSkip), nil
			case stSchedule:
				m.visited[stSchedule] = false
				return m.goTo(stSchedule), nil
			}
		case "s": // not enter, so it can't happen by accident
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

// startCheckout opens the browser and waits for it to come back, on the
// checkout screen.
func (m setupModel) startCheckout() (tea.Model, tea.Cmd) {
	m = m.stopCheckout()
	id := m.co.id + 1
	m.co = checkoutRun{id: id, waiting: true, until: time.Now().Add(permafrost.CheckoutTimeout)}
	ctx, cancel := context.WithCancel(m.ctx)
	m.co.cancel = cancel // cancellation must work while Checkout is still opening
	fn, st := m.deps.Checkout, m.cfg.Storage
	return m, tea.Batch(m.spin.Tick, func() tea.Msg {
		page, wait, err := fn(ctx, st)
		if wait != nil {
			// Cleanup cannot depend on the program receiving this message:
			// it may quit while the browser is opening. Run Wait only once,
			// even if cancellation races the normal response command.
			once := sync.OnceValues(wait)
			stop := context.AfterFunc(ctx, func() { once() })
			wait = func() (string, error) {
				defer stop()
				defer cancel()
				return once()
			}
		}
		return checkoutStartedMsg{id, page, wait, cancel, err}
	})
}

// stopCheckout gives up on a checkout that's still waiting.
func (m setupModel) stopCheckout() setupModel {
	if m.co.cancel != nil {
		m.co.cancel()
	}
	m.co.id++
	m.co.waiting, m.co.cancel = false, nil
	return m
}

// pasteKey moves from getting a key to the question that asks for one,
// filled in with token. esc from there goes back to the have-a-key choice.
func (m setupModel) pasteKey(token string) setupModel {
	m = m.stopCheckout()
	m = m.openDetails(0)
	m.details.fields[0].value = token
	for i := len(m.back) - 1; i >= 0; i-- {
		if m.back[i] == stPermaChoice {
			m.back = m.back[:i+1]
			break
		}
	}
	m.step, m.permaCur = stDetails, 1
	return m
}

// foldersKey: the input box adds a folder, and up moves into the list
// above it to pick one to remove.
func (m setupModel) foldersKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	if s == "esc" && m.folderSel < 0 {
		return m.goBack(), nil
	}
	if m.folderSel >= 0 {
		m.cfg.Paths = listKey(s, &m.folderSel, m.cfg.Paths)
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

// skipKey works like foldersKey, for patterns: the box adds one (or a
// comma separated few), and up moves into the list to remove one.
func (m setupModel) skipKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	if s == "esc" && m.skipSel < 0 {
		return m.goBack(), nil
	}
	if m.skipSel >= 0 {
		m.cfg.Exclude = listKey(s, &m.skipSel, m.cfg.Exclude)
		return m, nil
	}
	if s == "up" && len(m.cfg.Exclude) > 0 {
		m.skipSel = len(m.cfg.Exclude) - 1
		return m, nil
	}
	if !m.skipIn.key(k) {
		return m, nil
	}
	typed := splitList(m.skipIn.values()[0])
	if len(typed) == 0 {
		m.visited[stSkip] = true
		return m.advance()
	}
	for _, p := range typed {
		if _, err := path.Match(p, ""); err != nil {
			m.err = fmt.Sprintf("%q isn't a valid pattern. Check its brackets.", p)
			return m, nil
		}
	}
	list := slices.Clone(m.cfg.Exclude)
	for _, p := range typed {
		if !slices.Contains(list, p) {
			list = append(list, p)
		}
	}
	m.cfg.Exclude = list
	m.skipIn.fields = []field{{placeholder: m.skipIn.fields[0].placeholder}}
	return m, nil
}

// listKey handles a key while an item in a list is selected: move, remove,
// or go back to the box. sel becomes -1 when the box has focus again.
func listKey(s string, sel *int, items []string) []string {
	switch s {
	case "esc", "enter":
		*sel = -1
	case "up", "k":
		*sel = max(*sel-1, 0)
	case "down", "j":
		if *sel++; *sel >= len(items) {
			*sel = -1
		}
	case "x", "backspace", "delete":
		items = slices.Delete(slices.Clone(items), *sel, *sel+1)
		if *sel >= len(items) {
			*sel = len(items) - 1
		}
	}
	return items
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
	back                               int                 // runes after the text cursor, so 0 is the end
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
	r := []rune(cur.value)
	at := cur.cursor()
	before, after := r[:at], r[at:]
	set := func(before, after []rune) {
		cur.value = string(before) + string(after)
		cur.back = len(after)
	}
	// Alt with an arrow, b or f jumps a word, like option+arrow on a Mac.
	if k.Alt {
		switch {
		case k.Type == tea.KeyLeft || k.String() == "alt+b":
			cur.back = len(r) - wordStart(r, at)
		case k.Type == tea.KeyRight || k.String() == "alt+f":
			cur.back = len(r) - wordEnd(r, at)
		case k.Type == tea.KeyBackspace:
			set(before[:wordStart(r, at)], after)
		case k.Type == tea.KeyEnter:
			return true
		}
		return false
	}
	switch k.Type {
	case tea.KeyEnter:
		return true
	case tea.KeyLeft, tea.KeyCtrlB:
		cur.back = min(cur.back+1, len(r))
	case tea.KeyRight, tea.KeyCtrlF:
		cur.back = max(cur.back-1, 0)
	case tea.KeyHome, tea.KeyCtrlA:
		cur.back = len(r)
	case tea.KeyEnd, tea.KeyCtrlE:
		cur.back = 0
	case tea.KeyCtrlLeft:
		cur.back = len(r) - wordStart(r, at)
	case tea.KeyCtrlRight:
		cur.back = len(r) - wordEnd(r, at)
	case tea.KeyBackspace:
		if len(before) > 0 {
			set(before[:len(before)-1], after)
		}
	case tea.KeyDelete:
		if len(after) > 0 {
			set(before, after[1:])
		}
	case tea.KeyCtrlU:
		set(nil, after)
	case tea.KeyCtrlK:
		set(before, nil)
	case tea.KeyCtrlW:
		set(before[:wordStart(r, at)], after)
	case tea.KeySpace:
		set(append(slices.Clone(before), ' '), after)
	case tea.KeyRunes:
		// Pastes arrive here too, newlines and all.
		typed := strings.Map(func(r rune) rune {
			if r == '\n' || r == '\r' || r == '\t' {
				return ' '
			}
			if r < 0x20 {
				return -1
			}
			return r
		}, string(k.Runes))
		set(append(slices.Clone(before), []rune(typed)...), after)
	}
	return false
}

// cursor is where the text cursor is, in runes from the start.
func (f field) cursor() int {
	n := len([]rune(f.value))
	return n - min(max(f.back, 0), n)
}

// wordStart is the start of the word before i, skipping spaces first.
func wordStart(r []rune, i int) int {
	for i > 0 && r[i-1] == ' ' {
		i--
	}
	for i > 0 && r[i-1] != ' ' {
		i--
	}
	return i
}

// wordEnd is the end of the word after i, skipping spaces first.
func wordEnd(r []rune, i int) int {
	for i < len(r) && r[i] == ' ' {
		i++
	}
	for i < len(r) && r[i] != ' ' {
		i++
	}
	return i
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
	if i == 0 && fields[0].value == "" { // a key from earlier, while on other storage
		fields[0].value = m.cfg.Storage.Permafrost.Token
	}
	m.details = form{fields: fields}
	return m
}
