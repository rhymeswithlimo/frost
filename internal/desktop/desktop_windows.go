package desktop

import (
	"os/exec"
	"syscall"
)

func reveal(path string) error {
	// Go would quote "/select,<path>" as one argument, which explorer
	// misreads when the path has spaces, so write the command line by hand.
	// explorer exits with 1 even when it worked; start doesn't check.
	cmd := exec.Command("explorer")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer /select,"` + path + `"`}
	return start(cmd)
}
