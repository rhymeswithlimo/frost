package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

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
	if m.w <= 0 || m.h <= 0 {
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
	case m.quit:
		body = viewQuit(w, bodyH)
	case m.step == stWelcome:
		body = m.viewWelcome(w, bodyH)
	case m.step == stDone:
		body = m.viewDone(w, bodyH)
	default:
		body = m.render(m.page(w, bodyH), w, bodyH)
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
	if m.quit {
		return nil // the choices are on the screen
	}
	quit := h("q", "quit")
	switch m.step {
	case stWelcome:
		if m.existing {
			return []string{h("enter", "review settings"), quit}
		}
		return []string{h("enter", "start"), quit}
	case stStorage, stSchedule, stPermaChoice:
		return []string{h("enter", "next"), h("↑↓", "choose"), h("esc", "back"), quit}
	case stCheckout:
		if m.co.waiting {
			return []string{h("p", "paste a key instead"), h("esc", "cancel"), quit}
		}
		return []string{h("r", "try again"), h("p", "paste a key"), h("esc", "back"), quit}
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
		return append(hints, h("esc", "back"))
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
		return append(hints, h("esc", "back"))
	case stSkip:
		if m.skipSel >= 0 {
			return []string{h("x", "remove"), h("↑↓", "choose"), h("esc", "done"), quit}
		}
		enter := "continue"
		if m.skipIn.values()[0] != "" {
			enter = "add"
		}
		hints := []string{h("enter", enter)}
		if len(m.cfg.Exclude) > 0 {
			hints = append(hints, h("↑", "remove one"))
		}
		return append(hints, h("esc", "back"))
	case stPhrase:
		show := "show words"
		if m.showWords {
			show = "hide words"
		}
		return []string{h("v", show), h("enter", "words saved"), h("esc", "back"), quit}
	case stCheck:
		enter := "next"
		if m.check.focus == 1 {
			enter = "check"
		}
		return []string{h("enter", enter), h("esc", "see the words")}
	case stUnlock:
		show := "show phrase"
		if m.phrase.reveal {
			show = "hide phrase"
		}
		return []string{h("enter", "unlock"), h("tab", show), h("esc", "back")}
	case stReview:
		key := "show key"
		if m.showKey {
			key = "hide key"
		}
		return []string{h("s", "save and finalise"), h("e", "edit"), h("↑↓", "choose"), h("v", key), quit}
	case stDone:
		return []string{h("enter", "exit")}
	}
	return nil
}

// hintSep is the space between footer hints.
const hintSep = "   "

// fitHints joins hints to fit w. The last one (quit, usually) always stays;
// when space runs out the spacing tightens, then the ones before it go,
// from the end.
func fitHints(hints []string, w int) string {
	for len(hints) > 1 {
		for _, sep := range []string{hintSep, "  ", " "} {
			if s := strings.Join(hints, theme.Base.Render(sep)); lipgloss.Width(s) <= w {
				return s
			}
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
	if p.over != "" {
		lines = append(lines, theme.Dim.Render(p.over))
	}
	q := theme.Bold
	if p.good {
		q = theme.Good.Bold(true)
	}
	lines = append(lines, para(q, p.question, cw))
	if p.sub != "" {
		lines = append(lines, para(theme.Dim, p.sub, cw))
	}
	// Fit the question, answer and feedback first. Explanatory text and
	// breathing room use the space left over, never the answer's rows.
	answer := stack(p.body...)
	feedback := []string{}
	if m.err != "" {
		feedback = append(feedback, para(theme.Error, sentence(m.err), cw))
	}
	if m.note != "" {
		feedback = append(feedback, para(theme.Text, sentence(m.note), cw))
	}
	used := lipgloss.Height(stack(lines...)) + lipgloss.Height(answer)
	for _, f := range feedback {
		used += lipgloss.Height(f)
	}
	if used < h {
		lines = append(lines, blank(cw))
		used++
	}
	lines = append(lines, answer)
	lines = append(lines, feedback...)
	for _, extra := range []string{p.help, p.foot} {
		if extra == "" {
			continue
		}
		block := para(theme.Dim, extra, cw)
		if used+1+lipgloss.Height(block) <= h {
			lines = append(lines, blank(cw), block)
			used += 1 + lipgloss.Height(block)
		}
	}
	if len(p.extra) > 0 && used+1+lipgloss.Height(stack(p.extra...)) <= h {
		lines = append(lines, blank(cw), stack(p.extra...))
		used += 1 + lipgloss.Height(stack(p.extra...))
	}
	if h >= 16 && used < h {
		lines = append([]string{fill(cw, min(2, h-used))}, lines...)
	}
	for i, l := range lines {
		lines[i] = pad(l, cw)
	}
	return lipgloss.PlaceHorizontal(w, lipgloss.Center, stack(lines...), lipgloss.WithWhitespaceBackground(theme.Bg))
}

func (m setupModel) page(w, h int) page {
	cw := min(w, columnW)
	switch m.step {
	case stStorage:
		var rows []string
		for i, p := range providers {
			rows = append(rows, choice(i == m.provCur, p.name, p.note, 21, cw))
		}
		return page{
			question: "Where should backups go?",
			sub:      "Choose your storage provider below.",
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
			pg.extra = []string{theme.Text.Render("No key yet? Press ") + theme.Bold.Render("[esc]") + theme.Text.Render(" to get one.")}
		}
		return pg

	case stPermaChoice:
		return page{
			over:     "Permafrost",
			question: "Do you have a Permafrost access key?",
			sub:      "It's the only thing frost needs to connect.",
			body: []string{
				choice(m.permaCur == 0, "I have a key", "", 26, cw),
				choice(m.permaCur == 1, "I don't have a key yet", "get one in your browser", 26, cw),
			},
		}

	case stCheckout:
		return m.checkoutPage(cw)

	case stFolders:
		return m.fitList(w, h, m.foldersPage)

	case stSkip:
		return m.fitList(w, h, m.skipPage)

	case stSchedule:
		var rows []string
		for i, o := range m.schedOptions() {
			rows = append(rows, choice(i == m.schedCur, schedLabel(o), "", 40, cw))
		}
		pg := page{
			question: "How often should frost back up?",
			sub:      "Backups run automatically in the background, only uploading new or modified files.",
			body:     rows,
		}
		pg.foot = "You can change this any time by running frost init again."
		return pg

	case stPhrase:
		return page{
			question: "Write down your recovery phrase.",
			sub:      "It's the only way to get your files back if this machine is ever lost.",
			body:     []string{m.phraseCard(cw)},
			help:     "Consider writing it down on paper and placing it somewhere secure. Make sure nobody but you has access.",
		}

	case stCheck:
		c := m.check
		return page{
			over:     fmt.Sprintf("Confirm you've saved your phrase - %d of 2", c.focus+1),
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
			question: "Review and finalise.",
			sub:      "Adjust your preferences below. When you're ready, press [s] to finish.",
			body:     rows,
		}
	}
	return page{}
}

// fitList builds a list page with as many rows showing as fit in h.
func (m setupModel) fitList(w, h int, build func(cw, n int) page) page {
	cw := min(w, columnW)
	for n := listMax; n > 1; n-- {
		if pg := build(cw, n); lipgloss.Height(m.render(pg, w, h)) <= h {
			return pg
		}
	}
	return build(cw, 1)
}

func (m setupModel) foldersPage(cw, n int) page {
	missing := false
	rows := listRows(m.cfg.Paths, m.folderSel, n, cw, func(i int, on bool) string {
		p, note := m.cfg.Paths[i], ""
		if m.deps.DirExists != nil && !m.deps.DirExists(p) {
			note, missing = "  not found", true
		}
		if on {
			return theme.Selected.Render(padPlain(truncate(" "+p+note, cw), cw))
		}
		return pad(theme.Text.Render(" "+truncate(p, cw-14))+theme.Caution.Render(note), cw)
	})
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
}

func (m setupModel) skipPage(cw, n int) page {
	rows := listRows(m.cfg.Exclude, m.skipSel, n, cw, func(i int, on bool) string {
		if on {
			return theme.Selected.Render(padPlain(truncate(" "+m.cfg.Exclude[i], cw), cw))
		}
		return pad(theme.Text.Render(" "+truncate(m.cfg.Exclude[i], cw-2)), cw)
	})
	if len(rows) > 0 {
		rows = append(rows, blank(cw))
	}
	rows = append(rows, inputBox(m.skipIn.fields[0], false, m.skipSel < 0, cw))
	help := "Type a name or pattern and press enter to add it. Press enter on an empty box to move on."
	if len(m.cfg.Exclude) == 0 {
		help = "Nothing is skipped yet. Type a name or pattern and press enter to add it."
	}
	return page{
		question: "Which files should frost skip?",
		sub:      "Names or patterns. * matches anything, like *.tmp.",
		body:     rows,
		help:     help,
	}
}

// ---- building blocks ----

// listMax is the most rows of a list that show at once.
const listMax = 6

// listRows is a list with one row selected (or none, at -1), in at most n
// lines. When it's longer it scrolls to keep the selection in view, or the
// end when there's none, with a line saying how many are hidden each way.
func listRows(items []string, sel, n, w int, row func(i int, on bool) string) []string {
	from, to := 0, len(items)
	if len(items) > n {
		show := max(n-2, 1) // room for the "more" lines
		at := sel
		if at < 0 {
			at = len(items) - 1
		}
		from = min(max(at-show/2, 0), len(items)-show)
		to = from + show
	}
	var out []string
	if from > 0 {
		out = append(out, pad(theme.Dim.Render(fmt.Sprintf(" ↑ %d more", from)), w))
	}
	for i := from; i < to; i++ {
		out = append(out, row(i, i == sel))
	}
	if more := len(items) - to; more > 0 {
		out = append(out, pad(theme.Dim.Render(fmt.Sprintf(" ↓ %d more", more)), w))
	}
	return out
}

// para wraps s to w cells in style st, on the background.
func para(st lipgloss.Style, s string, w int) string {
	return solid(st.Width(w).Render(printable(s)))
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
	var content string
	switch {
	case f.value == "" && focused:
		content = theme.Text.Render("█") + theme.Dim.Render(truncate(f.placeholder, inner-1))
	case f.value == "":
		content = theme.Dim.Render(truncate(f.placeholder, inner))
	default:
		content = inputText(f, reveal, focused, inner)
	}
	return theme.Box(focused).Width(w - 2).Render(pad(content, inner))
}

// inputText is an answer with the cursor on it, scrolled so the cursor
// stays in view. The character under the cursor is inverted, and at the
// end the cursor is a block.
func inputText(f field, reveal, focused bool, w int) string {
	r := []rune(printable(f.value))
	if f.secret && !reveal {
		r = []rune(strings.Repeat("•", len(r)))
	}
	at := len(r) - min(max(f.back, 0), len(r))
	if !focused {
		return theme.Text.Render(truncateLeft(string(r), w))
	}
	// Fit the text on either side of the cursor in linear time. Rescanning
	// a shrinking copy of the entire answer makes long pastes quadratic.
	cursor := "█"
	if at < len(r) {
		cursor = string(r[at])
	}
	cursorW := ansi.StringWidth(cursor)
	left := tailCells(string(r[:at]), max(w-cursorW, 0))
	text := theme.Text.Render(left)
	if at < len(r) {
		right := ansi.Truncate(string(r[at+1:]), max(w-ansi.StringWidth(left)-cursorW, 0), "")
		text += theme.Selected.Render(cursor) + theme.Text.Render(right)
	} else {
		text += theme.Text.Render(cursor)
	}
	return truncate2(text, max(w, 0))
}

// choice is one row of a pick-one list: an inverted bar when chosen.
func choice(on bool, name, note string, nameW, w int) string {
	nameW = min(nameW, max(w-5, 0))
	label := " ( ) " + padPlain(truncate(name, nameW), nameW)
	if note != "" {
		label += " "
	}
	if on {
		return theme.Selected.Render(padPlain(truncate(strings.Replace(label, "( )", "(•)", 1)+note, w), w))
	}
	return pad(theme.Text.Render(label)+theme.Dim.Render(truncate(note, max(w-lipgloss.Width(label), 0))), w)
}

// phraseCard is the recovery phrase in a bordered card, the words covered
// until [v].
func (m setupModel) phraseCard(w int) string {
	words := strings.Fields(m.key.Phrase())
	cols := 4
	if w-4 < 4*11+3 {
		cols = 3
	}
	gap := max((w-4-cols*11)/max(cols-1, 1), 0)
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
			line += theme.Dim.Render(fmt.Sprintf("%2d ", i+1)) + word
			if c < cols-1 {
				line += fill(gap, 1)
			}
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
			center(theme.Text, "frost backs up your files.", tw),
			fill(w, 2),
			row(theme.Text.Render("learn more at ")+theme.Text.Bold(true).Underline(true).Render(projectLink)),
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
	block := stack(row(theme.Dim.Render("Welcome to")), blank(w), row(mark), fill(w, gap), rest)
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, block, lipgloss.WithWhitespaceBackground(theme.Bg))
}

// viewQuit asks before quitting, in the middle of the card.
func viewQuit(w, h int) string {
	row := func(s string) string {
		return lipgloss.PlaceHorizontal(w, lipgloss.Center, s, lipgloss.WithWhitespaceBackground(theme.Bg))
	}
	keys := theme.Bold.Render("[y]") + theme.Text.Render(" yes") + theme.Base.Render("     ") +
		theme.Bold.Render("[n]") + theme.Text.Render(" no")
	block := stack(
		row(theme.Bold.Render("Are you sure you want to quit?")),
		blank(w),
		row(theme.Dim.Render("Unsaved changes will be lost.")),
		fill(w, 2),
		row(keys),
	)
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

// ---- Permafrost checkout ----

// checkoutPage is the wait for the browser, or what went wrong with it.
func (m setupModel) checkoutPage(cw int) page {
	over := "Permafrost"
	if !m.co.waiting {
		keys := theme.Bold.Render("[r]") + theme.Text.Render(" try again") + theme.Base.Render("     ") +
			theme.Bold.Render("[p]") + theme.Text.Render(" paste a key instead")
		msg := m.co.failed
		if msg == "" {
			msg = "checkout stopped"
		}
		return page{
			over:     over,
			question: "Checkout didn't finish.",
			body:     []string{para(theme.Caution, sentence(msg), cw), blank(cw), pad(keys, cw)},
		}
	}
	spin := theme.Bold.Render(m.spin.View())
	status := spin + theme.Text.Render(" Waiting for checkout")
	if m.co.page == "" {
		status = spin + theme.Text.Render(" Opening your browser")
	}
	left := max(time.Until(m.co.until).Round(time.Second), 0)
	clock := theme.Dim.Render(fmt.Sprintf("%d:%02d left", int(left.Minutes()), int(left.Seconds())%60))
	iw := cw - 4
	line := status + fill(max(iw-lipgloss.Width(status)-lipgloss.Width(clock), 1), 1) + clock
	pg := page{
		over:     over,
		question: "Get your access key in your browser.",
		sub:      "Grab one there.",
		body:     []string{theme.Box(true).Width(cw - 2).Render(pad(line, iw))},
	}
	if m.co.page != "" {
		pg.help = "Browser didn't open? Go to " + m.co.page + ", then press [p] to paste your key."
	}
	return pg
}
