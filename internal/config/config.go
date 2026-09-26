// Package config loads and saves frost's human-editable TOML config and knows
// where frost keeps its files on each platform.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"
	"time"

	"github.com/BurntSushi/toml"
)

// Config is everything in config.toml.
type Config struct {
	Paths    []string `toml:"paths"`
	Exclude  []string `toml:"exclude"`
	Schedule Schedule `toml:"schedule"`
	Verify   Verify   `toml:"verify"`
	Storage  Storage  `toml:"storage"`
}

// Schedule controls automatic backups.
type Schedule struct {
	Enabled bool   `toml:"enabled"`
	Every   string `toml:"every"`
}

// Verify controls the post-backup spot check.
type Verify struct {
	Sample int `toml:"sample"`
}

// Storage picks and configures a backend.
type Storage struct {
	Backend    string     `toml:"backend"`
	S3         S3         `toml:"s3"`
	Permafrost Permafrost `toml:"permafrost"`
}

// S3 configures any S3-compatible endpoint.
type S3 struct {
	Endpoint        string `toml:"endpoint"`
	Region          string `toml:"region"`
	Bucket          string `toml:"bucket"`
	Prefix          string `toml:"prefix"`
	AccessKeyID     string `toml:"access_key_id"`
	SecretAccessKey string `toml:"secret_access_key"`
	Insecure        bool   `toml:"insecure"`
}

// Permafrost configures the hosted Permafrost backend.
type Permafrost struct {
	URL   string `toml:"url"`
	Token string `toml:"token"`
}

// Default returns a config with sensible defaults and no storage chosen.
func Default() Config {
	return Config{
		Exclude:  []string{".DS_Store", "Thumbs.db", "*.tmp", "*.swp", "node_modules", ".cache"},
		Schedule: Schedule{Enabled: true, Every: "daily"},
		Verify:   Verify{Sample: 20},
	}
}

// Intervals lists the accepted values for schedule.every.
var Intervals = []string{"hourly", "2h", "3h", "4h", "6h", "8h", "12h", "daily", "weekly"}

// Interval parses schedule.every into a duration.
func Interval(s string) (time.Duration, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "hourly", "1h":
		return time.Hour, nil
	case "daily", "24h":
		return 24 * time.Hour, nil
	case "weekly", "168h":
		return 7 * 24 * time.Hour, nil
	case "2h", "3h", "4h", "6h", "8h", "12h":
		return time.ParseDuration(s)
	}
	return 0, fmt.Errorf("schedule.every must be one of %s", strings.Join(Intervals, ", "))
}

// Validate checks the config is usable and explains what's wrong if not.
func (c *Config) Validate() error {
	var errs []error
	if len(c.Paths) == 0 {
		errs = append(errs, errors.New("paths is empty: add at least one directory to back up"))
	}
	if c.Schedule.Enabled {
		if _, err := Interval(c.Schedule.Every); err != nil {
			errs = append(errs, err)
		}
	}
	if c.Verify.Sample < 0 {
		errs = append(errs, errors.New("verify.sample can't be negative"))
	}
	switch c.Storage.Backend {
	case "s3":
		if c.Storage.S3.Endpoint == "" || c.Storage.S3.Bucket == "" {
			errs = append(errs, errors.New("storage.s3 needs an endpoint and a bucket"))
		}
	case "permafrost":
		// A blank url means the default server.
	case "":
		errs = append(errs, errors.New("storage.backend isn't set (s3 or permafrost)"))
	default:
		errs = append(errs, fmt.Errorf("unknown storage.backend %q (s3 or permafrost)", c.Storage.Backend))
	}
	return errors.Join(errs...)
}

// ExpandedPaths returns Paths with a leading ~ expanded.
func (c *Config) ExpandedPaths() []string { return expandAll(c.Paths) }

// ExpandedExclude returns Exclude with a leading ~ expanded.
func (c *Config) ExpandedExclude() []string { return expandAll(c.Exclude) }

func expandAll(in []string) []string {
	out := make([]string, len(in))
	for i, p := range in {
		out[i] = Expand(p)
	}
	return out
}

// Expand replaces a leading ~ with the home directory.
func Expand(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

// Dir is where config.toml and the key live. FROST_CONFIG_DIR overrides it.
func Dir() string {
	if d := os.Getenv("FROST_CONFIG_DIR"); d != "" {
		return d
	}
	if runtime.GOOS == "windows" {
		if d, err := os.UserConfigDir(); err == nil {
			return filepath.Join(d, "frost")
		}
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "frost")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "frost")
}

// CacheDir is where the manifest lives. FROST_CACHE_DIR overrides it.
func CacheDir() string {
	if d := os.Getenv("FROST_CACHE_DIR"); d != "" {
		return d
	}
	if runtime.GOOS == "windows" {
		if d, err := os.UserCacheDir(); err == nil {
			return filepath.Join(d, "frost")
		}
	}
	if d := os.Getenv("XDG_CACHE_HOME"); d != "" {
		return filepath.Join(d, "frost")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "frost")
}

// Path is the config file path.
func Path() string { return filepath.Join(Dir(), "config.toml") }

// KeyPath is the key file path.
func KeyPath() string { return filepath.Join(Dir(), "key") }

// ErrNoConfig means frost hasn't been set up yet.
var ErrNoConfig = errors.New("frost isn't set up yet, run `frost init`")

// Load reads the config file and applies environment overrides.
func Load() (Config, error) {
	c, err := LoadFile()
	if err != nil {
		return c, err
	}
	c.applyEnv()
	return c, nil
}

// LoadFile reads the config file as written, without environment overrides.
// Use it when the config will be saved again, so secrets from the
// environment never end up on disk.
func LoadFile() (Config, error) {
	c := Default()
	raw, err := os.ReadFile(Path())
	if errors.Is(err, os.ErrNotExist) {
		return c, ErrNoConfig
	}
	if err != nil {
		return c, err
	}
	err = Parse(raw, &c)
	return c, err
}

// Parse decodes TOML into c, rejecting unknown keys so typos don't pass silently.
func Parse(raw []byte, c *Config) error {
	md, err := toml.Decode(string(raw), c)
	if err != nil {
		return fmt.Errorf("%s: %w", Path(), err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		return fmt.Errorf("%s: unknown setting %q", Path(), und[0].String())
	}
	return nil
}

func (c *Config) applyEnv() {
	set := func(dst *string, names ...string) {
		for _, n := range names {
			if v := os.Getenv(n); v != "" {
				*dst = v
				return
			}
		}
	}
	set(&c.Storage.S3.AccessKeyID, "FROST_S3_ACCESS_KEY_ID", "AWS_ACCESS_KEY_ID")
	set(&c.Storage.S3.SecretAccessKey, "FROST_S3_SECRET_ACCESS_KEY", "AWS_SECRET_ACCESS_KEY")
	set(&c.Storage.Permafrost.Token, "FROST_PERMAFROST_TOKEN")
}

// Save writes the config with explanatory comments. The file is 0600
// because it can hold storage credentials.
func Save(c Config) error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	raw, err := Render(c)
	if err != nil {
		return err
	}
	tmp := Path() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, Path())
}

// Render produces the commented TOML for c.
func Render(c Config) ([]byte, error) {
	var buf bytes.Buffer
	err := fileTmpl.Execute(&buf, c)
	return buf.Bytes(), err
}

var fileTmpl = template.Must(template.New("config").Funcs(template.FuncMap{
	"q": func(v any) string {
		if l, ok := v.([]string); ok && len(l) == 0 {
			return "[]"
		}
		var b bytes.Buffer
		toml.NewEncoder(&b).Encode(map[string]any{"v": v})
		return strings.TrimSpace(strings.TrimPrefix(b.String(), "v = "))
	},
}).Parse(`# frost config. Edit by hand or with ` + "`frost config set`" + `.
# Nothing in here is secret except the storage credentials. Your encryption
# key lives in a separate file next to this one.

# Directories to back up. ~ means your home directory.
paths = {{q .Paths}}

# Patterns to skip. A bare name like "node_modules" or "*.tmp" matches that
# name anywhere. A pattern with a slash matches that path and everything under it.
exclude = {{q .Exclude}}

[schedule]
# Run backups automatically using your OS scheduler (no daemon).
enabled = {{.Schedule.Enabled}}
# hourly, 2h, 3h, 4h, 6h, 8h, 12h, daily or weekly.
every = {{q .Schedule.Every}}

[verify]
# After each backup, download this many random chunks and check them.
# 0 turns the check off.
sample = {{.Verify.Sample}}

[storage]
# "permafrost", or "s3" for any S3-compatible bucket.
backend = {{q .Storage.Backend}}

[storage.s3]
endpoint = {{q .Storage.S3.Endpoint}}
region = {{q .Storage.S3.Region}}
bucket = {{q .Storage.S3.Bucket}}
prefix = {{q .Storage.S3.Prefix}}
# Can also come from FROST_S3_ACCESS_KEY_ID / FROST_S3_SECRET_ACCESS_KEY.
access_key_id = {{q .Storage.S3.AccessKeyID}}
secret_access_key = {{q .Storage.S3.SecretAccessKey}}
# true to use plain http (local testing only).
insecure = {{.Storage.S3.Insecure}}

[storage.permafrost]
# Leave blank for the default server.
url = {{q .Storage.Permafrost.URL}}
# Can also come from FROST_PERMAFROST_TOKEN.
token = {{q .Storage.Permafrost.Token}}
`))
