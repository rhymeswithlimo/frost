package desktop

import (
	"runtime"
	"testing"
)

func TestAvailable(t *testing.T) {
	for _, v := range []string{"SSH_CONNECTION", "SSH_TTY", "DISPLAY", "WAYLAND_DISPLAY"} {
		t.Setenv(v, "")
	}
	desktop := runtime.GOOS == "darwin" || runtime.GOOS == "windows"
	if Available() != desktop {
		t.Fatalf("no env: got %v", !desktop)
	}
	t.Setenv("DISPLAY", ":0")
	if !Available() {
		t.Fatal("with DISPLAY: not available")
	}
	t.Setenv("SSH_CONNECTION", "10.0.0.1 22 10.0.0.2 22")
	if Available() {
		t.Fatal("over SSH: available")
	}
}
