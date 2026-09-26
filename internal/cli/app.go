package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rhymeswithlimo/frost/internal/config"
	"github.com/rhymeswithlimo/frost/internal/crypto"
	"github.com/rhymeswithlimo/frost/internal/engine"
	"github.com/rhymeswithlimo/frost/internal/manifest"
	"github.com/rhymeswithlimo/frost/internal/repo"
	"github.com/rhymeswithlimo/frost/internal/storage"
	"github.com/rhymeswithlimo/frost/internal/storage/permafrost"
	"github.com/rhymeswithlimo/frost/internal/storage/s3"
)

// newBackend builds the storage backend the config asks for.
func newBackend(c config.Storage) (storage.Backend, error) {
	switch c.Backend {
	case "s3":
		return s3.New(s3.Config{
			Endpoint:        c.S3.Endpoint,
			Region:          c.S3.Region,
			Bucket:          c.S3.Bucket,
			Prefix:          c.S3.Prefix,
			AccessKeyID:     c.S3.AccessKeyID,
			SecretAccessKey: c.S3.SecretAccessKey,
			Insecure:        c.S3.Insecure,
		})
	case "permafrost":
		u := c.Permafrost.URL
		if u == "" {
			u = permafrost.DefaultURL
		}
		return permafrost.New(u, c.Permafrost.Token)
	case "":
		return nil, errors.New("no storage backend configured, run `frost init`")
	}
	return nil, fmt.Errorf("unknown storage backend %q", c.Backend)
}

// ErrNoKey means there's no key file on this machine.
var ErrNoKey = errors.New("no key on this machine, run `frost init` or `frost key import`")

// loadKey reads the local key file.
func loadKey() (*crypto.Key, error) {
	raw, err := os.ReadFile(config.KeyPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoKey
	}
	if err != nil {
		return nil, err
	}
	k, err := crypto.KeyFromPhrase(string(raw))
	if err != nil {
		return nil, fmt.Errorf("key file %s is damaged: %w", config.KeyPath(), err)
	}
	return k, nil
}

// saveKey writes the key file, readable only by the current user.
func saveKey(k *crypto.Key) error {
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		return err
	}
	tmp := config.KeyPath() + ".tmp"
	if err := os.WriteFile(tmp, []byte(k.Phrase()+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, config.KeyPath())
}

// app is everything a command needs once frost is set up.
type app struct {
	cfg    config.Config
	engine *engine.Engine
}

func (a *app) Close() { a.engine.Manifest.Close() }

// openApp loads config and key, connects to the repository and opens the
// manifest. Callers must Close it.
func openApp(ctx context.Context) (*app, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	key, err := loadKey()
	if err != nil {
		return nil, err
	}
	b, err := newBackend(cfg.Storage)
	if err != nil {
		return nil, err
	}
	r, err := repo.Open(ctx, b, key)
	switch {
	case errors.Is(err, repo.ErrNotInitialized):
		return nil, fmt.Errorf("%s has no frost repository, run `frost init`", b)
	case errors.Is(err, repo.ErrWrongKey):
		return nil, fmt.Errorf("the key on this machine doesn't match %s (check it with `frost key verify`)", b)
	case err != nil:
		return nil, fmt.Errorf("connecting to %s: %w", b, err)
	}
	m, err := manifest.Open(manifestPath(r.Info.ID))
	if errors.Is(err, manifest.ErrLocked) {
		return nil, errors.New("a backup or restore is already running, try again when it's done")
	}
	if err != nil {
		return nil, err
	}
	return &app{cfg: cfg, engine: &engine.Engine{Repo: r, Manifest: m}}, nil
}

func manifestPath(repoID string) string {
	return filepath.Join(config.CacheDir(), "manifest-"+repoID+".db")
}

func logPath() string { return filepath.Join(config.CacheDir(), "frost.log") }

// tildify shortens a path under the home directory to ~/...
func tildify(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(p, home+string(filepath.Separator)); ok {
		return "~" + string(filepath.Separator) + rest
	}
	if rest, ok := strings.CutPrefix(p, filepath.ToSlash(home)+"/"); ok {
		return "~/" + rest
	}
	return p
}
