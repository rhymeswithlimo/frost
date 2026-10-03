package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/rhymeswithlimo/frost/internal/tui"
)

func TestHelpShowsEverything(t *testing.T) {
	mark := strings.Split(tui.Wordmark(), "\n")[0]
	help := func(width int) string {
		root := NewRoot()
		var out bytes.Buffer
		root.SetOut(&out)
		rootHelp(root, width)
		return out.String()
	}

	out := help(80)
	for _, want := range []string{
		mark, "frost backs up", "┌  frost <command> [flags]",
		"restore [snapshot] [paths...]", "config [get|set|edit]", "key <show|verify|import>",
		"--config-dir <dir>", "show this help", "show frost's version",
		"│  backup:", "-n, --dry-run", "--exclude <pattern>", "│  restore:", "--to <dir>", "-y, --yes",
		"│  status:", "-a, --all", "│  config:", "--show-secrets", "│  update:", "--check", "├  flags",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("help is missing %q:\n%s", want, out)
		}
	}
	for _, hidden := range []string{"cache-dir", "--scheduled", "--log-file", "help for", "├  backup", "├  update"} {
		if strings.Contains(out, hidden) {
			t.Errorf("help shows %q:\n%s", hidden, out)
		}
	}
	if out := help(40); strings.Contains(out, mark) {
		t.Errorf("wordmark shown in 40 columns:\n%s", out)
	}

	// Every -h is the same help. Piped, there's no terminal and so no wordmark.
	top := must(t, "", "-h")
	if strings.Contains(top, mark) || !strings.Contains(top, "│  backup:") {
		t.Errorf("frost -h, piped:\n%s", top)
	}
	for _, args := range [][]string{{"backup", "-h"}, {"config", "--help"}} {
		if out := must(t, "", args...); out != top {
			t.Errorf("frost %s isn't the same help:\n%s", strings.Join(args, " "), out)
		}
	}
}
