package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/rhymeswithlimo/frost/internal/chunker"
	"github.com/rhymeswithlimo/frost/internal/crypto"
	"github.com/rhymeswithlimo/frost/internal/snapshot"
)

// errBusy means another process holds the lock on a restore file.
var errBusy = errors.New("another restore is using it")

// restoreMarker sits at the top of a new restore folder until the restore
// finishes, so running the same restore again carries on in that folder.
const restoreMarker = ".frost-restore"

// marker is what restoreMarker holds.
type marker struct {
	Snapshot string   `json:"snapshot"`
	Include  []string `json:"include,omitempty"`
}

func newMarker(id string, include []string) marker {
	inc := slices.Clone(include)
	slices.Sort(inc)
	return marker{Snapshot: id, Include: inc}
}

func (m marker) equal(o marker) bool {
	return m.Snapshot == o.Snapshot && slices.Equal(m.Include, o.Include)
}

// holdMarker creates the marker in a new restore folder, or checks that an
// existing folder's marker is for this restore. Either way the marker stays
// locked until the returned file is closed.
func holdMarker(root *os.Root, want marker, create bool) (*os.File, error) {
	if create {
		f, err := root.OpenFile(restoreMarker, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, err
		}
		raw, _ := json.Marshal(want)
		if err = lockFile(f); err == nil {
			if _, err = f.Write(raw); err == nil {
				err = f.Sync()
			}
		}
		if err != nil {
			f.Close()
			return nil, err
		}
		return f, nil
	}
	info, err := root.Lstat(restoreMarker)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("the restore folder already exists")
	}
	f, err := root.OpenFile(restoreMarker, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("another restore is writing to this folder: %w", err)
	}
	if got, ok := readMarker(f); !ok || !got.equal(want) {
		f.Close()
		return nil, errors.New("the restore folder holds an unfinished restore of something else")
	}
	return f, nil
}

func readMarker(f *os.File) (marker, bool) {
	var m marker
	raw, err := io.ReadAll(io.LimitReader(f, 1<<20))
	return m, err == nil && json.Unmarshal(raw, &m) == nil
}

// unfinished reports whether dir holds an interrupted restore of want that
// no other process is working on.
func unfinished(dir string, want marker) bool {
	p := filepath.Join(dir, restoreMarker)
	if info, err := os.Lstat(p); err != nil || !info.Mode().IsRegular() {
		return false
	}
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	if lockFile(f) != nil {
		return false
	}
	got, ok := readMarker(f)
	return ok && got.equal(want)
}

// partialName is where a file is written until it's complete. It's the same
// on every run, so an interrupted restore can continue it.
func partialName(id, p string) string {
	sum := sha256.Sum256([]byte(id + "\x00" + p))
	return partialPrefix + hex.EncodeToString(sum[:8])
}

const partialPrefix = ".frost-partial-"

// isPartial reports whether name is exactly what partialName makes. Backups
// skip these, so a restore that stopped over the originals isn't backed up.
func isPartial(name string) bool {
	rest, ok := strings.CutPrefix(name, partialPrefix)
	if !ok || len(rest) != 16 {
		return false
	}
	_, err := hex.DecodeString(rest)
	return err == nil && strings.ToLower(rest) == rest
}

// restoring is one regular file on its way to disk.
type restoring struct {
	f   snapshot.File
	out string // destination, relative to the target root or absolute
	ids []crypto.ID
	// done means the destination already holds this file.
	done bool
	// partial is an earlier run's partial file, locked, of which the first
	// have chunks (haveLen bytes) are right.
	partial *os.File
	have    int
	haveLen int64
}

// restoreWriter writes regular files in order as their chunks arrive.
type restoreWriter struct {
	e        *Engine
	id       string // snapshot
	root     *os.Root
	table    *chunker.Table
	ch       *chunker.Chunker
	progress func(RestoreProgress)
	p        RestoreProgress
	// kept is set once a partial file is left on disk.
	kept bool

	// The file being written.
	parent  *os.Root
	fh      *os.File
	name    string // its partial name
	written int64
	need    int // chunks it's still waiting for
}

func (w *restoreWriter) report(path string) {
	if w.progress != nil {
		p := w.p
		p.Path = path
		w.progress(p)
	}
}

// restoreFiles writes files. With check, what's already at each destination
// is compared with the snapshot first, so finished files and the good part
// of partial files aren't downloaded again.
func (e *Engine) restoreFiles(ctx context.Context, id string, root *os.Root, files []*restoring, check bool, progress func(RestoreProgress)) (w *restoreWriter, err error) {
	w = &restoreWriter{e: e, id: id, root: root, table: chunker.NewTable(e.Repo.Key.ChunkerSeed()), progress: progress}
	w.p.TotalFiles = len(files)
	for _, r := range files {
		w.p.TotalBytes += r.f.Size
	}
	defer func() {
		w.abandon()
		for _, r := range files {
			if r.partial != nil {
				r.partial.Close()
				w.kept = true
			}
		}
	}()
	if check {
		w.p.Checking = true
		for _, r := range files {
			if err := w.check(ctx, r); err != nil {
				return w, err
			}
		}
		w.p.Checking, w.p.Bytes = false, 0
	}
	return w, w.write(ctx, files)
}

// check looks at r's destination and its partial file from an earlier run.
func (w *restoreWriter) check(ctx context.Context, r *restoring) error {
	parent, err := restoreParent(r.out, w.root)
	if err != nil {
		return err
	}
	defer parent.Close()
	name := filepath.Base(r.out)
	if info, err := parent.Lstat(name); err == nil && info.Mode().IsRegular() && info.Size() == r.f.Size {
		if fh, err := parent.Open(name); err == nil {
			n, length, err := w.matched(ctx, fh, r)
			fh.Close()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// A destination that can't be read is replaced, as before.
			if err == nil && n == len(r.ids) && length == r.f.Size {
				r.done = true
				return nil
			}
		}
	}
	pname := partialName(w.id, r.f.Path)
	info, err := parent.Lstat(pname)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("restoring %s: %s isn't a regular file", r.f.Path, pname)
	}
	fh, err := parent.OpenFile(pname, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := lockFile(fh); err != nil {
		fh.Close()
		return fmt.Errorf("restoring %s: %w", r.f.Path, err)
	}
	if st, err := fh.Stat(); err != nil || !os.SameFile(info, st) {
		fh.Close()
		return fmt.Errorf("restoring %s: %s changed while it was opened", r.f.Path, pname)
	}
	n, length, err := w.matched(ctx, fh, r)
	if err != nil {
		fh.Close()
		return err
	}
	r.partial, r.have, r.haveLen = fh, n, length
	return nil
}

// matched counts how many of r's leading chunks rd already holds, checked
// by their keyed IDs. Content-defined cut points depend only on the bytes
// since the last cut, so a prefix of a file splits into the same leading
// chunks as the whole file.
func (w *restoreWriter) matched(ctx context.Context, rd io.Reader, r *restoring) (int, int64, error) {
	if w.ch == nil {
		w.ch = chunker.New(rd, w.table)
	} else {
		w.ch.Reset(rd)
	}
	n, length := 0, int64(0)
	for n < len(r.ids) {
		if err := ctx.Err(); err != nil {
			return n, length, err
		}
		data, err := w.ch.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return n, length, err
		}
		if w.e.Repo.Key.ChunkID(data) != r.ids[n] {
			break
		}
		n++
		length += int64(len(data))
		w.p.Bytes += int64(len(data))
		w.report(r.f.Path)
	}
	return n, length, nil
}

// write downloads every chunk not already on disk, in one ordered stream
// across all files, and writes each file as its chunks arrive.
func (w *restoreWriter) write(ctx context.Context, files []*restoring) error {
	var ids []crypto.ID
	for _, r := range files {
		if !r.done {
			ids = append(ids, r.ids[r.have:]...)
		}
	}
	k := 0
	// settle finishes files until it reaches one still waiting for data,
	// and opens that one.
	settle := func() error {
		for k < len(files) {
			r := files[k]
			if !r.done && w.fh == nil {
				if err := w.open(r); err != nil {
					return err
				}
			}
			if !r.done && w.need > 0 {
				return nil
			}
			if err := w.finish(r); err != nil {
				return err
			}
			k++
		}
		return nil
	}
	if err := settle(); err != nil {
		return err
	}
	return w.e.Repo.Fetch(ctx, ids, w.e.downloaders(), func(_ int, data []byte) error {
		r := files[k]
		if int64(len(data)) > r.f.Size-w.written {
			w.discard()
			return fmt.Errorf("restoring %s: data exceeds recorded size", r.f.Path)
		}
		if _, err := w.fh.Write(data); err != nil {
			return err
		}
		w.written += int64(len(data))
		w.need--
		w.p.Bytes += int64(len(data))
		w.report(r.f.Path)
		if w.need == 0 {
			return settle()
		}
		return nil
	})
}

// open starts writing r, continuing its partial file if it has one.
func (w *restoreWriter) open(r *restoring) error {
	parent, err := restoreParent(r.out, w.root)
	if err != nil {
		return err
	}
	name := partialName(w.id, r.f.Path)
	fh := r.partial
	r.partial = nil
	if fh != nil {
		if err = fh.Truncate(r.haveLen); err == nil {
			_, err = fh.Seek(r.haveLen, io.SeekStart)
		}
		w.written = r.haveLen
	} else {
		fh, err = parent.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			if err = lockFile(fh); err != nil {
				fh.Close()
				fh = nil
				err = fmt.Errorf("restoring %s: %w", r.f.Path, err)
			}
		}
		w.written = 0
	}
	if err != nil {
		if fh != nil {
			fh.Close()
		}
		parent.Close()
		return err
	}
	w.parent, w.fh, w.name, w.need = parent, fh, name, len(r.ids)-r.have
	w.p.Bytes += w.written
	return nil
}

// finish puts a complete file in place, or fixes the mode and time of one
// that was already there.
func (w *restoreWriter) finish(r *restoring) error {
	mode := fs.FileMode(r.f.Mode).Perm()
	if r.done {
		parent, err := restoreParent(r.out, w.root)
		if err != nil {
			return err
		}
		name := filepath.Base(r.out)
		err = parent.Chmod(name, mode)
		if err == nil {
			err = parent.Chtimes(name, r.f.ModTime, r.f.ModTime)
		}
		parent.Close()
		if err != nil {
			return err
		}
		w.p.Bytes += r.f.Size
	} else {
		if w.written != r.f.Size {
			w.discard()
			return fmt.Errorf("restoring %s: size mismatch", r.f.Path)
		}
		err := w.fh.Chmod(mode)
		if err == nil {
			err = w.parent.Chtimes(w.name, r.f.ModTime, r.f.ModTime)
		}
		if err == nil {
			err = w.fh.Sync()
		}
		if cerr := w.fh.Close(); err == nil {
			err = cerr
		}
		w.fh = nil
		if err == nil {
			// The old file isn't removed first: the rename replaces it in
			// one step, so a failure never leaves neither.
			err = w.parent.Rename(w.name, filepath.Base(r.out))
		}
		if err != nil {
			w.kept = true
			w.parent.Close()
			w.parent = nil
			return err
		}
		w.parent.Close()
		w.parent = nil
	}
	w.p.Files++
	w.report(r.f.Path)
	return nil
}

// discard drops the file being written: its data doesn't match the
// snapshot, so continuing it later wouldn't help.
func (w *restoreWriter) discard() {
	if w.fh == nil {
		return
	}
	w.fh.Close()
	w.fh = nil
	w.parent.Remove(w.name)
	w.parent.Close()
	w.parent = nil
}

// abandon stops writing after a failure. A partial file with data in it is
// kept for the next run to continue.
func (w *restoreWriter) abandon() {
	if w.fh == nil {
		return
	}
	w.fh.Close()
	w.fh = nil
	if w.written == 0 {
		w.parent.Remove(w.name)
	} else {
		w.kept = true
	}
	w.parent.Close()
	w.parent = nil
}
