package tui

import (
	"fmt"
	"path"
	"strings"
	"unicode"

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
	tagline := theme.Dim.Render(tagline)

	// Centre each piece on a full-width background first, so the join
	// doesn't pad with uncoloured spaces.
	row := func(s string) string {
		return lipgloss.PlaceHorizontal(w, lipgloss.Center, s, lipgloss.WithWhitespaceBackground(theme.Bg))
	}
	block := stack(row(logo), fill(w, 1), row(tagline), fill(w, 2), row(info))
	return m.center(block)
}

// tagline goes under the wordmark.
const tagline = "encrypted backups. only you hold the key."

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
		return theme.Dim.Render(fmt.Sprintf("%-13s", k)) + v
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

func (m model) snapRows() []snapRow {
	var rows []snapRow
	last := ""
	for i, s := range m.snaps {
		d := s.Time.Local().Format("Mon 02 Jan 2006")
		if d != last {
			rows = append(rows, snapRow{header: d})
			last = d
		}
		rows = append(rows, snapRow{idx: i})
	}
	return rows
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

	rows := m.snapRows()
	curRow := 0
	for i, r := range rows {
		if r.header == "" && r.idx == m.snapCur {
			curRow = i
		}
	}
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
		text := fmt.Sprintf("%s%s  %-22s %9s  %s", mark, s.Time.Local().Format("15:04"), s.ID,
			fmt.Sprintf("%d files", s.Stats.Files), humanBytes(s.Stats.Bytes))
		text = truncate(text, inner)
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
	return lipgloss.JoinHorizontal(lipgloss.Top, list, fill(theme.Gap, h+2), detail)
}

func (m model) snapDetail(s snapshot.Snapshot, w int) string {
	row := func(k, v string) string {
		return pad(theme.Dim.Render(fmt.Sprintf("%-10s", k))+theme.Text.Render(truncate(v, w-10)), w)
	}
	lines := []string{
		pad(theme.Bold.Render(s.ID), w),
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
	for _, p := range s.Paths {
		lines = append(lines, pad(theme.Text.Render(truncate("  "+shortPath(p, w-2), w)), w))
	}
	if n := len(s.Warnings); n > 0 {
		lines = append(lines, fill(w, 1), pad(theme.Caution.Render(fmt.Sprintf("%d items couldn't be read", n)), w))
	}
	if m.marked != "" && m.marked != s.ID {
		lines = append(lines, fill(w, 1), pad(theme.Dim.Render("[d] compares with "+m.marked), w))
	}
	return strings.Join(lines, "\n")
}

// ---- files ----

func (m model) viewFiles() string {
	w, h := m.innerW(), m.bodyH()
	inner := w - 4
	kids := m.tree.children[m.dir]

	// Summary line: where we are and what's selected.
	selN, selB := m.selFiles, m.selBytes
	status := theme.Dim.Render(fmt.Sprintf("%d items", len(kids)))
	if selN > 0 {
		status += theme.Base.Render("   ") + theme.Bold.Render(fmt.Sprintf("%d files selected (%s)", selN, humanBytes(selB)))
	}
	lines := []string{pad(status, inner), pad(theme.Faded.Render(strings.Repeat("─", inner)), inner)}

	listH := h - 2
	top := window(m.fileCur, len(kids), listH)
	sizeW, timeW := 10, 16
	nameW := max(inner-4-sizeW-timeW-4, 10)

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
			name = shortPath(p, nameW)
		}
		size := humanBytes(f.Size)
		switch f.Type {
		case snapshot.TypeDir:
			name += "/"
			size = humanBytes(m.tree.sizes[p])
		case snapshot.TypeSymlink:
			name += " -> " + f.Target
			size = ""
		}
		text := box + padPlain(truncate(name, nameW), nameW) + "  " +
			fmt.Sprintf("%*s", sizeW, size) + "  " + f.ModTime.Local().Format("2006-01-02 15:04")

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

// ---- diff ----

func (m model) viewDiff() string {
	w, h := m.innerW(), m.bodyH()
	inner := w - 4
	add, del, mod := m.diffAdd, m.diffDel, m.diffMod
	head := theme.Text.Render(fmt.Sprintf("%s (%s)  >  %s (%s)   ",
		m.diffFrom.ID, m.diffFrom.Time.Local().Format("Jan 02 15:04"),
		m.diffTo.ID, m.diffTo.Time.Local().Format("Jan 02 15:04"))) +
		theme.Good.Render(fmt.Sprintf("+%d added", add)) + theme.Base.Render("  ") +
		theme.Error.Render(fmt.Sprintf("-%d removed", del)) + theme.Base.Render("  ") +
		theme.Caution.Render(fmt.Sprintf("~%d changed", mod))
	lines := []string{pad(head, inner), pad(theme.Faded.Render(strings.Repeat("─", inner)), inner)}

	if len(m.changes) == 0 {
		lines = append(lines, pad(theme.Dim.Render("No differences."), inner))
	}
	listH := h - 2
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
			sym, st = "-", theme.Error
			detail = humanBytes(c.Old.Size)
		default:
			sym, st = "~", theme.Caution
			detail = humanBytes(c.Old.Size) + " > " + humanBytes(c.New.Size)
		}
		p := truncateLeft(relToRoot(c.Path, m.diffTo.Paths, m.diffFrom.Paths), inner-26)
		lines = append(lines, pad(st.Render(sym+" ")+theme.Text.Render(padPlain(p, inner-26))+theme.Dim.Render(fmt.Sprintf("%24s", detail)), inner))
	}
	return theme.Box(true).Width(w - 2).Height(h).Render(strings.Join(lines, "\n"))
}

// ---- help and settings ----

func (m model) viewHelp() string {
	section := func(title string, rows [][2]string) string {
		out := []string{theme.Bold.Render(title)}
		for _, r := range rows {
			if r[0] == "" { // continuation of the line above
				out = append(out, theme.Text.Render(strings.Repeat(" ", 12)+r[1]))
				continue
			}
			k := "[" + r[0] + "]"
			out = append(out, theme.Key.Render(k)+theme.Text.Render(strings.Repeat(" ", max(12-lipgloss.Width(k), 1))+r[1]))
		}
		return stack(out...)
	}
	general := stack(
		section("Everywhere", [][2]string{{"h", "this help"}, {"s", "settings"}, {"v", "show or hide the key"}, {"esc", "go back"}, {"q", "quit"}}),
		fill(1, 1),
		section("Moving", [][2]string{{"↑ ↓", "move (or k j)"}, {"pgup pgdn", "page"}, {"g G", "top, bottom"}, {"enter", "open"}, {"←", "up a folder"}}),
	)
	specific := stack(
		section("Snapshots", [][2]string{
			{"d", "diff: list what was added, removed"},
			{"", "or changed since the snapshot before"},
			{"m", "mark: pick a snapshot to diff from,"},
			{"", "then press [d] on another one"},
		}),
		fill(1, 1),
		section("Files", [][2]string{{"space", "select for restore"}, {"a", "select whole folder"}, {"c", "clear selection"}, {"r", "restore selection"}}),
	)
	body := side(6, general, specific)
	if lipgloss.Width(body) > m.innerW()-8 {
		body = stack(general, fill(1, 1), specific)
	}
	footer := theme.Dim.Render("Confused? Check out the frost documentation at ") + theme.Bold.Render("getfro.st/docs")
	body = stack(body, fill(1, 1), footer)

	box := theme.Box(true)
	if lipgloss.Height(body)+4 <= m.areaH() {
		box = box.Padding(1, 3)
	}
	return m.center(box.Render(body))
}

func (m model) viewSettings() string {
	c := m.cfg
	row := func(k, v string) string {
		return theme.Dim.Render(fmt.Sprintf("%-18s", k)) + theme.Text.Render(v)
	}
	sched := "off"
	if c.Schedule.Enabled {
		sched = c.Schedule.Every
	}
	lines := []string{
		row("backing up", strings.Join(c.Paths, ", ")),
		row("skipping", strings.Join(c.Exclude, ", ")),
		row("automatic backups", sched),
		row("spot check", fmt.Sprintf("%d chunks after each backup", c.Verify.Sample)),
		row("storage", m.repo.Backend.String()),
		theme.Dim.Render(fmt.Sprintf("%-18s", "key fingerprint")) + m.keyLabel(),
		"",
		theme.Dim.Render("Change these with ") + theme.Bold.Render("frost config set") + theme.Dim.Render(" or ") + theme.Bold.Render("frost config edit"),
	}
	w := min(m.innerW()-8, 90)
	for i, l := range lines {
		lines[i] = pad(l, w)
	}
	return m.center(theme.Box(true).Padding(1, 3).Render(stack(lines...)))
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
	if sw <= w || w <= 3 {
		return s
	}
	tail := ansi.TruncateLeft(s, sw-w+3, "")
	if ansi.StringWidth(tail) > w-3 { // the cut landed inside a wide character
		tail = ansi.TruncateLeft(s, sw-w+4, "")
	}
	return "..." + tail
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
