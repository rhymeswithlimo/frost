package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"golang.org/x/term"
)

// ansiOK is false when the terminal can't take escape sequences, which is
// a Windows legacy console. Output is then plain text with no live
// progress line.
var ansiOK = true

// enableANSI turns on escape sequence processing for stdout and stderr.
// Windows consoles start with it off, so without this conhost (cmd.exe and
// PowerShell outside Windows Terminal) prints colours as "←[1m". It's a
// no-op elsewhere. The returned func puts the console back.
func enableANSI() (restore func()) {
	var undo []func() error
	for _, f := range []*os.File{os.Stdout, os.Stderr} {
		if !isTerminal(f) {
			continue
		}
		r, err := termenv.EnableVirtualTerminalProcessing(termenv.NewOutput(f))
		if err != nil {
			ansiOK = false
			continue
		}
		undo = append(undo, r)
	}
	if !ansiOK {
		lipgloss.SetColorProfile(termenv.Ascii)
	}
	return func() {
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
	}
}

// liveOutput reports whether a progress line can be redrawn in place.
func liveOutput() bool { return ansiOK && isTerminal(os.Stdout) }

// statusLine redraws the one-line progress display. It's cut to the
// terminal's width, because a line that wraps can't be erased by \r and
// leaves a trail of old progress lines behind.
func statusLine(out io.Writer, s string) {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 1 {
		s = ansi.Truncate(s, w-1, "")
	}
	fmt.Fprint(out, "\r\033[K"+s)
}

// clearStatus removes the progress line.
func clearStatus(out io.Writer) { fmt.Fprint(out, "\r\033[K") }

// printable replaces control characters, which file names can contain on
// Unix, so a name can't move the cursor or restyle the terminal.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '?'
		}
		return r
	}, s)
}
