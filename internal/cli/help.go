package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/term"

	"github.com/rhymeswithlimo/frost/internal/tui"
)

// flagHelp replaces cobra's wording for the flags it adds itself.
var flagHelp = map[string]string{
	"help":    "show this help",
	"version": "show frost's version",
}

// flagValue names what a flag takes, like the <dir> in --path <dir>.
// Flags not listed show their type.
var flagValue = map[string]string{
	"config-dir": "dir",
	"path":       "dir",
	"to":         "dir",
	"exclude":    "pattern",
}

// terminalWidth is out's width in columns, or 0 when it isn't a terminal.
func terminalWidth(out io.Writer) int {
	if f, ok := out.(*os.File); ok && isTerminal(f) {
		if w, _, err := term.GetSize(int(f.Fd())); err == nil {
			return w
		}
	}
	return 0
}

// rootHelp is frost's one help screen, for `frost -h` and every command's
// -h: the wordmark, what frost is, then every command and every flag on the
// rail. The wordmark only shows when width, the terminal's, has room for it.
func rootHelp(root *cobra.Command, width int) {
	root.InitDefaultHelpFlag()
	root.InitDefaultVersionFlag()
	out := root.OutOrStdout()
	fmt.Fprintln(out)
	if mark := strings.Split(tui.Wordmark(), "\n"); width > 3+len([]rune(mark[0])) {
		for _, l := range mark {
			fmt.Fprintln(out, "   "+sAccent.Render(l))
		}
		fmt.Fprintln(out)
	}
	for _, l := range strings.Split(root.Long, "\n") {
		fmt.Fprintln(out, "   "+l)
	}

	// Rows are gathered first so every section lines up on one column.
	type section struct {
		title string
		rows  []helpRow
	}
	var commands []helpRow
	sections := []section{{title: "flags", rows: flagRows(root, nil)}}
	for _, c := range root.Commands() {
		if !c.IsAvailableCommand() {
			continue
		}
		commands = append(commands, helpRow{synopsis(c), c.Short})
		if rows := flagRows(c, root); len(rows) > 0 {
			sections = append(sections, section{c.Name(), rows})
		}
	}
	w := 0
	for _, s := range append([]section{{rows: commands}}, sections...) {
		for _, r := range s.rows {
			w = max(w, len(r.left))
		}
	}
	// Columns are 4 apart, like the snapshot list in `status`.
	line := func(b *block, r helpRow) { b.line(fmt.Sprintf("%-*s    %s", w, r.left, r.right)) }

	b := newBlock(out)
	b.open("frost <command> [flags]", "")
	b.gap()
	for _, r := range commands {
		line(b, r)
	}
	// ├ starts the flags. Each command's flags follow right under its name.
	for i, s := range sections {
		b.gap()
		if i == 0 {
			b.section(s.title)
			b.gap()
		} else {
			b.line(s.title + ":")
		}
		for _, r := range s.rows {
			line(b, r)
		}
	}
	b.gap()
	b.close("")
}

// helpRow is one line of help: a command or flag, and what it does.
type helpRow struct{ left, right string }

// synopsis is a command with the arguments it takes, like
// "key <show|verify|import>".
func synopsis(c *cobra.Command) string {
	_, args, _ := strings.Cut(c.Use, " ")
	if len(c.ValidArgs) > 0 && args != "" {
		open, end := "[", "]"
		if args[0] == '<' {
			open, end = "<", ">"
		}
		args = open + strings.Join(c.ValidArgs, "|") + end
	}
	return strings.TrimSpace(c.Name() + " " + args)
}

// flagRows lists c's visible flags in the order they're defined. With a
// parent, the flags c inherits from it are left out.
func flagRows(c, parent *cobra.Command) []helpRow {
	var rows []helpRow
	fs := c.Flags()
	fs.SortFlags = false
	fs.VisitAll(func(f *pflag.Flag) {
		if f.Hidden || (parent != nil && (f.Name == "help" || parent.PersistentFlags().Lookup(f.Name) != nil)) {
			return
		}
		left := "    --" + f.Name
		if f.Shorthand != "" {
			left = "-" + f.Shorthand + ", --" + f.Name
		}
		if f.Value.Type() != "bool" {
			v, ok := flagValue[f.Name]
			if !ok {
				v = f.Value.Type()
			}
			left += " <" + v + ">"
		}
		usage := f.Usage
		if s, ok := flagHelp[f.Name]; ok {
			usage = s
		}
		rows = append(rows, helpRow{left, usage})
	})
	return rows
}
