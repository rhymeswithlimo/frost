package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/rhymeswithlimo/frost/internal/theme"
)

// CLI output uses foreground colours only, so it looks right on any
// terminal background. Keys and headings are small inverted buttons.
var (
	sDim     = lipgloss.NewStyle().Foreground(theme.Subtle)
	sBold    = lipgloss.NewStyle().Bold(true)
	sGood    = lipgloss.NewStyle().Foreground(theme.CLIOK)
	sCaution = lipgloss.NewStyle().Foreground(theme.CLIWarn)
	sErr     = lipgloss.NewStyle().Foreground(theme.CLIBad).Bold(true)
)

func accent(s string) string   { return theme.Key.Render(s) }
func dim(s string) string      { return sDim.Render(s) }
func bold(s string) string     { return sBold.Render(s) }
func good(s string) string     { return sGood.Render(s) }
func caution(s string) string  { return sCaution.Render(s) }
func errStyle(s string) string { return sErr.Render(s) }

// heading prints a small section title.
func heading(s string) string { return theme.Title.Render(strings.ToUpper(s)) }

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

// kv prints an aligned "label  value" row.
func kv(label, value string) string { return "  " + dim(fmt.Sprintf("%-12s", label)) + " " + value }
