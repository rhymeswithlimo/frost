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
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rhymeswithlimo/frost/internal/chunker"
	"github.com/rhymeswithlimo/frost/internal/crypto"
	"github.com/rhymeswithlimo/frost/internal/manifest"
	"github.com/rhymeswithlimo/frost/internal/repo"
	"github.com/rhymeswithlimo/frost/internal/snapshot"
	"github.com/rhymeswithlimo/frost/internal/storage"
)

// Engine ties a repository to its manifest.
type Engine struct {
	Repo     *repo.Repo
	Manifest *manifest.Manifest
	// Uploaders is how many chunks upload in parallel. Defaults to 4.
	Uploaders int
	// Downloaders is how many chunks download in parallel during restores
	// and verification. Defaults to 8.
	Downloaders int
}

func (e *Engine) downloaders() int {
	if e.Downloaders > 0 {
		return e.Downloaders
	}
	return 8
}

// Meta keys stored in the manifest.
const (
	metaLastBackup = "last_backup"
	metaVerify     = "verify"
	metaChunkSync  = "chunk_sync"
)

// LastRun records the outcome of the most recent backup attempt.
type LastRun struct {
	Time       time.Time `json:"time"`
	SnapshotID string    `json:"snapshot_id,omitempty"`
	Error      string    `json:"error,omitempty"`
	Skipped    int       `json:"skipped,omitempty"` // items that couldn't be read
	Kept       int       `json:"kept,omitempty"`    // busy files that kept their previous copy
	Missing    []string  `json:"missing,omitempty"` // configured paths that weren't there
}

// maxListed caps the warnings and kept files listed in a snapshot header.
// Headers are fetched for every listing, so they stay small; the counts in
// Stats are complete.
const maxListed = 100

// changedError means a file changed while it was being read.
type changedError struct{ path string }

func (e *changedError) Error() string { return "file changed while reading " + e.path }

// chunkRead, if set, is called after each chunk of a file is read. Tests
// use it to change files mid-read.
var chunkRead func(path string)

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

// Backup snapshots opts.Paths. A dry run refreshes chunk presence and scans
// files, reusing unchanged entries, without uploading or saving a snapshot.
func (e *Engine) Backup(ctx context.Context, opts BackupOptions) (res BackupResult, err error) {
	if len(opts.Paths) == 0 {
		return res, errors.New("nothing to back up: no paths configured")
	}
	for _, root := range opts.Paths {
		if strings.TrimSpace(root) == "" {
			return res, errors.New("backup paths can't contain an empty entry")
		}
	}
	for _, pattern := range opts.Exclude {
		if _, err := path.Match(filepath.ToSlash(pattern), ""); err != nil {
			return res, fmt.Errorf("invalid exclude pattern %q: %w", pattern, err)
		}
	}
	if !opts.DryRun {
		defer func() {
			run := LastRun{Time: time.Now(), SnapshotID: res.Snapshot.ID, Skipped: res.Snapshot.Stats.Skipped, Kept: res.Snapshot.Stats.Kept, Missing: res.Snapshot.Missing}
			if err != nil {
				run.Error = err.Error()
			}
			err = errors.Join(err, e.Manifest.PutMeta(metaLastBackup, run))
		}()
	}
	if e.syncDue() {
		if err := e.syncChunks(ctx); err != nil {
			return res, fmt.Errorf("reading repository chunk list: %w", err)
		}
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
	visited := make(map[string]bool)
	report := func(p string) {
		if opts.Progress != nil {
			b.mu.Lock()
			pr := Progress{Path: p, Files: snap.Stats.Files, Bytes: snap.Stats.Bytes, NewBytes: b.newBytes, UploadedBytes: b.uploaded}
			b.mu.Unlock()
			opts.Progress(pr)
		}
	}
	warn := func(msg string) {
		snap.Stats.Skipped++
		if len(snap.Warnings) < maxListed {
			snap.Warnings = append(snap.Warnings, msg)
		}
	}
	addFile := func(p string, f snapshot.File, planned PlannedFile) {
		if planned.NewBytes > 0 {
			res.Planned = append(res.Planned, planned)
		}
		snap.Stats.Files++
		snap.Stats.Bytes += f.Size
		tree.Files = append(tree.Files, f)
		report(p)
	}
	// Files that changed while they were read get one more try after the
	// walk, when whatever was writing them may have finished.
	type retry struct {
		p string
		f snapshot.File
	}
	var retries []retry

	for _, root := range opts.Paths {
		abs, err := filepath.Abs(root)
		if err != nil {
			cancel(err)
			break
		}
		// A path that isn't there, like an unplugged drive or a moved
		// folder, is skipped so the others still get backed up. Older
		// snapshots keep its files. Any other problem with it (no
		// permission, say) still fails the backup below.
		if _, err := os.Lstat(abs); errors.Is(err, fs.ErrNotExist) {
			snap.Missing = append(snap.Missing, filepath.ToSlash(abs))
			continue
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
				warn(err.Error())
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if p != abs && d.Type().IsRegular() && isPartial(d.Name()) {
				return nil // left by a restore that stopped partway over the originals
			}
			if p != abs && ex.match(p) {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if visited[p] {
				return nil
			}
			visited[p] = true
			info, err := d.Info()
			if err != nil {
				if p == abs {
					return fmt.Errorf("can't read %s: %w", abs, err)
				}
				warn(err.Error())
				return nil
			}
			f := snapshot.File{Path: filepath.ToSlash(p), Mode: uint32(info.Mode().Perm()), ModTime: info.ModTime().UTC()}
			switch {
			case d.IsDir():
				f.Type = snapshot.TypeDir
				snap.Stats.Dirs++
			case d.Type()&fs.ModeSymlink != 0:
				f.Type = snapshot.TypeSymlink
				target, err := os.Readlink(p)
				if err != nil {
					warn(err.Error())
					return nil
				}
				// Stored with forward slashes, like paths, so links restore on any OS.
				f.Target = filepath.ToSlash(target)
			case d.Type().IsRegular():
				f.Type = snapshot.TypeFile
				f.Size = info.Size()
				planned, err := b.file(ctx, p, &f)
				if err != nil {
					if ctx.Err() != nil {
						return context.Cause(ctx)
					}
					if ce := (*changedError)(nil); errors.As(err, &ce) {
						retries = append(retries, retry{p, f})
					} else {
						warn(err.Error())
					}
					return nil
				}
				addFile(p, f, planned)
				return nil
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

	for _, r := range retries {
		if ctx.Err() != nil {
			break
		}
		info, err := os.Lstat(r.p)
		if err != nil {
			warn(err.Error())
			continue
		}
		if !info.Mode().IsRegular() {
			warn(fmt.Sprintf("%s stopped being a regular file during the backup", r.p))
			continue
		}
		f := r.f
		f.Size, f.ModTime, f.Mode, f.Chunks = info.Size(), info.ModTime().UTC(), uint32(info.Mode().Perm()), nil
		planned, err := b.file(ctx, r.p, &f)
		if err == nil {
			addFile(r.p, f, planned)
			continue
		}
		if ce := (*changedError)(nil); ctx.Err() != nil || !errors.As(err, &ce) {
			warn(err.Error())
			continue
		}
		// Still changing. A torn copy could be worse than useless (think of
		// a database), so the last clean copy stays, if there is one. Its
		// manifest entry is left alone, so the next run reads it again.
		if prev, ok := e.Manifest.File(f.Path); ok && (prev.Size == 0 || len(prev.Chunks) > 0) && b.allKnown(prev.Chunks) {
			f.Size, f.ModTime, f.Chunks = prev.Size, prev.ModTime, prev.Chunks
			snap.Stats.Kept++
			if len(snap.Kept) < maxListed {
				snap.Kept = append(snap.Kept, f.Path)
			}
			addFile(r.p, f, PlannedFile{})
			continue
		}
		warn(fmt.Sprintf("%s kept changing while it was read and has no earlier copy, so it wasn't backed up", r.p))
	}

	if err := b.finish(); err != nil {
		return res, err
	}
	if ctx.Err() != nil {
		return res, context.Cause(ctx)
	}
	if len(snap.Paths) == 0 {
		// Nothing was there at all: saying so beats an empty snapshot.
		res.Snapshot.Missing = snap.Missing
		return res, fmt.Errorf("none of the folders to back up were found: %s", strings.Join(snap.Missing, ", "))
	}
	snap.Stats.NewChunks = len(b.pending)
	snap.Stats.NewBytes = b.newBytes
	snap.Stats.UploadedBytes = b.uploaded
	res.Snapshot = snap
	if opts.DryRun {
		return res, nil
	}

	tree.Sort()
	// The file list is stored as chunks too, so the parts of it that
	// didn't change since the last snapshot aren't uploaded again.
	listed, err := e.Repo.SaveSnapshot(ctx, snap, &tree, e.Manifest.HasChunk)
	if err := errors.Join(err, e.Manifest.AddChunks(listed)); err != nil {
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

// syncEvery is how long the local chunk list is trusted before a backup
// lists the repository again.
const syncEvery = 7 * 24 * time.Hour

// chunkSync records when and where the local chunk list last matched the
// repository.
type chunkSync struct {
	Time time.Time `json:"time"`
	// Where is the storage.Location it was checked against. The same
	// repository can sit in two places (a copy, or a frost.repo moved on
	// its own), and what one holds says nothing about the other.
	Where string `json:"where"`
	// Needed is set when a verification found a chunk missing.
	Needed bool `json:"needed,omitempty"`
}

// syncDue reports whether the local chunk list should be refreshed from
// storage before it's trusted. Between syncs, backups and verification use
// the local list and don't list the repository. That's safe because frost
// never deletes chunks: one can only go missing from outside, and the
// sampled check after each backup asks for a sync when it finds one gone.
// Storage that isn't where the list was checked always gets a sync.
func (e *Engine) syncDue() bool {
	var s chunkSync
	if !e.Manifest.GetMeta(metaChunkSync, &s) || s.Needed || e.Manifest.ChunkCount() == 0 {
		return true
	}
	if s.Where != storage.Location(e.Repo.Backend) {
		return true
	}
	age := time.Since(s.Time)
	return age < 0 || age > syncEvery
}

// syncChunks replaces the local chunk list with what's in storage. Missing
// chunks are uploaded again by the next backup that still has their data.
func (e *Engine) syncChunks(ctx context.Context) error {
	ids, err := e.Repo.ChunkIDs(ctx)
	if err != nil {
		return err
	}
	if err := e.Manifest.ReplaceChunks(ids); err != nil {
		return err
	}
	return e.Manifest.PutMeta(metaChunkSync, chunkSync{Time: time.Now(), Where: storage.Location(e.Repo.Backend)})
}

// requestSync makes the next backup refresh the chunk list.
func (e *Engine) requestSync() error {
	var s chunkSync
	e.Manifest.GetMeta(metaChunkSync, &s)
	s.Needed = true
	return e.Manifest.PutMeta(metaChunkSync, s)
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

	if prev, ok := b.e.Manifest.File(f.Path); ok && prev.Size == f.Size && (f.Size == 0 || len(prev.Chunks) > 0) && prev.ModTime.Equal(f.ModTime) && b.allKnown(prev.Chunks) {
		f.Chunks = prev.Chunks
		b.remember(f)
		return planned, nil
	}

	fh, err := os.Open(p)
	if err != nil {
		return planned, err
	}
	defer fh.Close()
	before, err := fh.Stat()
	if err != nil {
		return planned, err
	}
	if !before.Mode().IsRegular() {
		return planned, fmt.Errorf("%s isn't a regular file any more", p)
	}
	// It may have changed since the walk saw it. What's open now is what
	// gets read.
	f.Size, f.ModTime, f.Mode = before.Size(), before.ModTime().UTC(), uint32(before.Mode().Perm())
	planned.Size = f.Size

	c := chunker.New(fh, b.table)
	var read int64
	for {
		if err := ctx.Err(); err != nil {
			return planned, context.Cause(ctx)
		}
		data, err := c.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return planned, fmt.Errorf("reading %s: %w", p, err)
		}
		read += int64(len(data))
		if read > f.Size {
			return planned, &changedError{p}
		}
		id := b.e.Repo.Key.ChunkID(data)
		f.Chunks = append(f.Chunks, id.String())
		if chunkRead != nil {
			chunkRead(p)
		}

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
	after, err := fh.Stat()
	if err != nil {
		return planned, err
	}
	current, err := os.Lstat(p)
	if err != nil {
		return planned, err
	}
	if read != f.Size || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) || !os.SameFile(before, current) {
		return planned, &changedError{p}
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
