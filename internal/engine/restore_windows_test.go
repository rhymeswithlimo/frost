package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Short names like RUNNER~1 aren't links; junctions are.
func TestResolveParentWindows(t *testing.T) {
	dir := t.TempDir() // often has a short name in it on CI
	if _, err := resolveParent(dir); err != nil {
		t.Fatalf("plain folder refused: %v", err)
	}
	real := filepath.Join(dir, "real")
	os.Mkdir(real, 0o700)
	junction := filepath.Join(dir, "junction")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", junction, real).CombinedOutput(); err != nil {
		t.Skipf("can't make a junction: %v %s", err, out)
	}
	if _, err := resolveParent(filepath.Join(junction, "sub")); err == nil || !strings.Contains(err.Error(), "link") {
		t.Fatalf("junction accepted: %v", err)
	}
	if err := CanOverwrite([]string{filepath.ToSlash(filepath.Join(junction, "file"))}); err == nil || !strings.Contains(err.Error(), "link") {
		t.Fatalf("overwrite through a junction: %v", err)
	}
}
