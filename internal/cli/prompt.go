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

// prompter asks questions on the command's stdin and stdout. Its questions
// and hints sit on the rail of the block it prints.
type prompter struct {
	*block
	in  *bufio.Reader
	tty *os.File // stdin, when it's a terminal, for reading secrets unechoed
}

func newPrompter(cmd *cobra.Command) *prompter {
	p := &prompter{block: newBlock(cmd.OutOrStdout()), in: bufio.NewReader(cmd.InOrStdin())}
	if f, ok := cmd.InOrStdin().(*os.File); ok && isTerminal(f) {
		p.tty = f
	}
	return p
}

// answer reads one line of input.
func (p *prompter) answer() (string, error) {
	s, err := p.in.ReadString('\n')
	if err != nil && (err != io.EOF || s == "") {
		return "", fmt.Errorf("no answer (input closed)")
	}
	return strings.TrimSpace(s), nil
}

// question prints q on the rail and leaves the cursor after it.
func (p *prompter) question(q string) { fmt.Fprint(p.out, railed(q)+" ") }

// ask prompts with an optional default shown in brackets.
func (p *prompter) ask(q, def string) (string, error) {
	if def != "" {
		q += " " + dim("["+def+"]")
	}
	p.question(q)
	s, err := p.answer()
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
		p.line(dim("  this one's required"))
	}
}

// secret asks for a credential without echoing it. If one is already set,
// pressing enter keeps it without ever printing it.
func (p *prompter) secret(q, current string) (string, error) {
	hint := ""
	if current != "" {
		hint = " " + dim("[enter keeps the current one]")
	}
	for {
		p.question(q + hint)
		s, err := p.hidden()
		if err != nil {
			return "", err
		}
		switch {
		case s != "":
			return s, nil
		case current != "":
			return current, nil
		}
		p.line(dim("  this one's required"))
	}
}

// hidden reads a line without echoing it when stdin is a terminal.
func (p *prompter) hidden() (string, error) {
	if p.tty == nil {
		return p.answer()
	}
	b, err := term.ReadPassword(int(p.tty.Fd()))
	fmt.Fprintln(p.out)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// yesNo asks a yes/no question.
func (p *prompter) yesNo(q string, def bool) (bool, error) {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	for {
		p.question(q + " " + dim("["+hint+"]"))
		s, err := p.answer()
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

// confirm asks the user to type word, for steps that deserve more than a
// y. Anything else is a no.
func (p *prompter) confirm(word, to string) (bool, error) {
	s, err := p.ask("Type "+bold(word)+" to "+to+":", "")
	return s == word, err
}

// choose asks the user to pick one of options by number. def is the index
// enter picks, or -1 for no default.
func (p *prompter) choose(q string, options []string, def int) (int, error) {
	p.line(q)
	for i, o := range options {
		p.line(fmt.Sprintf("  %s %s", bold(fmt.Sprintf("[%d]", i+1)), o))
	}
	prompt := ">"
	if def >= 0 {
		prompt = dim(fmt.Sprintf("[%d]", def+1)) + " >"
	}
	for {
		p.question(prompt)
		s, err := p.answer()
		if err != nil {
			return 0, err
		}
		if s == "" && def >= 0 {
			return def, nil
		}
		var n int
		if _, err := fmt.Sscanf(s, "%d", &n); err == nil && n >= 1 && n <= len(options) {
			return n - 1, nil
		}
		p.line(dim(fmt.Sprintf("  type a number from 1 to %d", len(options))))
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
