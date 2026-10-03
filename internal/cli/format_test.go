package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func TestBlock(t *testing.T) {
	var out bytes.Buffer
	b := newBlock(&out)
	b.open("backup", "s3://backups/frost/")
	b.gap()
	b.row("files", "2 (300.0 kB)")
	b.warnRow("not found", "~/Old\nSkipped until it's back.")
	b.ok("valid phrase")
	b.fail("doesn't open\n\nthe repository")
	b.section("snapshots")
	if !blockOpen {
		t.Fatal("an open block isn't marked open")
	}
	b.close("Saved snapshot gift-flock-3664\nsecond line")
	if blockOpen {
		t.Fatal("a closed block is still marked open")
	}
	want := lines(
		"",
		"┌  backup  s3://backups/frost/",
		"│",
		"│  files        2 (300.0 kB)",
		"▲  not found    ~/Old",
		"│               Skipped until it's back.",
		"●  valid phrase",
		"■  doesn't open",
		"│",
		"│  the repository",
		"├  snapshots",
		"└  Saved snapshot gift-flock-3664",
		"   second line",
		"",
	)
	if out.String() != want {
		t.Fatalf("block rendered as:\n%s\nwant:\n%s", out.String(), want)
	}
}

// The whole rail is frost blue, and markers keep their status colours.
func TestRailIsBlue(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(termenv.Ascii) })
	blue, _, _ := strings.Cut(sAccent.Render("x"), "x")
	var out bytes.Buffer
	b := newBlock(&out)
	b.open("status", "")
	b.section("snapshots")
	b.gap()
	b.row("health", "ok")
	b.warn("careful")
	b.close("done")
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	for i, l := range lines {
		if i == 4 { // the warning
			if strings.Contains(l, blue) {
				t.Errorf("marker drawn in blue: %q", l)
			}
			continue
		}
		if !strings.HasPrefix(l, blue) {
			t.Errorf("rail not blue: %q", l)
		}
	}
}

func TestSingle(t *testing.T) {
	var out bytes.Buffer
	single(&out, "frost v1.0.0 is the latest release")
	if got, want := out.String(), "\nfrost v1.0.0 is the latest release\n\n"; got != want {
		t.Fatalf("single = %q, want %q", got, want)
	}
}

func TestErrorLine(t *testing.T) {
	err := errors.New("restore stopped\n\nTo carry on, run:")
	if got, want := errorLine(err, false), "error: restore stopped\n\nTo carry on, run:"; got != want {
		t.Errorf("errorLine = %q, want %q", got, want)
	}
	if got, want := errorLine(err, true), "└  error: restore stopped\n\n   To carry on, run:"; got != want {
		t.Errorf("errorLine closing a block = %q, want %q", got, want)
	}
}

func TestPlural(t *testing.T) {
	for n, want := range map[int]string{0: "0 snapshots", 1: "1 snapshot", 1200: "1,200 snapshots"} {
		if got := plural(n, "snapshot"); got != want {
			t.Errorf("plural(%d) = %q, want %q", n, got, want)
		}
	}
}
