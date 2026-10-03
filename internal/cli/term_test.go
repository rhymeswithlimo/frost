package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestStatusLineFitsTerminal(t *testing.T) {
	for _, text := range []string{
		"│  123 files, 42.0 MB scanned, 9.1 MB new  file.txt",
		"\x1b[34m│\x1b[0m  文件 🧊 " + strings.Repeat("long-name", 20),
	} {
		for _, width := range []int{1, 2, 3, 10, 40, 80} {
			t.Run(fmt.Sprintf("%d/%d", ansi.StringWidth(text), width), func(t *testing.T) {
				var out bytes.Buffer
				statusLineAtWidth(&out, text, width)
				if !strings.HasPrefix(out.String(), "\r\x1b[K") {
					t.Fatalf("progress didn't clear the old line: %q", out.String())
				}
				shown := strings.TrimPrefix(out.String(), "\r\x1b[K")
				if got := ansi.StringWidth(shown); got >= width {
					t.Fatalf("progress occupies %d cells in a %d-cell terminal: %q", got, width, shown)
				}
				if width > 1 && ansi.StringWidth(shown) == 0 {
					t.Fatalf("progress disappeared in a %d-cell terminal", width)
				}
				if !strings.HasPrefix(ansi.Strip(text), ansi.Strip(shown)) {
					t.Fatalf("progress lost its leading text: %q", shown)
				}
				if ansi.StringWidth(text) < width && shown != text {
					t.Fatalf("progress that fits was changed: %q", shown)
				}
				if strings.ContainsAny(shown, "\r\n") {
					t.Fatalf("progress moved to another line: %q", shown)
				}
			})
		}
	}
}

func TestStatusLineKeepsTextWhenWidthUnknown(t *testing.T) {
	const text = "│  2 files, 10 B scanned"
	var out bytes.Buffer
	statusLineAtWidth(&out, text, 0)
	if got, want := out.String(), "\r\x1b[K"+text; got != want {
		t.Fatalf("progress = %q, want %q", got, want)
	}
}
