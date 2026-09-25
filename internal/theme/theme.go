// Package theme is the single place frost's colours, borders and spacing are
// defined. The TUI and the CLI both style themselves from here.
package theme

import "github.com/charmbracelet/lipgloss"

// Palette. The five brand colours, plus status colours.
var (
	Pri    = lipgloss.Color("#f2efe7") // text, wordmark, buttons
	Sec    = lipgloss.Color("#0a0a0b") // text on Pri, the redaction cover
	Ter    = lipgloss.Color("#1926c4") // the TUI background
	Muted  = lipgloss.Color("#b1aea9") // secondary text
	Subtle = lipgloss.Color("#797774") // rules, idle borders, CLI hints

	// Status colours for the TUI, light enough to read on Ter.
	OK   = lipgloss.Color("#7ee787")
	Warn = lipgloss.Color("#f2cc60")
	Bad  = lipgloss.Color("#ffa198")

	// Status colours for CLI output, which lands on the user's own
	// terminal background.
	CLIOK   = lipgloss.Color("#3fb950")
	CLIWarn = lipgloss.Color("#d29922")
	CLIBad  = lipgloss.Color("#f85149")
)

// Bg is the TUI background.
var Bg = Ter

// Border is used for every box: sharp corners, never rounded.
var Border = lipgloss.NormalBorder()

// Spacing inside the TUI, in cells.
const (
	PadX = 2 // left and right screen margin
	PadY = 1 // top and bottom screen margin
	Gap  = 1 // space between panels
)

// base carries the background so styled text never punches a hole
// through it.
var base = lipgloss.NewStyle().Background(Bg)

// Text styles. The TUI is light text on the Ter blue. Anything that needs to
// stand out (the title, key buttons, the selection bar) is inverted: Pri
// background with dark text.
var (
	Base     = base
	Text     = base.Foreground(Pri)
	Dim      = base.Foreground(Muted)
	Faded    = base.Foreground(Subtle)
	Bold     = base.Foreground(Pri).Bold(true)
	Good     = base.Foreground(OK)
	Caution  = base.Foreground(Warn)
	Error    = base.Foreground(Bad).Bold(true)
	Wordmark = base.Foreground(Pri)
	Title    = lipgloss.NewStyle().Foreground(Ter).Background(Pri).Bold(true).Padding(0, 1)
	Selected = lipgloss.NewStyle().Foreground(Sec).Background(Pri)
	Key      = lipgloss.NewStyle().Foreground(Ter).Background(Pri).Bold(true)
	// Redacted is the black cover over hidden values.
	Redacted = lipgloss.NewStyle().Foreground(Sec).Background(Sec)
)

// Box is a bordered, padded panel. The focused panel gets a bright border.
func Box(focused bool) lipgloss.Style {
	c := Subtle
	if focused {
		c = Pri
	}
	return base.Border(Border).BorderForeground(c).BorderBackground(Bg).Padding(0, 1)
}

// Hint renders a footer shortcut like "[h] help": the key in bold white,
// the label dimmed.
func Hint(key, label string) string {
	return Bold.Render("["+key+"]") + Dim.Render(" "+label)
}
