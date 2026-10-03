package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"hash/fnv"
	"slices"
	"time"

	"github.com/rhymeswithlimo/frost/internal/snapshot"
)

// metaLastSaved holds the lastSaved record.
const metaLastSaved = "last_saved"

// lastSaved is the last snapshot this machine saved, kept so the next
// backup can tell whether anything changed since.
type lastSaved struct {
	ID    string   `json:"id"`
	Paths []string `json:"paths"`
	// Digest is a SHA-256 of the paths and the file list (see listDigest).
	// It alone decides whether a backup is unchanged.
	Digest []byte `json:"digest"`
	// Entries is 16 bytes per file list entry, sorted: a 64-bit hash of
	// its path, with the top bit set for folders, then a 64-bit hash of the
	// entry. It's only for counting what changed.
	Entries []byte `json:"entries"`
}

// Changes counts what's different from the last snapshot.
type Changes struct {
	Files   ChangeCount // files and symlinks
	Folders ChangeCount
}

// ChangeCount is what was added, changed and removed.
type ChangeCount struct{ Added, Changed, Removed int }

// None reports whether nothing was counted.
func (c Changes) None() bool { return c == Changes{} }

// listDigest returns a digest of what a snapshot of paths with tree t
// holds, and the entries for counting changes. A folder's mtime is left
// out: it changes whenever something inside comes or goes, including
// excluded and temporary files, and a change that matters shows up in the
// entries themselves. t must be sorted.
func listDigest(paths []string, t *snapshot.Tree) (digest, entries []byte) {
	h := sha256.New()
	for _, p := range paths {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	h.Write([]byte{1})
	type entry struct{ key, sum uint64 }
	list := make([]entry, 0, len(t.Files))
	for _, f := range t.Files {
		if f.Type == snapshot.TypeDir {
			f.ModTime = time.Time{}
		}
		raw, _ := json.Marshal(f) // a File always encodes
		h.Write(raw)
		h.Write([]byte{'\n'})
		key, sum := fnv.New64a(), fnv.New64a()
		key.Write([]byte(f.Path))
		sum.Write(raw)
		e := entry{key: key.Sum64() &^ (1 << 63), sum: sum.Sum64()}
		if f.Type == snapshot.TypeDir {
			e.key |= 1 << 63
		}
		list = append(list, e)
	}
	slices.SortFunc(list, func(a, b entry) int {
		switch {
		case a.key < b.key:
			return -1
		case a.key > b.key:
			return 1
		}
		return 0
	})
	entries = make([]byte, 0, 16*len(list))
	for _, e := range list {
		entries = binary.BigEndian.AppendUint64(entries, e.key)
		entries = binary.BigEndian.AppendUint64(entries, e.sum)
	}
	return h.Sum(nil), entries
}

// countChanges compares two sets of entries from listDigest.
func countChanges(was, now []byte) Changes {
	var c Changes
	count := func(key uint64) *ChangeCount {
		if key&(1<<63) != 0 {
			return &c.Folders
		}
		return &c.Files
	}
	at := func(b []byte, i int) (key, sum uint64) {
		return binary.BigEndian.Uint64(b[i:]), binary.BigEndian.Uint64(b[i+8:])
	}
	i, j := 0, 0
	for i+16 <= len(was) || j+16 <= len(now) {
		switch {
		case j+16 > len(now):
			k, _ := at(was, i)
			count(k).Removed++
			i += 16
		case i+16 > len(was):
			k, _ := at(now, j)
			count(k).Added++
			j += 16
		default:
			wk, ws := at(was, i)
			nk, ns := at(now, j)
			switch {
			case wk < nk:
				count(wk).Removed++
				i += 16
			case wk > nk:
				count(nk).Added++
				j += 16
			default:
				if ws != ns {
					count(nk).Changed++
				}
				i, j = i+16, j+16
			}
		}
	}
	return c
}

// compare checks this run against the last snapshot this machine saved.
// It fills in res.Changes when the last one covered the same folders, and
// returns that snapshot when nothing changed since, so no new one is
// needed. Anything it can't be sure of means a snapshot is saved: no
// record (a new or rebuilt manifest), other folders, a newer snapshot it
// knows of (so `latest` keeps meaning the newest backup), or a header
// that's gone from storage. The list of snapshots it checks against is
// refreshed by verification, so on a repository shared between machines
// it can be out of date until the next check.
func (e *Engine) compare(ctx context.Context, snap snapshot.Snapshot, digest, entries []byte, res *BackupResult) (snapshot.Snapshot, bool) {
	var last lastSaved
	if !e.Manifest.GetMeta(metaLastSaved, &last) || !slices.Equal(last.Paths, snap.Paths) {
		return snapshot.Snapshot{}, false
	}
	res.Changes, res.Compared = countChanges(last.Entries, entries), true
	if !bytes.Equal(last.Digest, digest) {
		return snapshot.Snapshot{}, false
	}
	known := e.Manifest.Snapshots()
	prev, ok := known[last.ID]
	if !ok {
		return snapshot.Snapshot{}, false
	}
	for _, s := range known {
		if s.Time.After(prev.Time) {
			return snapshot.Snapshot{}, false
		}
	}
	if _, err := e.Repo.LoadSnapshot(ctx, last.ID); err != nil {
		return snapshot.Snapshot{}, false
	}
	return prev, true
}

// verifyEvery is how often a backup that saved nothing new still runs its
// spot check.
const verifyEvery = 24 * time.Hour

// VerifyDue reports whether a backup that saved nothing new should still
// run its spot check: when there's no earlier result, the last one found a
// problem, or it's more than a day old. A backup that saves a snapshot
// always checks.
func (e *Engine) VerifyDue() bool {
	v, ok := e.LastVerify()
	age := time.Since(v.Time)
	return !ok || !v.OK() || age < 0 || age > verifyEvery
}
