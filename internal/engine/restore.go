package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/rhymeswithlimo/frost/internal/crypto"
	"github.com/rhymeswithlimo/frost/internal/snapshot"
)

// RestoreOptions controls a restore.
type RestoreOptions struct {
	// Target is the directory to restore into. Files land under it at their
	// full original path, e.g. <target>/home/me/notes.txt. Empty means
	// restore in place, over the original locations.
	Target string
	// Include limits the restore to these paths and everything under them.
	// Empty means the whole snapshot.
	Include []string
	// Progress, if set, is called after each file is written.
	Progress func(path string, done, total int)
}

// RestoreResult summarises a restore.
type RestoreResult struct {
	Files int
	Dirs  int
	Bytes int64
}

// Restore writes files from snapshot id back to disk. Every chunk is
// decrypted and checked against its ID before a byte is written.
func (e *Engine) Restore(ctx context.Context, id string, opts RestoreOptions) (RestoreResult, error) {
	var res RestoreResult
	tree, err := e.Repo.LoadTree(ctx, id)
	if err != nil {
		return res, fmt.Errorf("loading snapshot %s: %w", id, err)
	}

	var files []snapshot.File
	for _, f := range tree.Files {
		if included(f.Path, opts.Include) {
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		return res, errors.New("nothing in the snapshot matches the selected paths")
	}

	dest := func(p string) (string, error) {
		rel, err := snapshot.SafeRel(p)
		if err != nil {
			return "", err
		}
		if opts.Target == "" {
			return filepath.FromSlash(p), nil
		}
		return filepath.Join(opts.Target, filepath.FromSlash(rel)), nil
	}

	// Parents of included files must exist even if they weren't selected.
	var dirs, links []snapshot.File
	for _, f := range files {
		switch f.Type {
		case snapshot.TypeDir:
			dirs = append(dirs, f)
		case snapshot.TypeSymlink:
			links = append(links, f)
		}
	}

	total := len(files)
	done := 0
	for _, f := range files {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		if f.Type != snapshot.TypeFile {
			continue
		}
		out, err := dest(f.Path)
		if err != nil {
			return res, err
		}
		if err := e.restoreFile(ctx, f, out); err != nil {
			return res, err
		}
		res.Files++
		res.Bytes += f.Size
		done++
		if opts.Progress != nil {
			opts.Progress(f.Path, done, total)
		}
	}

	// Symlinks go last so a link can never redirect a later file write.
	for _, f := range links {
		out, err := dest(f.Path)
		if err != nil {
			return res, err
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return res, err
		}
		os.Remove(out)
		if err := os.Symlink(f.Target, out); err != nil {
			return res, fmt.Errorf("creating symlink %s: %w", out, err)
		}
	}

	// Directories last, deepest first, so file writes don't bump their mtimes.
	slices.SortFunc(dirs, func(a, b snapshot.File) int { return strings.Compare(b.Path, a.Path) })
	for _, f := range dirs {
		out, err := dest(f.Path)
		if err != nil {
			return res, err
		}
		if err := os.MkdirAll(out, 0o755); err != nil {
			return res, err
		}
		os.Chmod(out, fs.FileMode(f.Mode))
		os.Chtimes(out, f.ModTime, f.ModTime)
		res.Dirs++
	}
	return res, nil
}

func (e *Engine) restoreFile(ctx context.Context, f snapshot.File, out string) error {
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	// Write to a temp file and rename, so a failed restore never leaves a
	// half-written file where a good one used to be.
	tmp, err := os.CreateTemp(filepath.Dir(out), ".frost-restore-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	for _, c := range f.Chunks {
		id, err := crypto.ParseID(c)
		if err != nil {
			tmp.Close()
			return err
		}
		data, err := e.Repo.GetChunk(ctx, id)
		if err != nil {
			tmp.Close()
			return fmt.Errorf("restoring %s: %w", f.Path, err)
		}
		if _, err := tmp.Write(data); err != nil {
			tmp.Close()
			return err
		}
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	os.Chmod(tmp.Name(), fs.FileMode(f.Mode))
	os.Chtimes(tmp.Name(), f.ModTime, f.ModTime)
	os.Remove(out) // Windows can't rename over an existing file
	return os.Rename(tmp.Name(), out)
}

func included(p string, include []string) bool {
	if len(include) == 0 {
		return true
	}
	for _, inc := range include {
		inc = strings.TrimSuffix(filepath.ToSlash(inc), "/")
		if p == inc || strings.HasPrefix(p, inc+"/") {
			return true
		}
	}
	return false
}
