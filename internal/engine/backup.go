// Package engine runs backups, restores and verification on top of a
// repository and its local manifest.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rhymeswithlimo/frost/internal/chunker"
	"github.com/rhymeswithlimo/frost/internal/crypto"
	"github.com/rhymeswithlimo/frost/internal/manifest"
	"github.com/rhymeswithlimo/frost/internal/repo"
	"github.com/rhymeswithlimo/frost/internal/snapshot"
)

// Engine ties a repository to its manifest.
type Engine struct {
	Repo     *repo.Repo
	Manifest *manifest.Manifest
	// Uploaders is how many chunks upload in parallel. Defaults to 4.
	Uploaders int
}

// Meta keys stored in the manifest.
const (
	metaSynced     = "chunks_synced"
	metaLastBackup = "last_backup"
	metaVerify     = "verify"
)

// LastRun records the outcome of the most recent backup attempt.
type LastRun struct {
	Time       time.Time `json:"time"`
	SnapshotID string    `json:"snapshot_id,omitempty"`
	Error      string    `json:"error,omitempty"`
	Skipped    int       `json:"skipped,omitempty"` // items that couldn't be read
}

// BackupOptions controls one backup run.
type BackupOptions struct {
	Paths   []string
	Exclude []string
	DryRun  bool
	// Progress, if set, is called from the scanning goroutine as work happens.
	Progress func(Progress)
}

// Progress is a running tally during a backup.
type Progress struct {
	Path          string
	Files         int
	Bytes         int64
	NewBytes      int64
	UploadedBytes int64
}

// PlannedFile is a file with data that isn't in the repository yet.
type PlannedFile struct {
	Path     string
	Size     int64
	NewBytes int64
}

// BackupResult is what a run produced.
type BackupResult struct {
	Snapshot snapshot.Snapshot
	// Planned lists files that have new data. It's filled in for dry runs.
	Planned []PlannedFile
}

// Backup snapshots opts.Paths. In a dry run it reads and chunks everything
// but uploads nothing and saves nothing.
func (e *Engine) Backup(ctx context.Context, opts BackupOptions) (res BackupResult, err error) {
	if len(opts.Paths) == 0 {
		return res, errors.New("nothing to back up: no paths configured")
	}
	if !opts.DryRun {
		defer func() {
			run := LastRun{Time: time.Now(), SnapshotID: res.Snapshot.ID, Skipped: len(res.Snapshot.Warnings)}
			if err != nil {
				run.Error = err.Error()
			}
			e.Manifest.PutMeta(metaLastBackup, run)
		}()
	}
	if err := e.syncChunks(ctx); err != nil {
		return res, fmt.Errorf("reading repository chunk list: %w", err)
	}

	host, _ := os.Hostname()
	snap := snapshot.Snapshot{ID: snapshot.NewID(), Time: time.Now().UTC(), Host: host}
	ex := newExcluder(opts.Exclude)

	b := &run{
		e:       e,
		dryRun:  opts.DryRun,
		table:   chunker.NewTable(e.Repo.Key.ChunkerSeed()),
		pending: map[crypto.ID]bool{},
		done:    map[crypto.ID]int{},
		files:   map[string]manifest.FileEntry{},
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	b.startUploaders(ctx, cancel)

	var tree snapshot.Tree
	report := func(p string) {
		if opts.Progress != nil {
			b.mu.Lock()
			pr := Progress{Path: p, Files: snap.Stats.Files, Bytes: snap.Stats.Bytes, NewBytes: b.newBytes, UploadedBytes: b.uploaded}
			b.mu.Unlock()
			opts.Progress(pr)
		}
	}

	for _, root := range opts.Paths {
		abs, err := filepath.Abs(root)
		if err != nil {
			return res, err
		}
		snap.Paths = append(snap.Paths, filepath.ToSlash(abs))
		walkErr := filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return context.Cause(ctx)
			}
			if err != nil {
				// A configured directory that can't be read is a failed
				// backup, not a warning: otherwise it looks fine while
				// silently backing up nothing.
				if p == abs {
					return fmt.Errorf("can't read %s: %w", abs, err)
				}
				snap.Warnings = append(snap.Warnings, err.Error())
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if p != abs && ex.match(p) {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			info, err := d.Info()
			if err != nil {
				snap.Warnings = append(snap.Warnings, err.Error())
				return nil
			}
			f := snapshot.File{Path: filepath.ToSlash(p), Mode: uint32(info.Mode().Perm()), ModTime: info.ModTime().UTC()}
			switch {
			case d.IsDir():
				f.Type = snapshot.TypeDir
				snap.Stats.Dirs++
			case d.Type()&fs.ModeSymlink != 0:
				f.Type = snapshot.TypeSymlink
				if f.Target, err = os.Readlink(p); err != nil {
					snap.Warnings = append(snap.Warnings, err.Error())
					return nil
				}
			case d.Type().IsRegular():
				f.Type = snapshot.TypeFile
				f.Size = info.Size()
				planned, err := b.file(ctx, p, &f)
				if err != nil {
					if ctx.Err() != nil {
						return context.Cause(ctx)
					}
					snap.Warnings = append(snap.Warnings, err.Error())
					return nil
				}
				if planned.NewBytes > 0 {
					res.Planned = append(res.Planned, planned)
				}
				snap.Stats.Files++
				snap.Stats.Bytes += f.Size
				report(p)
			default:
				return nil // sockets, devices, pipes: skip
			}
			tree.Files = append(tree.Files, f)
			return nil
		})
		if walkErr != nil {
			cancel(walkErr)
			break
		}
	}

	if err := b.finish(); err != nil {
		return res, err
	}
	if ctx.Err() != nil {
		return res, context.Cause(ctx)
	}
	snap.Stats.NewChunks = len(b.pending)
	snap.Stats.NewBytes = b.newBytes
	snap.Stats.UploadedBytes = b.uploaded
	res.Snapshot = snap
	if opts.DryRun {
		return res, nil
	}

	tree.Sort()
	if err := e.Repo.SaveSnapshot(ctx, snap, &tree); err != nil {
		return res, err
	}
	if err := e.Manifest.PutFiles(b.files); err != nil {
		return res, err
	}
	known := e.Manifest.Snapshots()
	known[snap.ID] = snap
	all := make([]snapshot.Snapshot, 0, len(known))
	for _, s := range known {
		all = append(all, s)
	}
	return res, e.Manifest.SetSnapshots(all)
}

// syncChunks fills the manifest's chunk list from the repository the first
// time this manifest is used, e.g. on a new machine after `frost key import`.
func (e *Engine) syncChunks(ctx context.Context) error {
	var synced bool
	if e.Manifest.GetMeta(metaSynced, &synced) && synced {
		return nil
	}
	ids, err := e.Repo.ChunkIDs(ctx)
	if err != nil {
		return err
	}
	if err := e.Manifest.ReplaceChunks(ids); err != nil {
		return err
	}
	return e.Manifest.PutMeta(metaSynced, true)
}

// run is the state of one backup in progress.
type run struct {
	e      *Engine
	dryRun bool
	table  *chunker.Table

	uploads chan upload
	wg      sync.WaitGroup

	mu       sync.Mutex
	pending  map[crypto.ID]bool // new chunks seen this run
	done     map[crypto.ID]int  // uploaded, not yet written to the manifest
	files    map[string]manifest.FileEntry
	newBytes int64
	uploaded int64
}

type upload struct {
	id   crypto.ID
	data []byte
}

func (b *run) startUploaders(ctx context.Context, fail context.CancelCauseFunc) {
	n := b.e.Uploaders
	if n <= 0 {
		n = 4
	}
	b.uploads = make(chan upload, n)
	for range n {
		b.wg.Add(1)
		go func() {
			defer b.wg.Done()
			for u := range b.uploads {
				if ctx.Err() != nil {
					continue // drain
				}
				sent, err := b.e.Repo.PutChunk(ctx, u.id, u.data)
				if err != nil {
					fail(fmt.Errorf("upload failed: %w", err))
					continue
				}
				b.mu.Lock()
				b.done[u.id] = len(u.data)
				b.uploaded += int64(sent)
				var batch map[crypto.ID]int
				if len(b.done) >= 64 {
					batch, b.done = b.done, map[crypto.ID]int{}
				}
				b.mu.Unlock()
				// Recording as we go means an interrupted run doesn't
				// re-upload everything next time. finish() writes the rest.
				if batch != nil {
					if err := b.e.Manifest.AddChunks(batch); err != nil {
						fail(err)
					}
				}
			}
		}()
	}
}

// finish waits for uploads and records them in the manifest.
func (b *run) finish() error {
	close(b.uploads)
	b.wg.Wait()
	if b.dryRun {
		return nil
	}
	return b.e.Manifest.AddChunks(b.done)
}

// file chunks one regular file, queueing any new chunks for upload, and
// fills in f.Chunks.
func (b *run) file(ctx context.Context, p string, f *snapshot.File) (PlannedFile, error) {
	planned := PlannedFile{Path: f.Path, Size: f.Size}

	if prev, ok := b.e.Manifest.File(f.Path); ok && prev.Size == f.Size && prev.ModTime.Equal(f.ModTime) && b.allKnown(prev.Chunks) {
		f.Chunks = prev.Chunks
		b.remember(f)
		return planned, nil
	}

	fh, err := os.Open(p)
	if err != nil {
		return planned, err
	}
	defer fh.Close()

	c := chunker.New(fh, b.table)
	for {
		data, err := c.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return planned, fmt.Errorf("reading %s: %w", p, err)
		}
		id := b.e.Repo.Key.ChunkID(data)
		f.Chunks = append(f.Chunks, id.String())

		if b.e.Manifest.HasChunk(id) {
			continue
		}
		b.mu.Lock()
		seen := b.pending[id]
		b.pending[id] = true
		if !seen {
			b.newBytes += int64(len(data))
		}
		b.mu.Unlock()
		if seen {
			continue
		}
		planned.NewBytes += int64(len(data))
		if b.dryRun {
			continue
		}
		select {
		case b.uploads <- upload{id: id, data: append([]byte(nil), data...)}:
		case <-ctx.Done():
			return planned, context.Cause(ctx)
		}
	}
	b.remember(f)
	return planned, nil
}

func (b *run) remember(f *snapshot.File) {
	b.files[f.Path] = manifest.FileEntry{Size: f.Size, ModTime: f.ModTime, Chunks: f.Chunks}
}

func (b *run) allKnown(chunks []string) bool {
	for _, c := range chunks {
		id, err := crypto.ParseID(c)
		if err != nil || !b.e.Manifest.HasChunk(id) {
			return false
		}
	}
	return true
}
