package snapshot

import (
	"runtime"
	"testing"
	"time"
)

func TestSafeRelPreservesUnixColon(t *testing.T) {
	if runtime.GOOS == "windows" {
		return
	}
	if got, err := SafeRel("/:file"); err != nil || got != ":file" {
		t.Fatalf("%q, %v", got, err)
	}
}

func TestSafeRelWindowsAliases(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path semantics")
	}
	for _, p := range []string{"/NUL", "/COM1", "/x/file:stream", "/x/trailing.", "/x/trailing ", "/x/\x00"} {
		if _, err := SafeRel(p); err == nil {
			t.Errorf("accepted %q", p)
		}
	}
}

func TestRelativeTimeOverflow(t *testing.T) {
	for _, p := range []string{"999999999999999999999 years ago", "999999999999999999 hours ago", "100000 weeks ago"} {
		if _, err := ParseTime(p, time.Now()); err == nil {
			t.Errorf("accepted %q", p)
		}
	}
}
