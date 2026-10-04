package update

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/rhymeswithlimo/frost/internal/config"
)

// State is what the background check remembers between runs, so status
// screens can say what happened without going online.
type State struct {
	Checked time.Time `json:"checked"`          // last check, successful or not
	Latest  string    `json:"latest,omitempty"` // newest release seen
	Error   string    `json:"error,omitempty"`  // why the last check or install failed

	// The last automatic install.
	Installed   string    `json:"installed,omitempty"`
	From        string    `json:"from,omitempty"`
	InstalledAt time.Time `json:"installed_at,omitzero"`
}

// LoadState reads the state file. A missing or damaged one is empty.
func LoadState(path string) State {
	var s State
	if b, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(b, &s) != nil {
			s = State{}
		}
	}
	return s
}

// Save writes the state file, replacing it in one rename.
func (s State) Save(path string) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return config.WritePrivate(path, append(b, '\n'))
}
