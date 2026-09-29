//go:build !windows

package update

import (
	"os"
	"path/filepath"
	"syscall"
)

// replace moves staged over exe in one rename. A process already running
// the old binary keeps its copy.
func replace(staged, exe string) error {
	if err := os.Rename(staged, exe); err != nil {
		return err
	}
	if d, err := os.Open(filepath.Dir(exe)); err == nil {
		d.Sync() // make the rename survive a power cut
		d.Close()
	}
	return nil
}

// keepOwner gives the staged file exe's owner. It only works as root, which
// is the case where it matters: `sudo frost update` shouldn't leave a
// root-owned binary behind.
func keepOwner(exe, staged string) {
	fi, err := os.Stat(exe)
	if err != nil {
		return
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		os.Chown(staged, int(st.Uid), int(st.Gid))
	}
}
