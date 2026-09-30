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
	children map[string][]string  // parent path -> child paths, folders first
	totals   map[string]fileTotal // regular files and bytes under each path
	roots    []string             // the backed-up directories
}

type fileTotal struct {
	files int
	bytes int64
}

// rootKey is the parent of the backed-up directories.
const rootKey = ""

func newTree(s snapshot.Snapshot, t *snapshot.Tree) *tree {
	x := &tree{
		files:    make(map[string]*snapshot.File, len(t.Files)),
		children: map[string][]string{},
		totals:   make(map[string]fileTotal, len(t.Files)),
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
				total := x.totals[q]
				total.bytes += f.Size
				total.files++
				x.totals[q] = total
				if isRoot[q] || path.Dir(q) == q {
					break
				}
			}
		}
	}
	for k, kids := range x.children {
		if len(kids) < 2 {
			continue
		}
		// Compute case-folded names once per entry, not twice on every
		// comparison. Large folders otherwise allocate millions of strings.
		type child struct {
			path, name string
			dir        bool
		}
		ordered := make([]child, len(kids))
		for i, p := range kids {
			ordered[i] = child{p, strings.ToLower(path.Base(p)), x.isDir(p)}
		}
		slices.SortFunc(ordered, func(a, b child) int {
			if a.dir != b.dir {
				if a.dir {
					return -1
				}
				return 1
			}
			if cmp := strings.Compare(a.name, b.name); cmp != 0 {
				return cmp
			}
			return strings.Compare(a.path, b.path) // stable order for case-only differences
		})
		for i, c := range ordered {
			kids[i] = c.path
		}
		x.children[k] = kids
	}
	return x
}

// selectionTotals counts each selected subtree once, including when callers
// supply both a folder and one of its descendants.
func (x *tree) selectionTotals(sel map[string]bool) (files int, bytes int64) {
	for p, selected := range sel {
		if !selected {
			continue
		}
		parent := path.Dir(p)
		if parent != p && covered(parent, sel) {
			continue
		}
		total := x.totals[p]
		files += total.files
		bytes += total.bytes
	}
	return
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
