//go:build !windows

package engine

import (
	"errors"
	"io/fs"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// trustedLink says whether an in-place restore may follow a link above what
// it writes: one owned by root, like macOS's /var, or by you, like a
// ~/Dropbox pointing at another drive. Anyone else's could send the restore
// somewhere else, so it's refused. It's a variable so tests can distrust
// links.
var trustedLink = defaultTrustedLink

func defaultTrustedLink(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && (st.Uid == 0 || int(st.Uid) == os.Getuid())
}

func resolveParent(dir string) (string, error) { return resolveTrusted(dir, trustedLink) }

// lockFile takes an exclusive lock on f without waiting. It's released when
// f is closed or the process ends. It returns errBusy if another process
// holds it. Filesystems that can't lock are let through.
func lockFile(f *os.File) error {
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); errors.Is(err, unix.EWOULDBLOCK) {
		return errBusy
	}
	return nil
}
