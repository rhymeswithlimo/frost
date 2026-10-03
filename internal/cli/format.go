package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/rhymeswithlimo/frost/internal/theme"
)

// CLI output uses foreground colours only, so it looks right on any
// terminal background.
var (
	sDim     = lipgloss.NewStyle().Foreground(theme.Subtle)
	sBold    = lipgloss.NewStyle().Bold(true)
	sGood    = lipgloss.NewStyle().Foreground(theme.CLIOK)
	sCaution = lipgloss.NewStyle().Foreground(theme.CLIWarn)
	sErr     = lipgloss.NewStyle().Foreground(theme.CLIBad).Bold(true)
	sAccent  = lipgloss.NewStyle().Foreground(theme.CLIAccent)
)

func dim(s string) string      { return sDim.Render(s) }
func bold(s string) string     { return sBold.Render(s) }
func good(s string) string     { return sGood.Render(s) }
func caution(s string) string  { return sCaution.Render(s) }
func errStyle(s string) string { return sErr.Render(s) }

// rail paints the rail in frost blue.
func rail(s string) string { return sAccent.Render(s) }

// Output longer than a line hangs off a rail on the left. ┌ opens a block
// with its title, ├ starts a section, rows sit on │, and └ closes the
// block with the outcome or the next step. On a row that needs attention,
// a marker in its status colour takes the rail's place, so the words
// themselves stay plain. One-line results skip all this and print as a
// plain line. Either way, a blank line sets the output apart from the
// commands around it.
const (
	railOpen    = "┌"
	railSection = "├"
	railLine    = "│"
	railClose   = "└"
	markOK      = "●"
	markWarn    = "▲"
	markFail    = "■"
)

// labelWidth is the width of a row's label column.
const labelWidth = 12

// blockOpen says a block is waiting for its └, so an error can close it.
var blockOpen bool

// block prints one block of rail output to out.
type block struct {
	out   io.Writer
	width int // label column, labelWidth unless a block needs more
}

func newBlock(out io.Writer) *block { return &block{out: out, width: labelWidth} }

// open starts the block with a bold title and dimmed details after it.
func (b *block) open(title, meta string) {
	s := rail(railOpen) + "  " + bold(title)
	if meta != "" {
		s += "  " + dim(meta)
	}
	fmt.Fprintln(b.out)
	fmt.Fprintln(b.out, s)
	blockOpen = true
}

// section starts a titled part of the block.
func (b *block) section(title string) {
	fmt.Fprintln(b.out, rail(railSection)+"  "+bold(title))
}

// gap is an empty rail line.
func (b *block) gap() { fmt.Fprintln(b.out, rail(railLine)) }

// line prints s on the rail. Each line of s gets the rail.
func (b *block) line(s string) { b.mark(rail(railLine), s, "") }

// ok, warn and fail print s with a marker in place of the rail.
func (b *block) ok(s string)   { b.mark(good(markOK), s, "") }
func (b *block) warn(s string) { b.mark(caution(markWarn), s, "") }
func (b *block) fail(s string) { b.mark(errStyle(markFail), s, "") }

// row prints an aligned "label  value". A value's later lines line up
// under its first.
func (b *block) row(label, value string) { b.markedRow(rail(railLine), label, value) }

// warnRow and failRow are rows with a marker in place of the rail.
func (b *block) warnRow(label, value string) { b.markedRow(caution(markWarn), label, value) }
func (b *block) failRow(label, value string) { b.markedRow(errStyle(markFail), label, value) }

func (b *block) markedRow(glyph, label, value string) {
	b.mark(glyph, dim(fmt.Sprintf("%-*s", b.width, label))+" "+value, strings.Repeat(" ", b.width+1))
}

// mark prints s with glyph on its first line and the rail on the rest,
// each later line starting with indent.
func (b *block) mark(glyph, s, indent string) {
	for i, l := range strings.Split(s, "\n") {
		g := glyph
		if i > 0 {
			g, l = rail(railLine), indent+l
		}
		if strings.TrimSpace(ansi.Strip(l)) == "" {
			fmt.Fprintln(b.out, g)
			continue
		}
		fmt.Fprintln(b.out, g+"  "+l)
	}
}

// close ends the block with s, which can be empty.
func (b *block) close(s string) {
	fmt.Fprintln(b.out, closeLine(s))
	fmt.Fprintln(b.out)
	blockOpen = false
}

// single prints a one-line result, spaced like a block.
func single(out io.Writer, s string) { fmt.Fprintf(out, "\n%s\n\n", s) }

// closeLine is └ and s, with s's later lines indented under its first.
func closeLine(s string) string {
	if s == "" {
		return rail(railClose)
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines[1:] {
		if l != "" {
			lines[i+1] = "   " + l
		}
	}
	return rail(railClose) + "  " + strings.Join(lines, "\n")
}

// railed puts s on the rail, for a progress line that redraws in place.
func railed(s string) string { return rail(railLine) + "  " + s }

// humanBytes formats a byte count like "3.2 GB".
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

// plural formats a count of things, like "1 snapshot" or "3 snapshots".
func plural(n int, thing string) string {
	if n == 1 {
		return "1 " + thing
	}
	return humanCount(n) + " " + thing + "s"
}

// humanCount formats 12345 as "12,345".
func humanCount(n int) string {
	s := fmt.Sprint(n)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// ago formats a past time as "3h ago".
func ago(t time.Time) string { return relative(time.Since(t)) + " ago" }

// in formats a future time as "in 3h".
func in(t time.Time) string {
	d := time.Until(t)
	if d < 0 {
		return "overdue"
	}
	return "in " + relative(d)
}

func relative(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "moments"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// when formats a timestamp in local time.
func when(t time.Time) string { return t.Local().Format("2006-01-02 15:04") }
