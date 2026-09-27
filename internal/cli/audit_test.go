package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rhymeswithlimo/frost/internal/config"
	"github.com/rhymeswithlimo/frost/internal/crypto"
)

func TestConfigGetMasksSecret(t *testing.T) {
	setup(t)
	cfg := config.Default()
	cfg.Storage.Permafrost.Token = "do-not-print"
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if got := must(t, "", "config", "get", "storage.permafrost.token"); strings.Contains(got, cfg.Storage.Permafrost.Token) {
		t.Fatal("secret leaked")
	}
	if got := must(t, "", "config", "get", "storage.permafrost.token", "--show-secrets"); !strings.Contains(got, cfg.Storage.Permafrost.Token) {
		t.Fatal("explicit secret request ignored")
	}
}

func TestKeyVerifyMismatchWithoutConfig(t *testing.T) {
	f := setup(t)
	if err := saveKey(f.key); err != nil {
		t.Fatal(err)
	}
	other, _ := crypto.NewKey()
	if _, err := run(t, other.Phrase()+"\n", "key", "verify"); err == nil {
		t.Fatal("wrong key reported success")
	}
}

func TestLogTrimRefusesSymlink(t *testing.T) {
	setup(t)
	outside := filepath.Join(t.TempDir(), "data")
	if err := os.WriteFile(outside, make([]byte, (1<<20)+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, logPath()); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	trimLog()
	if info, err := os.Stat(outside); err != nil || info.Size() != (1<<20)+1 {
		t.Fatal("symlink target truncated")
	}
}
