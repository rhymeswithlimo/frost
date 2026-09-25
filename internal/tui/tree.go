package tui

import (
	"path"
	"slices"
	"strings"

	"github.com/rhymeswithlimo/frost/internal/snapshot"
)

// tree indexes a snapshot's flat file list so it can be browsed as folders.
type tree struct {
	files    map[string]*snapshot.File
	children map[string][]string // parent path -> child paths, folders first
	sizes    map[string]int64    // total size under each path
	roots    []string            // the backed-up directories
}

// rootKey is the parent of the backed-up directories.
const rootKey = ""

func newTree(s snapshot.Snapshot, t *snapshot.Tree) *tree {
	x := &tree{
		files:    make(map[string]*snapshot.File, len(t.Files)),
		children: map[string][]string{},
		sizes:    map[string]int64{},
	}
	for i := range t.Files {
		f := &t.Files[i]
		x.files[f.Path] = f
	}
	isRoot := map[string]bool{}
	for _, r := range s.Paths {
		if _, ok := x.files[r]; ok {
			isRoot[r] = true
			x.roots = append(x.roots, r)
		}
	}
	for p, f := range x.files {
		parent := rootKey
		if !isRoot[p] {
			parent = path.Dir(p)
		}
		x.children[parent] = append(x.children[parent], p)
		if f.Type == snapshot.TypeFile {
			// Add the size to every ancestor up to the root.
			for q := p; ; q = path.Dir(q) {
				x.sizes[q] += f.Size
				if isRoot[q] || path.Dir(q) == q {
					break
				}
			}
		}
	}
	for k, kids := range x.children {
		slices.SortFunc(kids, func(a, b string) int {
			da, db := x.isDir(a), x.isDir(b)
			if da != db {
				if da {
					return -1
				}
				return 1
			}
			return strings.Compare(strings.ToLower(path.Base(a)), strings.ToLower(path.Base(b)))
		})
		x.children[k] = kids
	}
	return x
}

func (x *tree) isDir(p string) bool {
	f, ok := x.files[p]
	return ok && f.Type == snapshot.TypeDir
}

// parent returns the folder to go "up" to from dir.
func (x *tree) parent(dir string) string {
	if slices.Contains(x.roots, dir) || dir == rootKey {
		return rootKey
	}
	return path.Dir(dir)
}

// covered reports whether p or any of its ancestors is in sel.
func covered(p string, sel map[string]bool) bool {
	for q := p; ; q = path.Dir(q) {
		if sel[q] {
			return true
		}
		if path.Dir(q) == q {
			return false
		}
	}
}
