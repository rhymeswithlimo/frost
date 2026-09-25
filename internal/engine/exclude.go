package engine

import (
	"path"
	"path/filepath"
	"strings"
)

// excluder matches paths against exclude patterns.
//
// A pattern without a slash, like "*.tmp" or "node_modules", matches any file
// or directory with that name anywhere. A pattern with a slash, like
// "/home/me/Downloads" or "/home/*/.cache", matches that path and everything
// under it.
type excluder struct {
	names []string
	paths []string
}

func newExcluder(patterns []string) *excluder {
	e := &excluder{}
	for _, p := range patterns {
		p = strings.TrimSpace(filepath.ToSlash(p))
		switch {
		case p == "":
		case strings.Contains(strings.TrimSuffix(p, "/"), "/"):
			e.paths = append(e.paths, strings.TrimSuffix(p, "/"))
		default:
			e.names = append(e.names, strings.TrimSuffix(p, "/"))
		}
	}
	return e
}

func (e *excluder) match(p string) bool {
	p = filepath.ToSlash(p)
	base := path.Base(p)
	for _, n := range e.names {
		if ok, _ := path.Match(n, base); ok {
			return true
		}
	}
	for _, pat := range e.paths {
		// Check p and each of its parents, so "/a/b" also excludes "/a/b/c".
		for q := p; q != "/" && q != "." && q != ""; q = path.Dir(q) {
			if ok, _ := path.Match(pat, q); ok {
				return true
			}
			if path.Dir(q) == q {
				break
			}
		}
	}
	return false
}
