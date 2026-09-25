// Package repo is the layout of a frost repository inside a storage backend.
//
// Every object is sealed with the user's key, and the object's own key is
// used as associated data, so a blob can't be moved to a different name
// without failing to decrypt.
//
//	frost.repo              repository info, doubles as the key check
//	chunks/<ab>/<abcd...>   file data, named by keyed chunk ID
//	snapshots/<id>          snapshot header (time, paths, stats)
//	trees/<id>              snapshot file list
package repo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rhymeswithlimo/frost/internal/crypto"
	"github.com/rhymeswithlimo/frost/internal/snapshot"
	"github.com/rhymeswithlimo/frost/internal/storage"
)

const (
	infoKey       = "frost.repo"
	layoutVersion = 1
)

var (
	// ErrNotInitialized means the backend has no frost repository yet.
	ErrNotInitialized = errors.New("no frost repository here yet")
	// ErrWrongKey means the repository exists but this key can't open it.
	ErrWrongKey = errors.New("this key doesn't match the repository")
	// ErrCorrupt means a stored chunk didn't match its ID after decryption.
	ErrCorrupt = errors.New("chunk content doesn't match its ID")
)

// Info is stored (encrypted) in frost.repo.
type Info struct {
	Version int       `json:"version"`
	ID      string    `json:"id"`
	Created time.Time `json:"created"`
}

// Repo is an opened repository.
type Repo struct {
	Backend storage.Backend
	Key     *crypto.Key
	Info    Info
}

// Init creates a new repository. It refuses if one already exists.
func Init(ctx context.Context, b storage.Backend, k *crypto.Key) (*Repo, error) {
	if _, err := b.Get(ctx, infoKey); err == nil {
		return nil, fmt.Errorf("%s already has a frost repository (use `frost key import` to connect to it)", b)
	} else if !errors.Is(err, storage.ErrNotFound) {
		return nil, err
	}
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	info := Info{Version: layoutVersion, ID: hex.EncodeToString(id[:]), Created: time.Now().UTC()}
	r := &Repo{Backend: b, Key: k, Info: info}
	if err := r.putJSON(ctx, infoKey, info); err != nil {
		return nil, err
	}
	return r, nil
}

// Exists reports whether b already holds a frost repository.
func Exists(ctx context.Context, b storage.Backend) (bool, error) {
	_, err := b.Get(ctx, infoKey)
	if errors.Is(err, storage.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// Open connects to an existing repository and checks the key against it.
func Open(ctx context.Context, b storage.Backend, k *crypto.Key) (*Repo, error) {
	blob, err := b.Get(ctx, infoKey)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, ErrNotInitialized
	}
	if err != nil {
		return nil, err
	}
	pt, err := k.Open(blob, infoKey)
	if err != nil {
		return nil, ErrWrongKey
	}
	var info Info
	if err := json.Unmarshal(pt, &info); err != nil {
		return nil, fmt.Errorf("reading %s: %w", infoKey, err)
	}
	if info.Version != layoutVersion {
		return nil, fmt.Errorf("repository format v%d isn't supported by this frost (wants v%d)", info.Version, layoutVersion)
	}
	return &Repo{Backend: b, Key: k, Info: info}, nil
}

// ChunkKey is the object key for a chunk.
func ChunkKey(id crypto.ID) string {
	h := id.String()
	return "chunks/" + h[:2] + "/" + h
}

// PutChunk encrypts and uploads a chunk and returns the number of bytes sent.
func (r *Repo) PutChunk(ctx context.Context, id crypto.ID, plaintext []byte) (int, error) {
	key := ChunkKey(id)
	blob := r.Key.Seal(plaintext, key)
	return len(blob), r.Backend.Put(ctx, key, blob)
}

// GetChunk downloads, decrypts and checks a chunk against its ID.
func (r *Repo) GetChunk(ctx context.Context, id crypto.ID) ([]byte, error) {
	key := ChunkKey(id)
	blob, err := r.Backend.Get(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("chunk %s: %w", id.String()[:12], err)
	}
	pt, err := r.Key.Open(blob, key)
	if err != nil {
		return nil, fmt.Errorf("chunk %s: %w", id.String()[:12], err)
	}
	if r.Key.ChunkID(pt) != id {
		return nil, fmt.Errorf("chunk %s: %w", id.String()[:12], ErrCorrupt)
	}
	return pt, nil
}

// ChunkIDs lists every chunk stored in the repository.
func (r *Repo) ChunkIDs(ctx context.Context) ([]crypto.ID, error) {
	keys, err := r.Backend.List(ctx, "chunks/")
	if err != nil {
		return nil, err
	}
	ids := make([]crypto.ID, 0, len(keys))
	for _, k := range keys {
		id, err := crypto.ParseID(k[strings.LastIndexByte(k, '/')+1:])
		if err != nil {
			continue // not ours, ignore
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// SaveSnapshot uploads the tree first and the header second, so a snapshot
// never shows up in listings before its file list exists.
func (r *Repo) SaveSnapshot(ctx context.Context, s snapshot.Snapshot, t *snapshot.Tree) error {
	if err := r.putJSON(ctx, "trees/"+s.ID, t); err != nil {
		return fmt.Errorf("saving file list: %w", err)
	}
	if err := r.putJSON(ctx, "snapshots/"+s.ID, s); err != nil {
		return fmt.Errorf("saving snapshot: %w", err)
	}
	return nil
}

// LoadSnapshot fetches one snapshot header.
func (r *Repo) LoadSnapshot(ctx context.Context, id string) (snapshot.Snapshot, error) {
	var s snapshot.Snapshot
	if !snapshot.ValidID(id) {
		return s, fmt.Errorf("invalid snapshot id %q", id)
	}
	err := r.getJSON(ctx, "snapshots/"+id, &s)
	return s, err
}

// LoadTree fetches a snapshot's file list.
func (r *Repo) LoadTree(ctx context.Context, id string) (*snapshot.Tree, error) {
	if !snapshot.ValidID(id) {
		return nil, fmt.Errorf("invalid snapshot id %q", id)
	}
	var t snapshot.Tree
	if err := r.getJSON(ctx, "trees/"+id, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// SnapshotIDs lists the IDs of every snapshot in the repository.
func (r *Repo) SnapshotIDs(ctx context.Context) ([]string, error) {
	keys, err := r.Backend.List(ctx, "snapshots/")
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(keys))
	for _, k := range keys {
		if id := strings.TrimPrefix(k, "snapshots/"); snapshot.ValidID(id) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// Snapshots returns every snapshot header. Headers already in known are
// reused and only new ones are downloaded.
func (r *Repo) Snapshots(ctx context.Context, known map[string]snapshot.Snapshot) ([]snapshot.Snapshot, error) {
	ids, err := r.SnapshotIDs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]snapshot.Snapshot, len(ids))
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		sem      = make(chan struct{}, 8)
	)
	for i, id := range ids {
		if s, ok := known[id]; ok {
			out[i] = s
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			s, err := r.LoadSnapshot(ctx, id)
			mu.Lock()
			defer mu.Unlock()
			if err != nil && firstErr == nil {
				firstErr = fmt.Errorf("snapshot %s: %w", id, err)
			}
			out[i] = s
		}()
	}
	wg.Wait()
	return out, firstErr
}

func (r *Repo) putJSON(ctx context.Context, key string, v any) error {
	pt, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return r.Backend.Put(ctx, key, r.Key.Seal(pt, key))
}

func (r *Repo) getJSON(ctx context.Context, key string, v any) error {
	blob, err := r.Backend.Get(ctx, key)
	if err != nil {
		return err
	}
	pt, err := r.Key.Open(blob, key)
	if err != nil {
		return err
	}
	return json.Unmarshal(pt, v)
}
