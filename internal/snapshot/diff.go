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
	if !ordered(a.Files) || !ordered(b.Files) {
		return diffUnsorted(a, b)
	}
	var out []Change
	i, j := 0, 0
	for i < len(a.Files) || j < len(b.Files) {
		switch {
		case i == len(a.Files) || j < len(b.Files) && b.Files[j].Path < a.Files[i].Path:
			nf := &b.Files[j]
			out = append(out, Change{Path: nf.Path, Kind: Added, New: nf})
			j++
		case j == len(b.Files) || a.Files[i].Path < b.Files[j].Path:
			of := &a.Files[i]
			out = append(out, Change{Path: of.Path, Kind: Removed, Old: of})
			i++
		default:
			of, nf := &a.Files[i], &b.Files[j]
			if !sameContent(of, nf) {
				out = append(out, Change{Path: nf.Path, Kind: Modified, Old: of, New: nf})
			}
			i++
			j++
		}
	}
	return out
}

func ordered(files []File) bool {
	for i := 1; i < len(files); i++ {
		if files[i-1].Path >= files[i].Path {
			return false
		}
	}
	return true
}

func diffUnsorted(a, b *Tree) []Change {
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
