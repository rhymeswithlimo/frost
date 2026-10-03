// Package snapshot defines what a snapshot is: a timestamped, immutable list
// of files and the chunks that make them up. It also resolves snapshot
// selectors like "latest" or "3 days ago" and diffs two snapshots.
package snapshot

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/rhymeswithlimo/frost/internal/crypto/bip39"
)

// Snapshot is the small header stored for every backup run.
type Snapshot struct {
	ID    string    `json:"id"`
	Time  time.Time `json:"time"`
	Host  string    `json:"host"`
	Paths []string  `json:"paths"`
	Stats Stats     `json:"stats"`
	// Warnings are the first of the items that couldn't be read
	// (Stats.Skipped counts them all).
	Warnings []string `json:"warnings,omitempty"`
	// Kept are the first of the files that kept changing while they were
	// read, so the snapshot has their previous copy (Stats.Kept counts them
	// all).
	Kept []string `json:"kept,omitempty"`
	// Missing are configured paths that weren't there, like a drive that
	// isn't plugged in. They're left out of Paths.
	Missing []string `json:"missing,omitempty"`
}

// Stats summarises a run.
type Stats struct {
	Files         int   `json:"files"`
	Dirs          int   `json:"dirs"`
	Bytes         int64 `json:"bytes"`             // total logical size of all files
	NewChunks     int   `json:"new_chunks"`        // chunks uploaded by this run
	NewBytes      int64 `json:"new_bytes"`         // plaintext bytes in those chunks
	UploadedBytes int64 `json:"uploaded_bytes"`    // bytes sent after compression and encryption
	Skipped       int   `json:"skipped,omitempty"` // items that couldn't be read
	Kept          int   `json:"kept,omitempty"`    // busy files that kept their previous copy
}

// Type is the kind of a tree entry.
type Type string

const (
	TypeFile    Type = "file"
	TypeDir     Type = "dir"
	TypeSymlink Type = "symlink"
)

// File is one entry in a snapshot's tree.
type File struct {
	// Path is the absolute source path, with forward slashes. On Windows it
	// keeps the drive, e.g. "C:/Users/me/notes.txt".
	Path    string    `json:"path"`
	Type    Type      `json:"type"`
	Mode    uint32    `json:"mode"`
	ModTime time.Time `json:"mtime"`
	Size    int64     `json:"size,omitempty"`
	Chunks  []string  `json:"chunks,omitempty"`
	Target  string    `json:"target,omitempty"` // symlink target
}

// Tree is the full file list of a snapshot, sorted by path.
type Tree struct {
	Files []File `json:"files"`
}

// Sort orders the tree by path.
func (t *Tree) Sort() {
	slices.SortFunc(t.Files, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
}

// NewID returns a readable ID with 64 random bits across two words and a suffix.
func NewID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	n := binary.LittleEndian.Uint64(b[:])
	w1 := bip39.Words[n%2048]
	w2 := bip39.Words[(n>>11)%2048]
	return fmt.Sprintf("%s-%s-%011x", w1, w2, n>>22)
}

// shortHex is how much of an ID's hex suffix is shown.
const shortHex = 4

// Short is how a snapshot ID is shown when it's alone: its two words and
// the first 4 characters of its suffix, like maple-absurd-3f1c. An ID
// without a longer suffix is returned whole. Storage, restore markers and
// anything that must name one snapshot for good use the full ID.
func Short(id string) string {
	i := strings.LastIndexByte(id, '-')
	if i < 0 || len(id)-i-1 <= shortHex {
		return id
	}
	return id[:i+1+shortHex]
}

// ShortIDs maps full snapshot IDs to the short ones that are shown.
type ShortIDs map[string]string

// Shorten gives every snapshot in snaps a short ID. It's Short(id), with
// more of the suffix where two IDs would otherwise look the same, so each
// short ID starts only its own ID in snaps and Resolve finds it again.
func Shorten(snaps []Snapshot) ShortIDs {
	ids := make([]string, 0, len(snaps))
	for _, s := range snaps {
		ids = append(ids, s.ID)
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	out := make(ShortIDs, len(ids))
	for i, id := range ids {
		// In sorted order, the longest start an ID shares with any
		// other is the one it shares with a neighbour.
		n := len(Short(id))
		if i > 0 {
			n = max(n, commonPrefix(id, ids[i-1])+1)
		}
		if i+1 < len(ids) {
			n = max(n, commonPrefix(id, ids[i+1])+1)
		}
		out[id] = id[:min(n, len(id))]
	}
	return out
}

// Of returns id's short ID, or Short(id) for an ID s doesn't know.
func (s ShortIDs) Of(id string) string {
	if short, ok := s[id]; ok {
		return short
	}
	return Short(id)
}

func commonPrefix(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

// ValidID reports whether s looks like a snapshot ID. It guards object keys
// built from IDs that came off the network.
func ValidID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// SafeRel converts a snapshot path into a relative path that's safe to join
// under a restore target. It strips the root and any drive letter and
// rejects anything containing "..".
func SafeRel(p string) (string, error) {
	if len(p) >= 3 && p[1] == ':' && p[2] == '/' && ((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z')) {
		p = p[:1] + p[2:] // "C:/x" -> "C/x", keeps drives apart
	}
	p = strings.TrimLeft(p, "/")
	// On Windows a backslash is a separator too, so "a/..\..\x" would climb
	// out of the target there. Elsewhere it's an ordinary file name character.
	sep := func(r rune) bool { return r == '/' || (r == '\\' && runtime.GOOS == "windows") }
	if slices.Contains(strings.FieldsFunc(p, sep), "..") {
		return "", fmt.Errorf("unsafe path %q", p)
	}
	clean := path.Clean(p)
	if !filepath.IsLocal(filepath.FromSlash(clean)) || strings.IndexByte(clean, 0) >= 0 {
		return "", fmt.Errorf("unsafe path %q", p)
	}
	if runtime.GOOS == "windows" {
		for _, part := range strings.Split(filepath.ToSlash(clean), "/") {
			if strings.Contains(part, ":") || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
				return "", fmt.Errorf("unsafe Windows path %q", p)
			}
		}
	}
	if clean == "." || clean == "" || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", fmt.Errorf("unsafe path %q", p)
	}
	return clean, nil
}
