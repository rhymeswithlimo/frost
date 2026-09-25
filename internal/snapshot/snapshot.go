// Package snapshot defines what a snapshot is: a timestamped, immutable list
// of files and the chunks that make them up. It also resolves snapshot
// selectors like "latest" or "3 days ago" and diffs two snapshots.
package snapshot

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"path"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/rhymeswithlimo/frost/internal/crypto/bip39"
)

// Snapshot is the small header stored for every backup run.
type Snapshot struct {
	ID       string    `json:"id"`
	Time     time.Time `json:"time"`
	Host     string    `json:"host"`
	Paths    []string  `json:"paths"`
	Stats    Stats     `json:"stats"`
	Warnings []string  `json:"warnings,omitempty"`
}

// Stats summarises a run.
type Stats struct {
	Files         int   `json:"files"`
	Dirs          int   `json:"dirs"`
	Bytes         int64 `json:"bytes"`          // total logical size of all files
	NewChunks     int   `json:"new_chunks"`     // chunks uploaded by this run
	NewBytes      int64 `json:"new_bytes"`      // plaintext bytes in those chunks
	UploadedBytes int64 `json:"uploaded_bytes"` // bytes sent after compression and encryption
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

// NewID returns a short, readable ID like "maple-otter-3f1c".
func NewID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	n := binary.LittleEndian.Uint64(b[:])
	w1 := bip39.Words[n%2048]
	w2 := bip39.Words[(n>>11)%2048]
	return fmt.Sprintf("%s-%s-%04x", w1, w2, (n>>22)&0xffff)
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
	if i := strings.IndexByte(p, ':'); i == 1 {
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
	if clean == "." || clean == "" || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", fmt.Errorf("unsafe path %q", p)
	}
	return clean, nil
}
