package engine

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
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
	// NewTarget refuses an existing target, for the default safe restore.
	NewTarget bool
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
		// The Unix filesystem root has no relative name. Restore its
		// children, without changing permissions on the destination root.
		if f.Path == "/" && f.Type == snapshot.TypeDir {
			continue
		}
		if included(f.Path, opts.Include) {
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		return res, errors.New("nothing in the snapshot matches the selected paths")
	}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	// Validate the entire selection before modifying any destination.
	seen := make(map[string]snapshot.Type)
	for _, f := range files {
		rel, err := snapshot.SafeRel(f.Path)
		if err != nil {
			return res, err
		}
		if opts.Target == "" && !filepath.IsAbs(filepath.FromSlash(f.Path)) {
			return res, fmt.Errorf("can't restore foreign or relative path %q in place", f.Path)
		}
		if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
			rel = strings.ToLower(rel)
		}
		if _, exists := seen[rel]; exists {
			return res, fmt.Errorf("duplicate restore destination %q", f.Path)
		}
		seen[rel] = f.Type
		if f.Type != snapshot.TypeFile && f.Type != snapshot.TypeDir && f.Type != snapshot.TypeSymlink {
			return res, fmt.Errorf("unknown file type %q", f.Type)
		}
		if f.Size < 0 {
			return res, fmt.Errorf("negative file size for %q", f.Path)
		}
		for _, c := range f.Chunks {
			if _, err := crypto.ParseID(c); err != nil {
				return res, err
			}
		}
	}
	for rel := range seen {
		for parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(rel))); parent != "."; parent = filepath.ToSlash(filepath.Dir(filepath.FromSlash(parent))) {
			if kind, ok := seen[parent]; ok && kind != snapshot.TypeDir {
				return res, fmt.Errorf("restore path %q has a non-directory ancestor", rel)
			}
		}
	}
	if opts.NewTarget {
		if opts.Target == "" {
			return res, errors.New("new restore target is empty")
		}
		if err := os.Mkdir(opts.Target, 0o700); err != nil {
			return res, fmt.Errorf("creating new restore target: %w", err)
		}
	}
	var targetRoot *os.Root
	if opts.Target != "" {
		if err := os.MkdirAll(opts.Target, 0o700); err != nil {
			return res, err
		}
		info, err := os.Lstat(opts.Target)
		if err != nil {
			return res, err
		}
		if !info.IsDir() {
			return res, errors.New("restore target isn't a real directory")
		}
		targetRoot, err = os.OpenRoot(opts.Target)
		if err != nil {
			return res, err
		}
		defer targetRoot.Close()
	}

	dest := func(p string) (string, error) {
		rel, err := snapshot.SafeRel(p)
		if err != nil {
			return "", err
		}
		if opts.Target == "" {
			return filepath.FromSlash(p), nil
		}
		return filepath.FromSlash(rel), nil
	}

	// Parents of included files must exist even if they weren't selected.
	var dirs, links []snapshot.File
	total := 0 // progress counts regular files only
	for _, f := range files {
		switch f.Type {
		case snapshot.TypeDir:
			dirs = append(dirs, f)
		case snapshot.TypeSymlink:
			links = append(links, f)
		case snapshot.TypeFile:
			total++
		}
	}

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
		if err := e.restoreFile(ctx, f, out, targetRoot); err != nil {
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
		if err := ctx.Err(); err != nil {
			return res, err
		}
		out, err := dest(f.Path)
		if err != nil {
			return res, err
		}
		parent, err := restoreParent(out, targetRoot)
		if err != nil {
			return res, err
		}
		name := ".frost-restore-" + rand.Text()
		err = parent.Symlink(filepath.FromSlash(f.Target), name)
		if err == nil {
			err = parent.Rename(name, filepath.Base(out))
		}
		parent.Remove(name)
		parent.Close()
		if err != nil {
			return res, fmt.Errorf("creating symlink %s: %w", out, err)
		}
	}

	// Directories last, deepest first, so file writes don't bump their mtimes.
	slices.SortFunc(dirs, func(a, b snapshot.File) int { return strings.Compare(b.Path, a.Path) })
	for _, f := range dirs {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		out, err := dest(f.Path)
		if err != nil {
			return res, err
		}
		parent, err := restoreParent(filepath.Join(out, ".frost-directory"), targetRoot)
		if err != nil {
			return res, err
		}
		err = parent.Chmod(".", fs.FileMode(f.Mode).Perm())
		if err == nil {
			err = parent.Chtimes(".", f.ModTime, f.ModTime)
		}
		parent.Close()
		if err != nil {
			return res, err
		}
		res.Dirs++
	}
	return res, nil
}

func (e *Engine) restoreFile(ctx context.Context, f snapshot.File, out string, root *os.Root) error {
	parent, err := restoreParent(out, root)
	if err != nil {
		return err
	}
	defer parent.Close()
	// Write to a temp file and rename, so a failed restore never leaves a
	// half-written file where a good one used to be.
	name := ".frost-restore-" + rand.Text()
	tmp, err := parent.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer parent.Remove(name)
	defer tmp.Close()
	var written int64

	for _, c := range f.Chunks {
		if err := ctx.Err(); err != nil {
			return err
		}
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
		if int64(len(data)) > f.Size-written {
			return fmt.Errorf("restoring %s: data exceeds recorded size", f.Path)
		}
		if _, err := tmp.Write(data); err != nil {
			tmp.Close()
			return err
		}
		written += int64(len(data))
	}
	if written != f.Size {
		return fmt.Errorf("restoring %s: size mismatch", f.Path)
	}
	if err := tmp.Chmod(fs.FileMode(f.Mode).Perm()); err != nil {
		return err
	}
	if err := parent.Chtimes(name, f.ModTime, f.ModTime); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return parent.Rename(name, filepath.Base(out))
}

// Open each parent through a confined directory handle. Existing symlinks
// are refused; a concurrent replacement cannot redirect access outside the
// currently opened parent. Never remove a destination to make rename succeed.
func restoreParent(out string, root *os.Root) (*os.Root, error) {
	var r *os.Root
	var err error
	rel := filepath.Dir(out)
	if root != nil {
		r, err = root.OpenRoot(".")
	} else {
		// In-place restores use the existing parent as their boundary. Reject
		// symlinked ancestors before opening it, including missing suffixes.
		base := rel
		var suffix []string
		for {
			info, statErr := os.Lstat(base)
			if statErr == nil {
				if !info.IsDir() {
					return nil, fmt.Errorf("restore parent %q isn't a real directory", base)
				}
				break
			}
			if !errors.Is(statErr, fs.ErrNotExist) {
				return nil, statErr
			}
			suffix = append(suffix, filepath.Base(base))
			base = filepath.Dir(base)
		}
		resolved, resolveErr := filepath.EvalSymlinks(base)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if resolved != base {
			return nil, fmt.Errorf("restore parent %q contains a symlink", base)
		}
		r, err = os.OpenRoot(base)
		slices.Reverse(suffix)
		rel = filepath.Join(suffix...)
	}
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "." || part == "" {
			continue
		}
		info, err := r.Lstat(part)
		if errors.Is(err, fs.ErrNotExist) {
			err = r.Mkdir(part, 0o755)
		} else if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			err = fmt.Errorf("restore parent %q isn't a real directory", part)
		}
		if err != nil {
			r.Close()
			return nil, err
		}
		next, err := r.OpenRoot(part)
		r.Close()
		if err != nil {
			return nil, err
		}
		r = next
	}
	return r, nil
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
