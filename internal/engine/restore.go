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
	// NewTarget refuses an existing target, for the default safe restore,
	// unless it holds an unfinished restore of the same snapshot and
	// selection. Then that restore carries on.
	NewTarget bool
	// Base, if set, is stripped from every path under Target, so
	// <target>/docs/notes.txt instead of <target>/home/me/docs/notes.txt.
	// Everything restored must be inside it. It needs a Target.
	Base string
	// Include limits the restore to these paths and everything under them.
	// Empty means the whole snapshot.
	Include []string
	// Progress, if set, is called as files are checked and written.
	Progress func(RestoreProgress)
}

// RestoreProgress is a running tally during a restore. It counts regular
// files only.
type RestoreProgress struct {
	// Checking is set while files already at the destination are compared
	// with the snapshot, before anything downloads. Bytes then counts what's
	// been read.
	Checking   bool
	Path       string
	Files      int // finished
	TotalFiles int
	Bytes      int64 // finished or written so far
	TotalBytes int64
}

// RestoreResult summarises a restore.
type RestoreResult struct {
	Files int
	Dirs  int
	Bytes int64
	// Unfinished means a failed restore left work behind that running the
	// same restore again continues.
	Unfinished bool
}

// Restore writes files from snapshot id back to disk. Every chunk is
// decrypted and checked against its ID before a byte is written. Chunks
// download in parallel. A file is written under a partial name and renamed
// into place when it's complete. If a restore stops, the next run of the
// same one skips files already in place and continues the partial file
// from its last good chunk. It doesn't need the manifest.
func (e *Engine) Restore(ctx context.Context, id string, opts RestoreOptions) (res RestoreResult, err error) {
	tree, err := e.Repo.LoadTree(ctx, id)
	if err != nil {
		return res, fmt.Errorf("loading snapshot %s: %w", id, err)
	}

	if opts.Base != "" && opts.Target == "" {
		return res, errors.New("a restore base needs a target")
	}
	// relOf is where a path goes under the target.
	relOf := func(p string) (string, error) {
		if opts.Target == "" {
			return snapshot.SafeRel(p)
		}
		return snapshot.RestoreRel(p, opts.Base)
	}

	var files []snapshot.File
	for _, f := range tree.Files {
		// The Unix filesystem root has no relative name. Restore its
		// children, without changing permissions on the destination root.
		if f.Path == "/" && f.Type == snapshot.TypeDir {
			continue
		}
		// Folders the base sits in aren't part of what's restored.
		if opts.Base != "" && f.Type == snapshot.TypeDir && strings.HasPrefix(withSlash(opts.Base), withSlash(f.Path)) {
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
		rel, err := relOf(f.Path)
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
		if opts.NewTarget && strings.EqualFold(rel, restoreMarker) {
			return res, fmt.Errorf("can't restore %q into a new folder: frost uses that name there", f.Path)
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
	// fresh means nothing of this restore can be on disk yet, so there's
	// nothing to check before downloading.
	fresh := false
	if opts.NewTarget {
		if opts.Target == "" {
			return res, errors.New("new restore target is empty")
		}
		err := os.Mkdir(opts.Target, 0o700)
		if err != nil && !errors.Is(err, fs.ErrExist) {
			return res, fmt.Errorf("creating new restore target: %w", err)
		}
		fresh = err == nil
	}
	var targetRoot *os.Root
	var mark *os.File
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
		if opts.NewTarget {
			if mark, err = holdMarker(targetRoot, newMarker(id, opts.Include), fresh); err != nil {
				return res, fmt.Errorf("restoring into %s: %w", opts.Target, err)
			}
		}
	}
	if mark != nil {
		// Out here, err is the result: the marker stays until a run succeeds.
		defer func() {
			mark.Close()
			if err == nil {
				targetRoot.Remove(restoreMarker)
			} else {
				res.Unfinished = true
			}
		}()
	}

	dest := func(p string) (string, error) {
		r, err := relOf(p)
		if err != nil {
			return "", err
		}
		if opts.Target == "" {
			return filepath.FromSlash(p), nil
		}
		return filepath.FromSlash(r), nil
	}

	// Parents of included files must exist even if they weren't selected.
	var dirs, links []snapshot.File
	var regular []*restoring
	for _, f := range files {
		switch f.Type {
		case snapshot.TypeDir:
			dirs = append(dirs, f)
		case snapshot.TypeSymlink:
			links = append(links, f)
		case snapshot.TypeFile:
			out, err := dest(f.Path)
			if err != nil {
				return res, err
			}
			r := &restoring{f: f, out: out, ids: make([]crypto.ID, len(f.Chunks))}
			for i, c := range f.Chunks {
				r.ids[i], _ = crypto.ParseID(c) // checked above
			}
			regular = append(regular, r)
		}
	}

	w, err := e.restoreFiles(ctx, id, targetRoot, regular, !fresh, opts.Progress)
	res.Files, res.Bytes = w.p.Files, w.p.Bytes
	if err != nil {
		res.Unfinished = w.kept
		return res, err
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

// Open each parent through a confined directory handle. Existing symlinks
// below the starting folder are refused; a concurrent replacement cannot
// redirect access outside the currently opened parent. Never remove a
// destination to make rename succeed.
func restoreParent(out string, root *os.Root) (*os.Root, error) {
	var r *os.Root
	var err error
	rel := filepath.Dir(out)
	if root != nil {
		r, err = root.OpenRoot(".")
	} else {
		// In-place restores start from the deepest parent that exists and
		// create the rest inside it.
		var base string
		var suffix []string
		base, suffix, err = inPlaceBase(rel)
		if err == nil {
			r, err = os.OpenRoot(base)
			rel = filepath.Join(suffix...)
		}
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

func withSlash(p string) string {
	if strings.HasSuffix(p, "/") {
		return p
	}
	return p + "/"
}

// RestoreFolderName is the name of a new folder for restoring snapshot id
// into. It uses snapshot.Short(id) alone, never a longer form that depends
// on the other snapshots, so a later backup can't rename the folder an
// interrupted restore carries on in.
func RestoreFolderName(id string) string { return "frost-restore-" + snapshot.Short(id) }

// NewRestoreFolder is a RestoreFolderName(id) folder in parent for
// restoring include from snapshot id, adding -1, -2 and so on when the name
// is taken. If one of them holds an unfinished restore of the same selection
// that nothing else is working on, it's returned with resume set, and
// restoring into it carries on. The marker holds the full ID, so a snapshot
// with the same short ID never resumes another's folder. Otherwise Restore
// creates the folder exclusively, so a race can't reuse one.
func NewRestoreFolder(parent, id string, include []string) (dir string, resume bool, err error) {
	base := filepath.Join(parent, RestoreFolderName(id))
	want := newMarker(id, include)
	for n := 0; n < 10000; n++ {
		candidate := base
		if n > 0 {
			candidate = fmt.Sprintf("%s-%d", base, n)
		}
		info, err := os.Lstat(candidate)
		if errors.Is(err, fs.ErrNotExist) {
			return candidate, false, nil
		} else if err != nil {
			return base, false, err
		}
		if info.IsDir() && unfinished(candidate, want) {
			return candidate, true, nil
		}
	}
	return base, false, fmt.Errorf("no unused restore folder found beside %s", base)
}

// BesideFolder is a new restore folder next to the originals, in base (see
// snapshot.RestoreBase). The error says in plain words why there can't be
// one.
func BesideFolder(base, id string, include []string) (dir string, resume bool, err error) {
	if base == "" {
		return "", false, errors.New("the selection is on more than one drive")
	}
	if snapshot.IsRoot(base) {
		return "", false, errors.New("the selection only shares the top of the drive")
	}
	dir = filepath.FromSlash(base)
	if !filepath.IsAbs(dir) {
		return "", false, errors.New("the snapshot is from a different kind of computer")
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", false, fmt.Errorf("%s isn't on this computer", dir)
	}
	probe, err := os.MkdirTemp(dir, ".frost-probe-")
	if err != nil {
		return "", false, fmt.Errorf("can't write to %s", dir)
	}
	os.Remove(probe)
	return NewRestoreFolder(dir, id, include)
}

// inPlaceBase finds the deepest existing folder above dir, and the missing
// folders under it. The folder comes back with its links resolved, and
// only links that can be trusted are followed (see resolveParent).
func inPlaceBase(dir string) (string, []string, error) {
	base := dir
	var suffix []string
	for {
		info, err := os.Lstat(base)
		if err == nil {
			// Links, and junctions on Windows, are for resolveParent to judge.
			if info.Mode()&(os.ModeSymlink|os.ModeIrregular) == 0 && !info.IsDir() {
				return "", nil, fmt.Errorf("%s isn't a folder", base)
			}
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", nil, err
		}
		suffix = append(suffix, filepath.Base(base))
		base = filepath.Dir(base)
	}
	resolved, err := resolveParent(base)
	if err != nil {
		return "", nil, err
	}
	if info, err := os.Stat(resolved); err != nil || !info.IsDir() {
		return "", nil, fmt.Errorf("%s isn't a folder", base)
	}
	slices.Reverse(suffix)
	return resolved, suffix, nil
}

// resolveTrusted resolves every link in p, like filepath.EvalSymlinks, but
// refuses a link that trusted says no to.
func resolveTrusted(p string, trusted func(fs.FileInfo) bool) (string, error) {
	sep := string(filepath.Separator)
	split := func(p string) (string, []string) {
		vol := filepath.VolumeName(p)
		return vol, strings.Split(strings.Trim(p[len(vol):], sep), sep)
	}
	vol, parts := split(filepath.Clean(p))
	resolved := vol + sep
	for links := 0; len(parts) > 0; {
		part := parts[0]
		parts = parts[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			resolved = filepath.Dir(resolved)
			continue
		}
		next := filepath.Join(resolved, part)
		info, err := os.Lstat(next)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			resolved = next
			continue
		}
		if !trusted(info) {
			return "", fmt.Errorf("%s is a link owned by another user", next)
		}
		if links++; links > 40 {
			return "", fmt.Errorf("too many links in %s", p)
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", err
		}
		var more []string
		if filepath.IsAbs(target) {
			vol, more = split(target)
			resolved = vol + sep
		} else {
			_, more = split(target)
		}
		parts = append(more, parts...)
	}
	return resolved, nil
}

// CanOverwrite says why paths can't be restored over the originals, or
// returns nil. It checks the folders above each path; someone else's link
// inside the selection is still refused when the restore gets to it.
func CanOverwrite(paths []string) error {
	seen := map[string]bool{}
	for _, p := range paths {
		native := filepath.FromSlash(p)
		if !filepath.IsAbs(native) {
			return errors.New("the snapshot is from a different kind of computer")
		}
		parent := filepath.Dir(native)
		if seen[parent] {
			continue // a whole folder's worth of selected siblings
		}
		seen[parent] = true
		if _, _, err := inPlaceBase(parent); err != nil {
			return err
		}
	}
	return nil
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
