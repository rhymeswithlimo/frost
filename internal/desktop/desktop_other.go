//go:build !windows

package desktop

import (
	"os/exec"
	"path/filepath"
	"runtime"
)

func reveal(path string) error {
	if runtime.GOOS == "darwin" {
		return start(exec.Command("open", "-R", path))
	}
	// There's no standard way to select a file on Linux, so open its folder.
	return Open(filepath.Dir(path))
}
