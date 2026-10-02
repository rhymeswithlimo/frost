package cli

import (
	"path/filepath"
	"testing"
)

func TestRerunCommandNamesTheSnapshot(t *testing.T) {
	got := rerunCommand("maple-otter-3f1c", []string{"/home/me/My Documents"}, true, "", false)
	want := "frost restore maple-otter-3f1c '" + filepath.FromSlash("/home/me/My Documents") + "' --beside"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if got := rerunCommand("x", nil, false, "/tmp/out", false); got != "frost restore x --to /tmp/out" {
		t.Fatalf("--to: %s", got)
	}
	if got := rerunCommand("x", nil, false, "", true); got != "frost restore x --overwrite" {
		t.Fatalf("--overwrite: %s", got)
	}
}
