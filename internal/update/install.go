package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ErrBusy means another frost is installing an update right now.
var ErrBusy = errors.New("another frost update is already running")

// Executable is the path of the running binary with links resolved: the
// file an update replaces, and the one the scheduled job runs.
func Executable() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}

// Managed names the package manager that owns exe, if any. frost doesn't
// replace files a package manager put there.
func Managed(exe string) string {
	p := strings.ReplaceAll(exe, `\`, "/")
	lower := strings.ToLower(p)
	switch {
	case strings.Contains(p, "/Cellar/") || strings.HasPrefix(p, "/home/linuxbrew/"):
		return "Homebrew"
	case strings.HasPrefix(p, "/nix/store/"):
		return "Nix"
	case strings.HasPrefix(p, "/snap/"):
		return "Snap"
	case strings.Contains(lower, "/scoop/apps/"):
		return "Scoop"
	case strings.HasPrefix(p, "/usr/bin/") || strings.HasPrefix(p, "/bin/"):
		return "your system's package manager"
	}
	return ""
}

// CanReplace checks that frost could swap the binary at exe, before
// anything is downloaded.
func CanReplace(exe string) error {
	if pm := Managed(exe); pm != "" {
		return fmt.Errorf("%s was installed by %s, update it there", exe, pm)
	}
	f, err := os.CreateTemp(filepath.Dir(exe), stagePattern())
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			how := "with sudo"
			if runtime.GOOS == "windows" {
				how = "as administrator"
			}
			return fmt.Errorf("can't write to %s. Run `frost update` %s, or reinstall frost somewhere you can write to", filepath.Dir(exe), how)
		}
		return err
	}
	f.Close()
	os.Remove(f.Name())
	return nil
}

// probe runs a staged binary and checks it reports the version it should.
// Tests swap it.
var probe = func(ctx context.Context, bin, version string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return fmt.Errorf("the new binary doesn't run: %w", err)
	}
	f := strings.Fields(string(out))
	if len(f) == 0 || f[len(f)-1] != version {
		return fmt.Errorf("the new binary says %q, expected %s", strings.TrimSpace(string(out)), version)
	}
	return nil
}

// Install downloads rel, checks it against the signed checksum, and
// replaces the binary at exe. The old binary stays in place until the new
// one has been written, synced and run once.
func Install(ctx context.Context, rel Release, exe string) error {
	if len(rel.sum) != sha256.Size {
		return errors.New("release wasn't checked, use Latest")
	}
	if err := CanReplace(exe); err != nil {
		return err
	}
	unlock, err := lock(filepath.Dir(exe))
	if err != nil {
		return err
	}
	defer unlock()
	Cleanup(exe)

	archive, err := get(ctx, BaseURL+"/download/"+rel.Version+"/"+rel.Archive, maxArchive)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", rel.Archive, err)
	}
	got := sha256.Sum256(archive)
	if subtle.ConstantTimeCompare(got[:], rel.sum) != 1 {
		return fmt.Errorf("%s doesn't match its signed checksum, not installing it", rel.Archive)
	}
	bin, err := extract(rel.Archive, archive)
	if err != nil {
		return fmt.Errorf("%s: %w", rel.Archive, err)
	}

	staged, err := stage(exe, bin)
	if err != nil {
		return err
	}
	defer os.Remove(staged) // gone already if the swap worked
	if err := probe(ctx, staged, rel.Version); err != nil {
		return err
	}
	return replace(staged, exe)
}

// extract pulls the frost binary out of a release archive. Nothing else in
// the archive is looked at, and no path from it is used.
func extract(name string, archive []byte) ([]byte, error) {
	want := "frost"
	if strings.HasSuffix(name, ".zip") {
		want = "frost.exe"
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if cleanName(f.Name) != want || !f.Mode().IsRegular() {
				continue
			}
			if f.UncompressedSize64 > maxBinary {
				return nil, errors.New("binary too big")
			}
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return readAll(rc)
		}
		return nil, errors.New(want + " not found in the archive")
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, errors.New(want + " not found in the archive")
		}
		if err != nil {
			return nil, err
		}
		if cleanName(h.Name) == want && h.Typeflag == tar.TypeReg {
			if h.Size > maxBinary {
				return nil, errors.New("binary too big")
			}
			return readAll(tr)
		}
	}
}

func cleanName(n string) string { return strings.TrimPrefix(path.Clean("/"+n), "/") }

func readAll(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxBinary+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxBinary {
		return nil, errors.New("binary too big")
	}
	if len(b) == 0 {
		return nil, errors.New("binary is empty")
	}
	return b, nil
}

// stagePattern names staged binaries: hidden, next to the real one so the
// final rename stays on one filesystem, and runnable on Windows.
func stagePattern() string {
	if runtime.GOOS == "windows" {
		return ".frost-update-*.exe"
	}
	return ".frost-update-*"
}

// stage writes bin next to exe with exe's permissions, synced to disk.
func stage(exe string, bin []byte) (string, error) {
	mode := fs.FileMode(0o755)
	if fi, err := os.Stat(exe); err == nil {
		mode = fi.Mode().Perm() | 0o100
	}
	f, err := os.CreateTemp(filepath.Dir(exe), stagePattern())
	if err != nil {
		return "", err
	}
	name := f.Name()
	_, err = f.Write(bin)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(name, mode)
	}
	if err != nil {
		os.Remove(name)
		return "", fmt.Errorf("writing the new binary: %w", err)
	}
	keepOwner(exe, name)
	return name, nil
}

// Cleanup removes what earlier updates left next to exe: old binaries
// Windows couldn't delete while they ran, and staged files from an update
// that was killed halfway.
func Cleanup(exe string) {
	dir, base := filepath.Split(exe)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		n := e.Name()
		switch {
		case isOld(n, base):
			os.Remove(filepath.Join(dir, n))
		case strings.HasPrefix(n, ".frost-update-") && n != lockName:
			if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > time.Hour {
				os.Remove(filepath.Join(dir, n))
			}
		}
	}
}

// isOld matches what replace renames a running binary to: <base>.old or
// <base>.<digits>.old.
func isOld(name, base string) bool {
	mid, ok := strings.CutPrefix(name, base+".")
	if !ok {
		return false
	}
	mid, ok = strings.CutSuffix(mid, "old")
	if !ok {
		return false
	}
	if mid == "" {
		return true
	}
	mid, ok = strings.CutSuffix(mid, ".")
	return ok && mid != "" && strings.Trim(mid, "0123456789") == ""
}

const lockName = ".frost-update.lock"

// lock stops two updates of the same binary running at once. A lock left by
// a crashed update expires after ten minutes.
func lock(dir string) (func(), error) {
	p := filepath.Join(dir, lockName)
	for range 2 {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			f.Close()
			return func() { os.Remove(p) }, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		fi, serr := os.Stat(p)
		if serr == nil && time.Since(fi.ModTime()) < 10*time.Minute {
			return nil, ErrBusy
		}
		os.Remove(p) // stale, or already gone
	}
	return nil, ErrBusy
}
