package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rhymeswithlimo/frost/internal/config"
	"github.com/rhymeswithlimo/frost/internal/storage"
)

// known is where this machine's backups were last opened, so a command can
// explain what changed when they can't be found. It's kept in the cache
// folder and holds no credentials.
type known struct {
	Storage config.Storage `json:"storage"`
	Shown   string         `json:"shown"` // storage.Backend.String, for messages
	Where   string         `json:"where"` // storage.Location
	RepoID  string         `json:"repo_id"`
	// Failed is the last backup that couldn't open the storage.
	Failed *failedRun `json:"failed,omitempty"`
}

type failedRun struct {
	Time  time.Time `json:"time"`
	Error string    `json:"error"`
}

// knownPath is one file per config folder, because --config-dir can point
// one machine at several sets of backups.
func knownPath() string {
	dir, err := filepath.Abs(config.Dir())
	if err != nil {
		dir = config.Dir()
	}
	sum := sha256.Sum256([]byte(dir))
	return filepath.Join(config.CacheDir(), "storage-"+hex.EncodeToString(sum[:4])+".json")
}

func loadKnown() known {
	var k known
	if raw, err := os.ReadFile(knownPath()); err == nil && json.Unmarshal(raw, &k) != nil {
		k = known{}
	}
	return k
}

func (k known) save() error {
	raw, err := json.MarshalIndent(k, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(config.CacheDir(), 0o700); err != nil {
		return err
	}
	return config.WritePrivate(knownPath(), append(raw, '\n'))
}

// withoutSecrets is s with its credentials cleared.
func withoutSecrets(s config.Storage) config.Storage {
	s.S3.AccessKeyID, s.S3.SecretAccessKey, s.Permafrost.Token = "", "", ""
	return s
}

// rememberStorage records that s, at b, opened repository id.
func rememberStorage(s config.Storage, b storage.Backend, id string) {
	k := known{Storage: withoutSecrets(s), Shown: b.String(), Where: storage.Location(b), RepoID: id}
	if old := loadKnown(); old.Storage == k.Storage && old.Where == k.Where && old.RepoID == k.RepoID && old.Failed == nil {
		return
	}
	k.save()
}

// rememberFailure records a backup that couldn't open the storage, for
// `status` to show.
func rememberFailure(err error) {
	k := loadKnown()
	k.Failed = &failedRun{Time: time.Now(), Error: err.Error()}
	k.save()
}

// storageError means the configured storage couldn't be opened as this
// machine's repository.
type storageError struct{ msg string }

func (e *storageError) Error() string { return e.msg }

// cantOpen explains why the repository at b (from settings s) didn't open:
// what was wrong, where this machine's backups were last found if that's
// somewhere else, and the ways out. missing means there's no repository at
// b at all.
func cantOpen(problem string, s config.Storage, b storage.Backend, missing bool) error {
	msg := problem
	if hint := storageHint(loadKnown(), s, b, missing); hint != "" {
		msg += "\n\n" + hint
	}
	return &storageError{msg}
}

func storageHint(k known, s config.Storage, b storage.Backend, missing bool) string {
	if k.Where == "" {
		return ""
	}
	if k.Where == storage.Location(b) {
		if !missing {
			return ""
		}
		return "This is where your backups were. If you moved them, move all of them back, or point frost at where they are now."
	}
	lines := []string{"Your backups were last opened in " + k.Shown + "."}
	changed := changedStorage(k.Storage, s)
	for _, c := range changed {
		lines = append(lines, fmt.Sprintf("Since then %s changed from %s to %s.", c.key, quoteValue(c.was), quoteValue(c.now)))
	}
	if len(changed) == 0 && s.Backend == "permafrost" {
		lines = append(lines, "Since then storage.permafrost.token changed, so this may be a different Permafrost account.")
	}
	back := "frost init, pointed back at " + k.Shown
	switch {
	case k.Storage.Backend != s.Backend:
	case len(changed) == 1:
		back = fmt.Sprintf("frost config set %s %s", changed[0].key, quoteValue(changed[0].was))
	case len(changed) > 1:
		// One `config set` at a time would be refused: halfway back
		// points at neither place.
		var olds []string
		for _, c := range changed {
			olds = append(olds, fmt.Sprintf("%s to %s", c.key, quoteValue(c.was)))
		}
		back = "frost config edit, and set " + strings.Join(olds, " and ")
	}
	lines = append(lines, "", "Do one of these:",
		"  put it back:       "+back,
		"  keep the change:   "+moveHint(s, b.String()),
		"  start over there:  frost init (your old backups stay where they are)")
	return strings.Join(lines, "\n")
}

// moveHint says how to move backups to shown, the new storage.
func moveHint(s config.Storage, shown string) string {
	if s.Backend == "s3" {
		return "move the whole folder (frost.repo, chunks/, snapshots/ and trees/) to " + shown
	}
	return "copy every object of your backups to " + shown
}

type change struct{ key, was, now string }

// changedStorage lists the settings that say where backups are and differ
// between was and now.
func changedStorage(was, now config.Storage) []change {
	var out []change
	add := func(key, a, b string) {
		if a != b {
			out = append(out, change{key, a, b})
		}
	}
	add("storage.backend", was.Backend, now.Backend)
	if was.Backend != now.Backend {
		return out
	}
	switch now.Backend {
	case "s3":
		add("storage.s3.endpoint", was.S3.Endpoint, now.S3.Endpoint)
		add("storage.s3.bucket", was.S3.Bucket, now.S3.Bucket)
		add("storage.s3.prefix", was.S3.Prefix, now.S3.Prefix)
	case "permafrost":
		add("storage.permafrost.url", was.Permafrost.URL, now.Permafrost.URL)
	}
	return out
}

func quoteValue(v string) string {
	if v == "" || strings.ContainsAny(v, " \t'\"") {
		return fmt.Sprintf("%q", v)
	}
	return v
}

// backupsElsewhere names where this machine's backups were last opened,
// when that isn't s. It's "" if they're in s or there's no record.
func backupsElsewhere(s config.Storage) string {
	k := loadKnown()
	b, err := newBackend(s)
	if k.Where == "" || err != nil || k.Where == storage.Location(b) {
		return ""
	}
	return k.Shown
}

// isStorageError reports whether err is a storage problem from openApp.
func isStorageError(err error) bool {
	var se *storageError
	return errors.As(err, &se)
}
