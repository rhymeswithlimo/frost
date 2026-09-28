package snapshot

import (
	"fmt"
	"path"
	"strings"
)

// CommonDir returns the deepest snapshot path that is paths[0] or an
// ancestor of every path, or "" when they share nothing, like two Windows
// drives.
func CommonDir(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	common := strings.Split(paths[0], "/")
	for _, p := range paths[1:] {
		parts := strings.Split(p, "/")
		n := 0
		for n < len(common) && n < len(parts) && common[n] == parts[n] {
			n++
		}
		common = common[:n]
	}
	switch {
	case len(common) == 0:
		return ""
	case len(common) == 1 && common[0] == "":
		return "/"
	case len(common) == 1 && strings.HasSuffix(common[0], ":"):
		return common[0] + "/" // a Windows drive
	}
	return strings.Join(common, "/")
}

// RestoreBase is the folder that restoring paths into a new folder keeps
// them relative to: the deepest folder holding all of their parents, so a
// selected folder keeps its own name. "" means there's no common folder.
func RestoreBase(paths []string) string {
	parents := make([]string, len(paths))
	for i, p := range paths {
		parents[i] = path.Dir(p)
	}
	return CommonDir(parents)
}

// IsRoot reports whether a snapshot path is a filesystem or drive root.
func IsRoot(p string) bool {
	return p == "/" || (len(p) == 3 && p[1] == ':' && p[2] == '/')
}

// RestoreRel is where p goes under a restore target when base is stripped
// from it, checked like SafeRel. p must be inside base. An empty base keeps
// the full path.
func RestoreRel(p, base string) (string, error) {
	if base == "" {
		return SafeRel(p)
	}
	prefix := base
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	if !strings.HasPrefix(p, prefix) || len(p) == len(prefix) {
		return "", fmt.Errorf("%q isn't inside %q", p, base)
	}
	return SafeRel(p[len(prefix):])
}
