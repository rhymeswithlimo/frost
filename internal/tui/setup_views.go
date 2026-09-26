package tui

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"

	"github.com/rhymeswithlimo/frost/internal/theme"
)

// The setup screens sit in a card no bigger than this, centred in the
// window, so they stay compact on a big terminal. The welcome gets a bit
// more height, for air around the wordmark.
const (
	cardW, cardH = 80, 26
	welcomeH     = 30
	minW, minH   = 56, 18
	columnW      = 60 // the question column inside the card
)

func (m setupModel) View() string {
	if m.w == 0 {
		return ""
	}
	if m.w < minW || m.h < minH {
		msg := theme.Text.Render(fmt.Sprintf("make the window at least %dx%d", minW, minH))
		return clip(lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center, msg,
			lipgloss.WithWhitespaceBackground(theme.Bg)), m.w, m.h)
	}

	w := min(cardW, m.w-2*theme.PadX)
	h := min(cardH, m.h-2*theme.PadY)
	bodyH := h - 5                                  // header, gap, gap, rule, hints
	bare := m.step == stWelcome || m.step == stDone // no header
	if bare {
		h = min(welcomeH, m.h-2*theme.PadY)
		bodyH = h - 2
	}
	var body string
	switch {
	case m.busy != "":
		body = lipgloss.Place(w, bodyH, lipgloss.Center, lipgloss.Center,
			theme.Bold.Render(m.spin.View())+theme.Text.Render(" "+m.busy+"..."),
			lipgloss.WithWhitespaceBackground(theme.Bg))
	case m.step == stWelcome:
		body = m.viewWelcome(w, bodyH)
	case m.step == stDone:
		body = m.viewDone(w, bodyH)
	default:
		body = m.render(m.page(w), w, bodyH)
	}
	body = clip(body, w, bodyH)
	body = lipgloss.Place(w, bodyH, lipgloss.Left, lipgloss.Top, body, lipgloss.WithWhitespaceBackground(theme.Bg))

	rule := theme.Faded.Render(strings.Repeat("─", w))
	hints := pad(fitHints(m.setupHints(), w), w)
	page := stack(body, rule, hints)
	if !bare {
		page = stack(m.setupHeader(w), fill(w, 1), body, fill(w, 1), rule, hints)
	}
	page = lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center, page, lipgloss.WithWhitespaceBackground(theme.Bg))
	return clip(page, m.w, m.h)
}

// setupHeader is the title on the left and the progress bar on the right.
func (m setupModel) setupHeader(w int) string {
	left := theme.Title.Render("FROST") + theme.Dim.Render("  setup")
	right := ""
	if n := stepOf(m.step); n > 0 {
		segs := make([]string, setupSteps)
		for i := range segs {
			st := theme.Faded
			if i < n {
				st = theme.Text
			}
			segs[i] = st.Render("━━━")
		}
		right = theme.Dim.Render(stepNames[n]+"  ") + strings.Join(segs, theme.Base.Render(" "))
	}
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return pad(left, w)
	}
	return left + fill(gap, 1) + right
}

func (m setupModel) setupHints() []string {
	h := theme.Hint
	if m.busy != "" {
		return []string{h("ctrl+c", "quit")}
	}
	quit, ctrlQuit := h("q", "quit"), h("ctrl+c", "quit")
	switch m.step {
	case stWelcome:
		if m.existing {
			return []string{h("enter", "review settings"), quit}
		}
		return []string{h("enter", "start"), quit}
	case stStorage, stSchedule:
		return []string{h("enter", "next"), h("↑↓", "choose"), h("esc", "back"), quit}
	case stDetails:
		enter := "next"
		if m.details.focus == len(m.details.fields)-1 {
			enter = "connect"
		}
		hints := []string{h("enter", enter)}
		if m.details.fields[m.details.focus].secret {
			show := "show"
			if m.details.reveal {
				show = "hide"
			}
			hints = append(hints, h("tab", show))
		}
		return append(hints, h("esc", "back"), ctrlQuit)
	case stFolders:
		if m.folderSel >= 0 {
			return []string{h("x", "remove"), h("↑↓", "choose"), h("esc", "done"), quit}
		}
		enter := "continue"
		if m.folderIn.values()[0] != "" || len(m.cfg.Paths) == 0 {
			enter = "add"
		}
		hints := []string{h("enter", enter)}
		if len(m.cfg.Paths) > 0 {
			hints = append(hints, h("↑", "remove one"))
		}
		return append(hints, h("esc", "back"), ctrlQuit)
	case stSkip:
		return []string{h("enter", "save"), h("esc", "back"), ctrlQuit}
	case stPhrase:
		show := "show words"
		if m.showWords {
			show = "hide words"
		}
		return []string{h("v", show), h("enter", "I've written them down"), h("esc", "back"), quit}
	case stCheck:
		enter := "next"
		if m.check.focus == 1 {
			enter = "check"
		}
		return []string{h("enter", enter), h("esc", "see the words"), ctrlQuit}
	case stUnlock:
		show := "show phrase"
		if m.phrase.reveal {
			show = "hide phrase"
		}
		return []string{h("enter", "unlock"), h("tab", show), h("esc", "back"), ctrlQuit}
	case stReview:
		key := "show key"
		if m.showKey {
			key = "hide key"
		}
		return []string{h("enter", "save"), h("e", "edit"), h("↑↓", "choose"), h("v", key), quit}
	case stDone:
		return []string{h("enter", "exit")}
	}
	return nil
}

// fitHints joins hints to fit w. The last one (quit, usually) always stays;
// when space runs out the ones before it go first, from the end.
func fitHints(hints []string, w int) string {
	sep := theme.Base.Render("   ")
	for len(hints) > 1 {
		if s := strings.Join(hints, sep); lipgloss.Width(s) <= w {
			return s
		}
		hints = slices.Delete(slices.Clone(hints), len(hints)-2, len(hints)-1)
	}
	return joinFit(hints, w)
}

// ---- the page template ----

// page is the one layout every screen after the welcome uses: a question,
// a line of context, the thing to answer with, and a line of help.
type page struct {
	over     string // faded, above the question: where you are inside a step
	question string
	good     bool   // the question is a success message
	sub      string // context under the question
	body     []string
	help     string   // dim, under the body
	foot     string   // faded, at the bottom
	extra    []string // styled lines at the bottom
}

// render lays out a page in a column centred in the card. The question sits
// on the same row on every screen, so moving through them reads as a flow.
func (m setupModel) render(p page, w, h int) string {
	cw := min(w, columnW)
	var lines []string
	if h >= 16 {
		lines = append(lines, fill(cw, min(2, h-16+1)))
	}
	if p.over != "" {
		lines = append(lines, theme.Faded.Render(p.over))
	}
	q := theme.Bold
	if p.good {
		q = theme.Good.Bold(true)
	}
	lines = append(lines, para(q, p.question, cw))
	if p.sub != "" {
		lines = append(lines, para(theme.Dim, p.sub, cw))
	}
	lines = append(lines, blank(cw))
	lines = append(lines, p.body...)
	if p.help != "" {
		lines = append(lines, blank(cw), para(theme.Dim, p.help, cw))
	}
	if m.err != "" {
		lines = append(lines, blank(cw), para(theme.Error, sentence(m.err), cw))
	}
	if m.note != "" {
		lines = append(lines, blank(cw), para(theme.Text, sentence(m.note), cw))
	}
	if p.foot != "" {
		lines = append(lines, blank(cw), para(theme.Faded, p.foot, cw))
	}
	if len(p.extra) > 0 {
		lines = append(append(lines, blank(cw)), p.extra...)
	}
	for i, l := range lines {
		lines[i] = pad(l, cw)
	}
	return lipgloss.PlaceHorizontal(w, lipgloss.Center, stack(lines...), lipgloss.WithWhitespaceBackground(theme.Bg))
}

func (m setupModel) page(w int) page {
	cw := min(w, columnW)
	switch m.step {
	case stStorage:
		var rows []string
		for i, p := range providers {
			rows = append(rows, choice(i == m.provCur, p.name, p.note, 21, cw))
		}
		return page{
			question: "Where should backups go?",
			sub:      "Everything is encrypted before it leaves this machine, so the storage only ever sees scrambled data.",
			body:     rows,
		}

	case stDetails:
		p, d := providers[m.prov], m.details
		f := d.fields[d.focus]
		over := p.name
		if n := len(d.fields); n > 1 {
			over += fmt.Sprintf("  %d of %d", d.focus+1, n)
		}
		pg := page{over: over, question: f.question, body: []string{inputBox(f, d.reveal, true, cw)}, help: f.help}
		if m.prov == 0 {
			link := theme.Text.Bold(true).Underline(true)
			pg.extra = []string{
				theme.Text.Render("Don't have a key yet? Head to ") + link.Render(permafrostLink) + theme.Text.Render(" to learn more."),
			}
		}
		return pg

	case stFolders:
		var rows []string
		missing := false
		for i, p := range m.cfg.Paths {
			note := ""
			if m.deps.DirExists != nil && !m.deps.DirExists(p) {
				note, missing = "  not found", true
			}
			if i == m.folderSel {
				rows = append(rows, theme.Selected.Render(padPlain(truncate(" "+p+note, cw), cw)))
			} else {
				rows = append(rows, pad(theme.Text.Render(" "+truncate(p, cw-14))+theme.Caution.Render(note), cw))
			}
		}
		if len(rows) > 0 {
			rows = append(rows, blank(cw))
		}
		rows = append(rows, inputBox(m.folderIn.fields[0], false, m.folderSel < 0, cw))
		pg := page{
			question: "Which folders should frost back up?",
			sub:      "Whole folders, with everything inside them.",
			body:     rows,
			help:     "Type a path and press enter to add it. Press enter on an empty box to move on.",
		}
		if len(m.cfg.Paths) == 0 {
			pg.help = "Type a path and press enter to add it. Add as many as you like."
		}
		if missing {
			pg.foot = "A folder that isn't there is skipped until it is, like a drive that isn't plugged in."
		}
		return pg

	case stSkip:
		return page{
			question: "Which files should frost skip?",
			sub:      "Names or patterns, comma separated. * matches anything.",
			body:     []string{inputBox(m.skipIn.fields[0], false, true, cw)},
		}

	case stSchedule:
		var rows []string
		for i, o := range m.schedOptions() {
			rows = append(rows, choice(i == m.schedCur, schedLabel(o), "", 40, cw))
		}
		pg := page{
			question: "How often should frost back up?",
			sub:      "Backups run in the background and only upload what changed.",
			body:     rows,
		}
		if m.deps.Scheduler != "" {
			pg.foot = "It's set up as a " + m.deps.Scheduler + " job. Change it any time by running frost init again."
		}
		return pg

	case stPhrase:
		return page{
			question: "Write down your recovery phrase.",
			sub:      "It's the only way to get your files back if this machine is lost, and nobody can recover it for you.",
			body:     []string{m.phraseCard(cw)},
			help:     "On paper, somewhere safe. Make sure nobody can see your screen.",
		}

	case stCheck:
		c := m.check
		return page{
			over:     fmt.Sprintf("Checking your copy  %d of 2", c.focus+1),
			question: c.fields[c.focus].question,
			body:     []string{inputBox(c.fields[c.focus], false, true, cw)},
			help:     "Type it from what you wrote down.",
		}

	case stUnlock:
		q, sub := "This storage already has backups.", "Type the recovery phrase you saved when you first set them up."
		if m.state == RepoLocalWrong {
			q, sub = "These backups use a different key.", "The key on this machine doesn't open them. Type their recovery phrase, and frost will use that key here instead."
		}
		return page{
			question: q,
			sub:      sub,
			body:     []string{inputBox(m.phrase.fields[0], m.phrase.reveal, true, cw)},
			help:     "All 24 words, separated by spaces.",
		}

	case stReview:
		var rows []string
		for i, r := range m.reviewRows() {
			if i == m.revCur {
				rows = append(rows, theme.Selected.Render(padPlain(" "+padPlain(r.label, 10)+truncate(r.value, cw-12), cw)))
			} else {
				rows = append(rows, pad(theme.Dim.Render(" "+padPlain(r.label, 10))+theme.Text.Render(truncate(r.value, cw-12)), cw))
			}
		}
		if m.key != nil {
			fp := m.key.Fingerprint()
			shown := theme.Redacted.Render(strings.Repeat(" ", len(fp)))
			if m.showKey {
				shown = theme.Text.Render(fp)
			}
			from := "new"
			switch {
			case m.key == m.deps.LocalKey:
				from = "already on this machine"
			case !m.newRepo:
				from = "from your recovery phrase"
			}
			rows = append(rows, pad(theme.Dim.Render(" "+padPlain("key", 10))+shown+theme.Dim.Render("  "+from), cw))
		}
		return page{
			question: "Ready to save?",
			sub:      "Choose a line and press [e] to change it.",
			body:     rows,
		}
	}
	return page{}
}

// ---- building blocks ----

// para wraps s to w cells in style st, on the background.
func para(st lipgloss.Style, s string, w int) string {
	return solid(st.Width(w).Render(s))
}

func blank(w int) string { return fill(w, 1) }

// sentence starts s with a capital and ends it with a full stop.
func sentence(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) == 0 {
		return ""
	}
	r[0] = unicode.ToUpper(r[0])
	if !strings.ContainsRune(".?!", r[len(r)-1]) {
		r = append(r, '.')
	}
	return string(r)
}

// inputBox is the big answer box: a bordered line with a block cursor.
func inputBox(f field, reveal, focused bool, w int) string {
	inner := w - 4 // border and padding
	val := printable(f.value)
	if f.secret && !reveal {
		val = strings.Repeat("•", min(len([]rune(f.value)), inner))
	}
	cursor := ""
	if focused {
		cursor = theme.Text.Render("█")
	}
	var content string
	if val == "" {
		content = cursor + theme.Faded.Render(truncate(f.placeholder, inner-1))
	} else {
		content = theme.Text.Render(truncateLeft(val, inner-1)) + cursor
	}
	return theme.Box(focused).Width(w - 2).Render(pad(content, inner))
}

// choice is one row of a pick-one list: an inverted bar when chosen.
func choice(on bool, name, note string, nameW, w int) string {
	if on {
		return theme.Selected.Render(padPlain(truncate(" (•) "+padPlain(name, nameW)+note, w), w))
	}
	return pad(theme.Text.Render(" ( ) "+padPlain(name, nameW))+theme.Dim.Render(note), w)
}

// phraseCard is the recovery phrase in a bordered card, the words covered
// until [v].
func (m setupModel) phraseCard(w int) string {
	words := strings.Fields(m.key.Phrase())
	cols := 4
	if w-4 < 4*14 {
		cols = 3
	}
	rows := (len(words) + cols - 1) / cols
	var out []string
	for r := range rows {
		line := ""
		for c := range cols {
			i := c*rows + r
			if i >= len(words) {
				break
			}
			word := theme.Text.Render(padPlain(words[i], 8))
			if !m.showWords {
				word = theme.Redacted.Render(strings.Repeat(" ", 8))
			}
			line += theme.Dim.Render(fmt.Sprintf("%2d ", i+1)) + word + theme.Base.Render("   ")
		}
		out = append(out, pad(line, w-4))
	}
	return theme.Box(true).Width(w - 2).Render(stack(out...))
}

// ---- the welcome ----

func (m setupModel) viewWelcome(w, h int) string {
	row := func(s string) string {
		return lipgloss.PlaceHorizontal(w, lipgloss.Center, s, lipgloss.WithWhitespaceBackground(theme.Bg))
	}
	center := func(st lipgloss.Style, s string, tw int) string { return row(para(st.Align(lipgloss.Center), s, tw)) }
	tw := min(w, columnW)
	var text []string
	if m.existing {
		text = append(text, center(theme.Text, "frost is already set up on this machine. Review your settings, change any of them, and save.", tw), blank(w))
		for _, r := range m.reviewRows() {
			v := truncate(r.value, tw-10)
			text = append(text, row(pad(theme.Dim.Render(padPlain(r.label, 10))+theme.Text.Render(v), tw)))
		}
	} else {
		text = append(text,
			center(theme.Text, "frost backs up your folders, encrypted on this machine before anything leaves it.", tw),
			blank(w),
			center(theme.Dim, "Setup takes about two minutes.", tw),
		)
	}
	rest := stack(text...)
	// "Welcome to" and a blank line, the wordmark, a gap, then the text.
	markH := lipgloss.Height(strings.TrimRight(wordmark, "\n"))
	gap := 3
	if 2+markH+gap+lipgloss.Height(rest) > h {
		gap = 1
	}
	mark := logo(w, h-lipgloss.Height(rest)-gap-2)
	block := stack(row(theme.Text.Render("Welcome to")), blank(w), row(mark), fill(w, gap), rest)
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, block, lipgloss.WithWhitespaceBackground(theme.Bg))
}

// viewDone is the last screen: short, centred, and pointing at what to run
// next.
func (m setupModel) viewDone(w, h int) string {
	row := func(s string) string {
		return lipgloss.PlaceHorizontal(w, lipgloss.Center, s, lipgloss.WithWhitespaceBackground(theme.Bg))
	}
	when := "Your folders will be backed up " + strings.ToLower(schedLabel(m.schedValue())) + "."
	if !m.cfg.Schedule.Enabled {
		when = "Automatic backups are off, so run a backup whenever you like."
	}
	whenStyle := theme.Text
	for _, r := range m.savedRows {
		if r[0] == "schedule" && strings.HasPrefix(r[1], "not installed") {
			when = "Automatic backups couldn't be set up (" + strings.TrimPrefix(r[1], "not installed: ") + "). Run frost init again to retry."
			whenStyle = theme.Caution
		}
	}
	cmds := [][2]string{
		{"frost backup", "back up now"},
		{"frost browse", "look through your backups"},
		{"frost status", "check everything's healthy"},
	}
	var list []string
	for _, c := range cmds {
		list = append(list, theme.Bold.Render(padPlain(c[0], 16))+theme.Dim.Render(c[1]))
	}
	block := stack(
		row(theme.Good.Bold(true).Render("All set up!")),
		blank(w),
		row(para(whenStyle.Align(lipgloss.Center), when, min(w, columnW))),
		fill(w, 2),
		row(theme.Dim.Render("Exit, then run one of these to get started:")),
		blank(w),
		row(stack(list...)),
	)
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, block, lipgloss.WithWhitespaceBackground(theme.Bg))
}
