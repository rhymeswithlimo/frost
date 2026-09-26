package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// AddPath checks folder p can join paths, the folders already being backed
// up. It returns p tidied up, and the indexes of entries inside p, which
// adding p makes redundant. Errors are written to be shown as they are.
func AddPath(p string, paths []string) (string, []int, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", nil, errors.New("type a folder's path")
	}
	clean := filepath.Clean(p)
	full := Expand(clean)
	if !filepath.IsAbs(full) {
		return "", nil, errors.New("use a full path, like ~/Documents")
	}
	if fi, err := os.Stat(full); err == nil && !fi.IsDir() {
		return "", nil, errors.New("that's a file, frost backs up whole folders")
	}
	var inside []int
	for i, e := range paths {
		ef := Expand(filepath.Clean(e))
		switch {
		case samePath(full, ef):
			return "", nil, fmt.Errorf("%s is already on the list", e)
		case within(full, ef):
			return "", nil, fmt.Errorf("that's already included, it's inside %s", e)
		case within(ef, full):
			inside = append(inside, i)
		}
	}
	return clean, inside, nil
}

// samePath reports whether a and b are the same folder. Existing folders
// are compared by identity, so case differences on macOS and Windows and
// symlinks don't count as different.
func samePath(a, b string) bool {
	if fa, err := os.Stat(a); err == nil {
		if fb, err := os.Stat(b); err == nil {
			return os.SameFile(fa, fb)
		}
	}
	return foldCase(a) == foldCase(b)
}

// within reports whether child is somewhere inside parent.
func within(child, parent string) bool {
	rel, err := filepath.Rel(foldCase(parent), foldCase(child))
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// foldCase lowercases a path on systems whose file names ignore case.
func foldCase(p string) string {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return strings.ToLower(p)
	}
	return p
}
