package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// prompter asks questions on the command's stdin and stdout.
type prompter struct {
	in  *bufio.Reader
	out io.Writer
}

func newPrompter(cmd *cobra.Command) *prompter {
	return &prompter{in: bufio.NewReader(cmd.InOrStdin()), out: cmd.OutOrStdout()}
}

func (p *prompter) line() (string, error) {
	s, err := p.in.ReadString('\n')
	if err != nil && (err != io.EOF || s == "") {
		return "", fmt.Errorf("no answer (input closed)")
	}
	return strings.TrimSpace(s), nil
}

// ask prompts with an optional default shown in brackets.
func (p *prompter) ask(q, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(p.out, "%s %s ", q, dim("["+def+"]"))
	} else {
		fmt.Fprintf(p.out, "%s ", q)
	}
	s, err := p.line()
	if err != nil {
		return "", err
	}
	if s == "" {
		return def, nil
	}
	return s, nil
}

// required keeps asking until it gets a non-empty answer.
func (p *prompter) required(q, def string) (string, error) {
	for {
		s, err := p.ask(q, def)
		if err != nil || s != "" {
			return s, err
		}
		fmt.Fprintln(p.out, dim("  this one's required"))
	}
}

// secret asks for a credential. If one is already set, pressing enter keeps
// it without ever printing it.
func (p *prompter) secret(q, current string) (string, error) {
	if current == "" {
		return p.required(q, "")
	}
	s, err := p.ask(q, "keep current")
	if err != nil || s == "keep current" {
		return current, err
	}
	return s, nil
}

// yesNo asks a yes/no question.
func (p *prompter) yesNo(q string, def bool) (bool, error) {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	for {
		fmt.Fprintf(p.out, "%s %s ", q, dim("["+hint+"]"))
		s, err := p.line()
		if err != nil {
			return false, err
		}
		switch strings.ToLower(s) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
	}
}

// choose asks the user to pick one of options by number. No default.
func (p *prompter) choose(q string, options []string) (int, error) {
	fmt.Fprintln(p.out, q)
	for i, o := range options {
		fmt.Fprintf(p.out, "  %s %s\n", accent(fmt.Sprintf("[%d]", i+1)), o)
	}
	for {
		fmt.Fprint(p.out, "> ")
		s, err := p.line()
		if err != nil {
			return 0, err
		}
		var n int
		if _, err := fmt.Sscanf(s, "%d", &n); err == nil && n >= 1 && n <= len(options) {
			return n - 1, nil
		}
	}
}

// list asks for a comma separated list.
func (p *prompter) list(q string, def []string) ([]string, error) {
	s, err := p.ask(q, strings.Join(def, ", "))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" && part != "-" {
			out = append(out, part)
		}
	}
	return out, nil
}

func isTerminal(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }
