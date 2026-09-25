package snapshot

import (
	"slices"
	"strings"
)

// ChangeKind says how a path differs between two snapshots.
type ChangeKind string

const (
	Added    ChangeKind = "added"
	Removed  ChangeKind = "removed"
	Modified ChangeKind = "modified"
)

// Change is one differing path. Old is nil for Added, New is nil for Removed.
type Change struct {
	Path string
	Kind ChangeKind
	Old  *File
	New  *File
}

// Diff lists what changed going from tree a to tree b, sorted by path.
// A file whose only difference is its modification time isn't a change.
func Diff(a, b *Tree) []Change {
	old := make(map[string]*File, len(a.Files))
	for i := range a.Files {
		old[a.Files[i].Path] = &a.Files[i]
	}
	var out []Change
	for i := range b.Files {
		nf := &b.Files[i]
		of, ok := old[nf.Path]
		switch {
		case !ok:
			out = append(out, Change{Path: nf.Path, Kind: Added, New: nf})
		case !sameContent(of, nf):
			out = append(out, Change{Path: nf.Path, Kind: Modified, Old: of, New: nf})
		}
		delete(old, nf.Path)
	}
	for p, of := range old {
		out = append(out, Change{Path: p, Kind: Removed, Old: of})
	}
	slices.SortFunc(out, func(x, y Change) int { return strings.Compare(x.Path, y.Path) })
	return out
}

func sameContent(a, b *File) bool {
	return a.Type == b.Type && a.Mode == b.Mode && a.Size == b.Size &&
		a.Target == b.Target && slices.Equal(a.Chunks, b.Chunks)
}
