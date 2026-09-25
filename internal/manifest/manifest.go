// Package manifest is frost's local record of what's already been uploaded.
//
// It's a cache, not a source of truth. If it's deleted, the next backup
// rebuilds the chunk list from the repository and simply re-reads files once.
// It's a single bbolt file, which also acts as a lock: two frost processes
// can't back up to the same repository at the same time.
package manifest

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"
	berrors "go.etcd.io/bbolt/errors"

	"github.com/rhymeswithlimo/frost/internal/crypto"
	"github.com/rhymeswithlimo/frost/internal/snapshot"
)

var (
	bChunks = []byte("chunks")    // chunk id -> plaintext size
	bFiles  = []byte("files")     // source path -> FileEntry
	bSnaps  = []byte("snapshots") // snapshot id -> Snapshot header
	bMeta   = []byte("meta")      // name -> JSON
)

// ErrLocked means another frost process has the manifest open.
var ErrLocked = errors.New("another frost process is running against this repository")

// Manifest is an open manifest database.
type Manifest struct{ db *bolt.DB }

// FileEntry remembers how a file was chunked last time, so an unchanged file
// (same size and mtime) can skip being read and chunked again.
type FileEntry struct {
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mtime"`
	Chunks  []string  `json:"chunks"`
}

// Open opens or creates the manifest at path.
func Open(path string) (*Manifest, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
	if errors.Is(err, berrors.ErrTimeout) {
		return nil, ErrLocked
	}
	if err != nil {
		return nil, err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{bChunks, bFiles, bSnaps, bMeta} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Manifest{db: db}, nil
}

// Close releases the database and its lock.
func (m *Manifest) Close() error { return m.db.Close() }

// HasChunk reports whether a chunk is known to be uploaded.
func (m *Manifest) HasChunk(id crypto.ID) bool {
	var ok bool
	m.db.View(func(tx *bolt.Tx) error {
		ok = tx.Bucket(bChunks).Get(id[:]) != nil
		return nil
	})
	return ok
}

// AddChunks records chunks as uploaded.
func (m *Manifest) AddChunks(chunks map[crypto.ID]int) error {
	if len(chunks) == 0 {
		return nil
	}
	return m.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bChunks)
		var v [4]byte
		for id, size := range chunks {
			binary.LittleEndian.PutUint32(v[:], uint32(size))
			if err := b.Put(id[:], v[:]); err != nil {
				return err
			}
		}
		return nil
	})
}

// ReplaceChunks throws away the chunk list and sets it to ids. Used to
// rebuild from the repository. Sizes are unknown after a rebuild.
func (m *Manifest) ReplaceChunks(ids []crypto.ID) error {
	return m.db.Update(func(tx *bolt.Tx) error {
		if err := tx.DeleteBucket(bChunks); err != nil {
			return err
		}
		b, err := tx.CreateBucket(bChunks)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := b.Put(id[:], []byte{0, 0, 0, 0}); err != nil {
				return err
			}
		}
		return nil
	})
}

// ChunkCount returns the number of known chunks.
func (m *Manifest) ChunkCount() int {
	var n int
	m.db.View(func(tx *bolt.Tx) error {
		n = tx.Bucket(bChunks).Stats().KeyN
		return nil
	})
	return n
}

// SampleChunks picks up to n known chunk IDs uniformly at random.
func (m *Manifest) SampleChunks(n int) []crypto.ID {
	var out []crypto.ID
	seen := 0
	m.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bChunks).ForEach(func(k, _ []byte) error {
			var id crypto.ID
			copy(id[:], k)
			seen++
			if len(out) < n {
				out = append(out, id)
			} else if j := rand.IntN(seen); j < n {
				out[j] = id
			}
			return nil
		})
	})
	return out
}

// File returns the cached entry for a source path.
func (m *Manifest) File(path string) (FileEntry, bool) {
	var e FileEntry
	var ok bool
	m.db.View(func(tx *bolt.Tx) error {
		if v := tx.Bucket(bFiles).Get([]byte(path)); v != nil {
			ok = json.Unmarshal(v, &e) == nil
		}
		return nil
	})
	return e, ok
}

// PutFiles stores file entries.
func (m *Manifest) PutFiles(entries map[string]FileEntry) error {
	return m.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bFiles)
		for p, e := range entries {
			v, err := json.Marshal(e)
			if err != nil {
				return err
			}
			if err := b.Put([]byte(p), v); err != nil {
				return err
			}
		}
		return nil
	})
}

// Snapshots returns every cached snapshot header, keyed by ID.
func (m *Manifest) Snapshots() map[string]snapshot.Snapshot {
	out := map[string]snapshot.Snapshot{}
	m.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bSnaps).ForEach(func(k, v []byte) error {
			var s snapshot.Snapshot
			if json.Unmarshal(v, &s) == nil {
				out[string(k)] = s
			}
			return nil
		})
	})
	return out
}

// SetSnapshots replaces the cached snapshot headers.
func (m *Manifest) SetSnapshots(snaps []snapshot.Snapshot) error {
	return m.db.Update(func(tx *bolt.Tx) error {
		if err := tx.DeleteBucket(bSnaps); err != nil {
			return err
		}
		b, err := tx.CreateBucket(bSnaps)
		if err != nil {
			return err
		}
		for _, s := range snaps {
			v, err := json.Marshal(s)
			if err != nil {
				return err
			}
			if err := b.Put([]byte(s.ID), v); err != nil {
				return err
			}
		}
		return nil
	})
}

// GetMeta decodes the JSON value stored under name into v. It returns false
// if nothing is stored.
func (m *Manifest) GetMeta(name string, v any) bool {
	var ok bool
	m.db.View(func(tx *bolt.Tx) error {
		if raw := tx.Bucket(bMeta).Get([]byte(name)); raw != nil {
			ok = json.Unmarshal(raw, v) == nil
		}
		return nil
	})
	return ok
}

// PutMeta stores v as JSON under name.
func (m *Manifest) PutMeta(name string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return m.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bMeta).Put([]byte(name), raw)
	})
}
