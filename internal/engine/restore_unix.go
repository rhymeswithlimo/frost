//go:build !windows

package engine

import (
	"io/fs"
	"os"
	"syscall"
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
