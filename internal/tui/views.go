package tui

import (
	"fmt"
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/rhymeswithlimo/frost/internal/snapshot"
	"github.com/rhymeswithlimo/frost/internal/theme"
)

// ---- home ----

func (m model) viewHome() string {
	w := m.innerW()
	info := m.summary()
	if m.loading != "" {
		info = theme.Bold.Render(m.spin.View()) + theme.Text.Render(" "+m.loading+"...")
	}
	logo := logo(w, m.areaH()-4-lipgloss.Height(info))

	// Centre each piece on a full-width background first, so the join
	// doesn't pad with uncoloured spaces.
	row := func(s string) string {
		return lipgloss.PlaceHorizontal(w, lipgloss.Center, s, lipgloss.WithWhitespaceBackground(theme.Bg))
	}
	// The tagline belongs to the wordmark: when only the small title fits,
	// it goes too.
	block := stack(row(logo), fill(w, 2), row(info))
	if lipgloss.Height(logo) > 1 {
		block = stack(row(logo), fill(w, 1), row(theme.Dim.Render(tagline)), fill(w, 2), row(info))
	}
	return m.center(block)
}

// tagline goes under the wordmark.
const tagline = "back up your files."

// logo is the wordmark if it fits in w by h cells, or the small inverted
// title if it doesn't.
func logo(w, h int) string {
	mark := strings.TrimRight(wordmark, "\n")
	if lipgloss.Width(mark) > w || lipgloss.Height(mark) > h {
		return theme.Title.Render("FROST")
	}
	lines := strings.Split(mark, "\n")
	for i, l := range lines {
		lines[i] = theme.Wordmark.Render(l)
	}
	return strings.Join(lines, "\n")
}

func (m model) summary() string {
	row := func(k, v string) string {
		return truncate2(theme.Dim.Render(fmt.Sprintf("%-13s", k))+v, max(m.innerW()-4, 1))
	}
	var rows []string
	if n := len(m.snaps); n == 0 {
		rows = append(rows, row("snapshots", theme.Text.Render("none yet, run `frost backup`")))
	} else {
		rows = append(rows, row("snapshots", theme.Text.Render(fmt.Sprintf("%d, newest %s", n, ago(m.snaps[0].Time)))))
		rows = append(rows, row("protected", theme.Text.Render(fmt.Sprintf("%d files, %s", m.snaps[0].Stats.Files, humanBytes(m.snaps[0].Stats.Bytes)))))
	}
	if last := m.st.Last; m.st.HasLast {
		switch {
		case last.Error != "":
			rows = append(rows, row("last backup", theme.Error.Render("failed ")+theme.Text.Render(ago(last.Time))))
		case last.Skipped > 0:
			rows = append(rows, row("last backup", theme.Caution.Render(fmt.Sprintf("ok, %d items unreadable ", last.Skipped))+theme.Text.Render(ago(last.Time))))
		default:
			rows = append(rows, row("last backup", theme.Good.Render("ok ")+theme.Text.Render(ago(last.Time))))
		}
		if last.Error == "" && last.Kept > 0 {
			rows = append(rows, row("busy files", theme.Caution.Render(fmt.Sprintf("%d kept their previous copy", last.Kept))))
		}
	}
	if v, ok := m.st.Verify, m.st.HasVerify; !ok {
		rows = append(rows, row("health", theme.Dim.Render("not checked yet")))
	} else if v.OK() {
		rows = append(rows, row("health", theme.Good.Render("ok ")+theme.Text.Render(fmt.Sprintf("%d objects checked %s", v.Checked, ago(v.Time)))))
	} else {
		rows = append(rows, row("health", theme.Error.Render(fmt.Sprintf("%d problems found %s", len(v.Failures), ago(v.Time)))))
	}
	w := 0
	for _, r := range rows {
		w = max(w, lipgloss.Width(r))
	}
	for i, r := range rows {
		rows[i] = pad(r, w)
	}
	return theme.Box(false).Render(stack(rows...))
}

// ---- snapshots ----

type snapRow struct {
	header string // date heading, or "" for a snapshot row
	idx    int
}

func (m *model) indexSnapshots() {
	rows := make([]snapRow, 0, len(m.snaps))
	m.snapPositions = make([]int, len(m.snaps))
	m.snapCountW, m.snapSizeW = 0, 0
	last := ""
	for i, s := range m.snaps {
		d := s.Time.Local().Format("Mon 02 Jan 2006")
		if d != last {
			rows = append(rows, snapRow{header: d})
			last = d
		}
		m.snapPositions[i] = len(rows)
		rows = append(rows, snapRow{idx: i})
		m.snapCountW = max(m.snapCountW, len(strconv.Itoa(s.Stats.Files)))
		m.snapSizeW = max(m.snapSizeW, len(humanBytes(s.Stats.Bytes)))
	}
	m.snapLayout = rows
	m.short = snapshot.Shorten(m.snaps)
}

func (m model) viewSnapshots() string {
	w, h := m.innerW(), m.bodyH()
	if len(m.snaps) == 0 {
		return m.center(theme.Text.Render("No snapshots yet. Run ") + theme.Bold.Render("frost backup") + theme.Text.Render(" first."))
	}
	showDetail := w >= 90
	listW := w
	if showDetail {
		listW = w * 3 / 5
	}
	inner := listW - 4 // border and padding

	rows := m.snapLayout
	curRow := m.snapPositions[m.snapCur]
	top := window(curRow, len(rows), h)

	var lines []string
	for _, r := range rows[top:min(top+h, len(rows))] {
		if r.header != "" {
			lines = append(lines, pad(theme.Bold.Render(r.header), inner))
			continue
		}
		s := m.snaps[r.idx]
		mark := "  "
		if s.ID == m.marked {
			mark = "* "
		}
		text := snapshotListLabel(s, m.short.Of(s.ID), mark, m.snapCountW, m.snapSizeW, inner)
		if r.idx == m.snapCur {
			lines = append(lines, theme.Selected.Render(padPlain(text, inner)))
		} else if s.ID == m.marked {
			lines = append(lines, pad(theme.Caution.Render(text), inner))
		} else {
			lines = append(lines, pad(theme.Text.Render(text), inner))
		}
	}
	list := theme.Box(true).Width(listW - 2).Height(h).Render(strings.Join(lines, "\n"))
	if !showDetail {
		return list
	}
	detail := theme.Box(false).Width(w - listW - theme.Gap - 2).Height(h).Render(m.snapDetail(m.snaps[m.snapCur], w-listW-theme.Gap-4))
	return side(theme.Gap, list, detail)
}

// snapshotListLabel is one row of the snapshot list, showing s by id. The
// file count and size are right-aligned in columns countW and sizeW wide, so
// they line up.
func snapshotListLabel(s snapshot.Snapshot, id, mark string, countW, sizeW, width int) string {
	prefix := mark + s.Time.Local().Format("15:04") + "  "
	suffix := fmt.Sprintf("  %*d files  %*s  ", countW, s.Stats.Files, sizeW, humanBytes(s.Stats.Bytes))
	idWidth := width - lipgloss.Width(prefix) - lipgloss.Width(suffix)
	if idWidth < 8 {
		return truncate(prefix+id, max(width-2, 0)) + strings.Repeat(" ", min(width, 2))
	}
	return prefix + padPlain(truncate(id, idWidth), idWidth) + suffix
}

func (m model) snapDetail(s snapshot.Snapshot, w int) string {
	row := func(k, v string) string {
		return pad(theme.Dim.Render(fmt.Sprintf("%-10s", k))+theme.Text.Render(truncate(v, w-10)), w)
	}
	lines := []string{
		pad(theme.Bold.Render(truncate(m.short.Of(s.ID), w)), w),
		fill(w, 1),
		row("taken", s.Time.Local().Format("2006-01-02 15:04:05")),
		row("", ago(s.Time)),
		row("host", s.Host),
		row("files", fmt.Sprintf("%d in %d folders", s.Stats.Files, s.Stats.Dirs)),
		row("size", humanBytes(s.Stats.Bytes)),
		row("new data", humanBytes(s.Stats.NewBytes)),
		fill(w, 1),
		pad(theme.Dim.Render("paths"), w),
	}
	var notes []string
	if n := s.Stats.Skipped; n > 0 {
		notes = append(notes, fill(w, 1), para(theme.Caution, fmt.Sprintf("%d items couldn't be read", n), w))
	}
	if n := s.Stats.Kept; n > 0 {
		notes = append(notes, fill(w, 1), para(theme.Caution, fmt.Sprintf("%d busy files kept their previous copy", n), w))
	}
	if m.marked != "" && m.marked != s.ID {
		notes = append(notes, fill(w, 1), pad(theme.Dim.Render(truncate("[d] compares with "+m.short.Of(m.marked), w)), w))
	}
	notes = strings.Split(strings.Join(notes, "\n"), "\n")
	if len(notes) == 1 && notes[0] == "" {
		notes = nil
	}
	if len(lines)+len(notes)+min(len(s.Paths), 2) > m.bodyH() {
		// Remove spacing first so warnings leave room for a path preview.
		compact := func(rows []string) []string {
			var out []string
			for _, r := range rows {
				if strings.TrimSpace(ansi.Strip(r)) != "" {
					out = append(out, r)
				}
			}
			return out
		}
		lines, notes = compact(lines), compact(notes)
		if len(lines)+len(notes)+min(len(s.Paths), 2) > m.bodyH() {
			lines = append(lines[:2], lines[3:]...) // the relative time repeats the taken row
		}
	}
	reserve := min(len(s.Paths), 1)
	notes = notes[:min(len(notes), max(m.bodyH()-reserve, 0))]
	lines = lines[:min(len(lines), max(m.bodyH()-len(notes)-reserve, 0))]
	room := max(m.bodyH()-len(lines)-len(notes), 0)
	show := min(len(s.Paths), room)
	if show < len(s.Paths) && show > 0 {
		show-- // the omitted count occupies one preview row
	}
	for _, p := range s.Paths[:show] {
		lines = append(lines, pad(theme.Text.Render(truncate("  "+shortPath(p, w-2), w)), w))
	}
	if more := len(s.Paths) - show; more > 0 && room > 0 {
		lines = append(lines, pad(theme.Dim.Render(fmt.Sprintf("  +%d", more)), w))
	}
	lines = append(lines, notes...)
	return strings.Join(lines, "\n")
}

// ---- files ----

func (m model) viewFiles() string {
	w, h := m.innerW(), m.bodyH()
	inner := w - 4
	kids := m.tree.children[m.dir]
	nameW, sizeW, timeW := fileColumns(inner)

	lines := m.fileHeader()
	listH := max(h-len(lines), 1)
	top := window(m.fileCur, len(kids), listH)

	if len(kids) == 0 {
		lines = append(lines, pad(theme.Dim.Render("(empty folder)"), inner))
	}
	for i := top; i < min(top+listH, len(kids)); i++ {
		p := kids[i]
		f := m.tree.files[p]
		box := "[ ] "
		if m.sel[p] {
			box = "[x] "
		} else if covered(p, m.sel) {
			box = "[.] "
		}
		name := path.Base(p)
		if m.dir == rootKey {
			pathW := nameW
			if f.Type == snapshot.TypeDir {
				pathW--
			}
			name = shortPath(p, pathW)
		}
		size := humanBytes(f.Size)
		switch f.Type {
		case snapshot.TypeDir:
			name = truncate(name, max(nameW-1, 0)) + "/"
			size = humanBytes(m.tree.totals[p].bytes)
		case snapshot.TypeSymlink:
			name += " -> " + f.Target
			size = ""
		}
		text := box + padPlain(truncate(name, nameW), nameW)
		if sizeW > 0 {
			text += fmt.Sprintf("  %*s", sizeW, truncate(size, sizeW))
		}
		if timeW > 0 {
			format := "2006-01-02"
			if timeW == 16 {
				format += " 15:04"
			}
			text += "  " + f.ModTime.Local().Format(format)
		}

		switch {
		case i == m.fileCur:
			lines = append(lines, theme.Selected.Render(padPlain(text, inner)))
		case m.sel[p] || covered(p, m.sel):
			lines = append(lines, pad(theme.Bold.Render(text), inner))
		case f.Type == snapshot.TypeDir:
			lines = append(lines, pad(theme.Text.Render(text), inner))
		default:
			lines = append(lines, pad(theme.Dim.Render(box)+theme.Text.Render(text[len(box):]), inner))
		}
	}
	return theme.Box(true).Width(w - 2).Height(h).Render(strings.Join(lines, "\n"))
}

func (m model) fileHeader() []string {
	inner := m.innerW() - 4
	nameW, sizeW, timeW := fileColumns(inner)
	status := theme.Dim.Render(fmt.Sprintf("%d items", len(m.tree.children[m.dir])))
	if m.selFiles > 0 {
		status = theme.Bold.Render(fmt.Sprintf("%d files selected (%s)", m.selFiles, humanBytes(m.selBytes)))
	}
	var lines []string
	if lipgloss.Width(status) > nameW+4 {
		lines = strings.Split(ansi.Wrap(status, inner, ""), "\n")
		status = ""
	}
	if status != "" || sizeW > 0 || timeW > 0 {
		head := pad(status, nameW+4)
		if sizeW > 0 {
			head += theme.Dim.Render(fmt.Sprintf("  %*s", sizeW, "size"))
		}
		if timeW > 0 {
			head += theme.Dim.Render("  " + padPlain("modified", timeW))
		}
		lines = append(lines, pad(head, inner))
	}
	lines = append(lines, pad(theme.Faded.Render(strings.Repeat("─", inner)), inner))
	return lines[:min(len(lines), max(m.bodyH()-1, 1))]
}

func (m model) fileListH() int { return max(m.bodyH()-len(m.fileHeader()), 1) }

// Give names space before adding metadata columns. All widths are cells.
func fileColumns(inner int) (name, size, modified int) {
	if inner >= 28 {
		size = 8
	}
	if inner >= 54 {
		modified = 10
	}
	if inner >= 70 {
		modified = 16
	}
	name = max(inner-4, 0)
	if size > 0 {
		name -= size + 2
	}
	if modified > 0 {
		name -= modified + 2
	}
	return
}

// ---- diff ----

// removed is the red for removals: the error colour without the error's
// weight, so the three kinds of change read as equals.
var removed = theme.Error.UnsetBold()

func (m model) viewDiff() string {
	w, h := m.innerW(), m.bodyH()
	inner := w - 4
	lines := m.diffHeader()

	if len(m.changes) == 0 {
		lines = append(lines, pad(theme.Dim.Render("No differences."), inner))
	}
	listH := max(h-len(lines), 1)
	end := min(m.diffTop+listH, len(m.changes))
	for _, c := range m.changes[min(m.diffTop, end):end] {
		var sym string
		var st lipgloss.Style
		var detail string
		switch c.Kind {
		case snapshot.Added:
			sym, st = "+", theme.Good
			detail = humanBytes(c.New.Size)
		case snapshot.Removed:
			sym, st = "-", removed
			detail = humanBytes(c.Old.Size)
		default:
			sym, st = "~", theme.Caution
			detail = humanBytes(c.Old.Size) + " → " + humanBytes(c.New.Size)
		}
		detailW := min(24, max(inner/3, 0))
		pathW := max(inner-detailW-4, 0)
		p := truncateLeft(relToRoot(c.Path, m.diffTo.Paths, m.diffFrom.Paths), pathW)
		lines = append(lines, pad(st.Render(sym+" ")+theme.Text.Render(padPlain(p, pathW))+theme.Dim.Render("  "+fmt.Sprintf("%*s", detailW, truncate(detail, detailW))), inner))
	}
	return theme.Box(true).Width(w - 2).Height(h).Render(strings.Join(lines, "\n"))
}

func (m model) diffHeader() []string {
	inner := m.innerW() - 4
	// Counts in their colour, all one weight, and dimmed when there are none.
	count := func(st lipgloss.Style, n int, s string) string {
		if n == 0 {
			st = theme.Dim
		}
		return st.Render(fmt.Sprintf(s, n))
	}
	var lines []string
	line := ""
	for _, part := range []string{count(theme.Good, m.diffAdd, "+%d added"), count(removed, m.diffDel, "-%d removed"), count(theme.Caution, m.diffMod, "~%d changed")} {
		if line != "" && lipgloss.Width(line)+3+lipgloss.Width(part) > inner {
			lines = append(lines, strings.Split(ansi.Wrap(line, inner, ""), "\n")...)
			line = ""
		}
		if line != "" {
			line += theme.Base.Render("   ")
		}
		line += part
	}
	lines = append(lines, strings.Split(ansi.Wrap(line, inner, ""), "\n")...)
	lines = append(lines, pad(theme.Faded.Render(strings.Repeat("─", inner)), inner))
	return lines[:min(len(lines), max(m.bodyH()-1, 1))]
}

func (m model) diffListH() int { return max(m.bodyH()-len(m.diffHeader()), 1) }

// ---- help and settings ----

func (m model) viewHelp() string {
	w := m.overlayW()
	return m.dialog(m.helpContent(w), w, m.overlayTop, theme.Box(true))
}

func (m model) helpContent(w int) string {
	colW := w
	if w >= 68 {
		colW = (w - 3) / 2
	}
	section := func(title string, rows [][2]string) string {
		out := []string{theme.Dim.Render(title)}
		for _, r := range rows {
			// Keys in a column one wider than the longest, [pgup pgdn].
			k := "[" + r[0] + "]"
			for i, line := range strings.Split(para(theme.Text, r[1], max(colW-12, 1)), "\n") {
				prefix := fill(12, 1)
				if i == 0 {
					prefix = theme.Bold.Render(k) + fill(max(12-lipgloss.Width(k), 1), 1)
				}
				out = append(out, pad(prefix+line, colW))
			}
		}
		return stack(out...)
	}
	everywhere := section("Everywhere", [][2]string{{"h", "help"}, {"s", "settings"}, {"v", "show / hide key"}, {"esc", "back"}, {"q", "quit"}})
	moving := section("Moving", [][2]string{{"↑↓", "move (or k j)"}, {"pgup pgdn", "page"}, {"g G", "first / last"}, {"enter", "open"}, {"←", "parent folder"}})
	snapshots := section("Snapshots", [][2]string{
		{"d", "compare with previous snapshot"},
		{"m", "mark source, then [d] on another snapshot"},
	})
	files := section("Files", [][2]string{{"space", "select / unselect"}, {"a", "select / clear all"}, {"c", "clear selection"}, {"r", "restore selected or focused item"}})
	body := stack(side(3, everywhere, snapshots), fill(w, 1), side(3, moving, files))
	if w < 68 {
		body = stack(everywhere, fill(1, 1), moving, fill(1, 1), snapshots, fill(1, 1), files)
	}
	footer := theme.Dim.Render("Documentation  ") + theme.Bold.Render("getfro.st/docs")
	if lipgloss.Width(footer) > w {
		footer = stack(para(theme.Dim, "Documentation", w), para(theme.Bold, "getfro.st/docs", w))
	}
	body = stack(body, fill(1, 1), footer)
	return body
}

func (m model) viewSettings() string {
	w := m.overlayW()
	return m.dialog(m.settingsContent(w), w, m.overlayTop, theme.Box(true))
}

func (m model) settingsContent(w int) string {
	const labelW = 19
	c := m.cfg
	var lines []string
	rowValue := func(k, v string) {
		if w < labelW+12 {
			if k != "" {
				lines = append(lines, para(theme.Dim, k, w))
			}
			lines = append(lines, v)
			return
		}
		for i, l := range strings.Split(v, "\n") {
			label := ""
			if i == 0 {
				label = k
			}
			lines = append(lines, theme.Dim.Render(fmt.Sprintf("%-*s", labelW, label))+l)
		}
	}
	valueW := w
	if w >= labelW+12 {
		valueW -= labelW
	}
	row := func(k, v string) {
		if v == "" {
			v = "none"
		}
		rowValue(k, para(theme.Text, v, valueW))
	}
	sched := "off"
	if c.Schedule.Enabled {
		sched = c.Schedule.Every
	}
	if len(c.Paths) == 0 {
		row("backing up", "none")
	}
	// One row per folder, cut from the left: the end of a path is the part
	// that tells folders apart.
	for i, p := range c.Paths {
		label := ""
		if i == 0 {
			label = "backing up"
		}
		rowValue(label, theme.Text.Render(shortPath(p, valueW)))
	}
	row("skipping", strings.Join(c.Exclude, ", "))
	lines = append(lines, blank(w))
	row("automatic backups", sched)
	if m.st.Updates != "" {
		row("updates", m.st.Updates)
	}
	row("spot check", fmt.Sprintf("%d chunks after each backup", c.Verify.Sample))
	lines = append(lines, blank(w))
	row("storage", m.repo.Backend.String())
	rowValue("key fingerprint", m.keyLabel())
	lines = append(lines, blank(w))
	lines = append(lines, settingsFooter(printable(m.st.Version), w)...)
	for i, l := range lines {
		lines[i] = pad(l, w)
	}
	return stack(lines...)
}

func (m model) overlayW() int { return m.dialogW(90) }

// overlayH is how many rows of the open overlay show at once.
func (m model) overlayH() int {
	rows, _ := m.dialogRows(lipgloss.Height(m.overlayContent()))
	return max(rows, 1)
}

func (m model) overlayContent() string {
	if m.overlay == "help" {
		return m.helpContent(m.overlayW())
	}
	if m.overlay == "settings" {
		return m.settingsContent(m.overlayW())
	}
	return ""
}

func (m model) overlayMaxTop() int {
	return m.dialogMaxTop(lipgloss.Height(m.overlayContent()))
}

// settingsFooter is the how-to-change line, with the version set in the
// bottom-right corner like a colophon. It takes two lines when one's too
// narrow.
func settingsFooter(version string, w int) []string {
	first := theme.Dim.Render("Change these with ") + theme.Bold.Render("frost config set") + theme.Dim.Render(" or")
	second := theme.Bold.Render("frost config edit")
	colophon := ""
	if version != "" {
		colophon = theme.Dim.Render("frost " + version)
	}
	if lipgloss.Width(second)+2+lipgloss.Width(colophon) > w {
		lines := strings.Split(ansi.Wrap(first+theme.Text.Render(" ")+second, w, ""), "\n")
		if colophon != "" {
			lines = append(lines, strings.Split(para(theme.Dim, "frost "+version, w), "\n")...)
		}
		return lines
	}
	right := func(left string) string {
		gap := w - lipgloss.Width(left) - lipgloss.Width(colophon)
		if colophon == "" || gap < 2 {
			return left
		}
		return left + fill(gap, 1) + colophon
	}
	one := first + theme.Text.Render(" ") + second
	if lipgloss.Width(one)+2+lipgloss.Width(colophon) <= w || (colophon == "" && lipgloss.Width(one) <= w) {
		return []string{right(one)}
	}
	if lipgloss.Width(first)+2+lipgloss.Width(colophon) <= w {
		return []string{right(first), second}
	}
	return append(strings.Split(ansi.Wrap(first, w, ""), "\n"), right(second))
}

// ---- helpers ----

// window returns the first visible row so that cur stays on screen.
func window(cur, n, h int) int {
	if n <= h {
		return 0
	}
	top := cur - h/2
	return max(0, min(top, n-h))
}

// relToRoot shows a path relative to the parent of the backup root it's
// under, e.g. "Documents/taxes/2025.pdf" for a root of ~/Documents.
func relToRoot(p string, roots ...[]string) string {
	for _, rs := range roots {
		for _, r := range rs {
			if p == r || strings.HasPrefix(p, r+"/") {
				return strings.TrimPrefix(p, path.Dir(r)+"/")
			}
		}
	}
	return p
}

// truncateLeft fits s into w cells by cutting from the left.
func truncateLeft(s string, w int) string {
	s = printable(s)
	sw := ansi.StringWidth(s)
	if sw <= w {
		return s
	}
	if w <= 3 {
		return tailCells(s, w)
	}
	return "..." + tailCells(s, w-3)
}

// tailCells keeps whole graphemes at the end of s without an ellipsis.
func tailCells(s string, w int) string {
	if w <= 0 {
		return ""
	}
	cut := max(ansi.StringWidth(s)-w, 0)
	tail := ansi.TruncateLeft(s, cut, "")
	if ansi.StringWidth(tail) > w { // the cut landed inside a wide character
		tail = ansi.TruncateLeft(s, cut+1, "")
	}
	return tail
}

// truncate fits s into w cells by cutting from the right. Widths are in
// cells, not runes, so wide characters (CJK, emoji) don't overflow a row.
func truncate(s string, w int) string {
	s = printable(s)
	if ansi.StringWidth(s) <= w {
		return s
	}
	if w <= 3 {
		return ansi.Truncate(s, max(w, 0), "")
	}
	return ansi.Truncate(s, w, "...")
}

// printable replaces control characters, which file names can contain on
// Unix, so a name can't move the cursor or restyle the screen.
func printable(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "?")
	}
	if !strings.ContainsFunc(s, unicode.IsControl) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '?'
		}
		return r
	}, s)
}

// truncate2 trims a styled string to w cells.
func truncate2(s string, w int) string {
	if lipgloss.Width(s) <= w {
		return s
	}
	return lipgloss.NewStyle().MaxWidth(w).Render(s)
}

func padPlain(s string, w int) string {
	if d := w - lipgloss.Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}
