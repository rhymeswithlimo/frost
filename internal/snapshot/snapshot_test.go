package snapshot

import (
	"maps"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestNewID(t *testing.T) {
	id := NewID()
	if !ValidID(id) {
		t.Fatalf("NewID produced invalid id %q", id)
	}
	if NewID() == id {
		t.Fatal("two IDs in a row were equal")
	}
}

func TestShort(t *testing.T) {
	cases := map[string]string{
		"maple-absurd-3f1c9a0b2e7": "maple-absurd-3f1c",
		"apple-bread-0001":         "apple-bread-0001", // already short
		"apple-bread-00012":        "apple-bread-0001",
		"x":                        "x",
		"":                         "",
	}
	for id, want := range cases {
		if got := Short(id); got != want {
			t.Errorf("Short(%q) = %q, want %q", id, got, want)
		}
	}
	id := NewID()
	if got := Short(id); len(got) != len(id)-7 || !strings.HasPrefix(id, got) {
		t.Errorf("Short(%q) = %q, want the words and 4 hex characters", id, got)
	}
}

func TestShorten(t *testing.T) {
	snaps := []Snapshot{
		{ID: "maple-absurd-3f1c9a0b2e7"},
		{ID: "maple-absurd-3f1c0000000"}, // same 4 characters as the first
		{ID: "maple-absurd-3f2d0000000"},
		{ID: "maple-acid-3f1c9a0b2e7"},
		{ID: "old-style-0001"},
		{ID: "old-style-00011"}, // starts with the whole of the one before
		{ID: "x"},
	}
	want := ShortIDs{
		"maple-absurd-3f1c9a0b2e7": "maple-absurd-3f1c9",
		"maple-absurd-3f1c0000000": "maple-absurd-3f1c0",
		"maple-absurd-3f2d0000000": "maple-absurd-3f2d",
		"maple-acid-3f1c9a0b2e7":   "maple-acid-3f1c",
		"old-style-0001":           "old-style-0001",
		"old-style-00011":          "old-style-00011",
		"x":                        "x",
	}
	got := Shorten(snaps)
	if !maps.Equal(got, want) {
		t.Fatalf("Shorten =\n%v\nwant\n%v", got, want)
	}
	if dup := Shorten(append(slices.Clone(snaps), snaps[0])); !maps.Equal(dup, want) {
		t.Errorf("a duplicate ID changed Shorten to\n%v", dup)
	}
	if s := got.Of("birch-cable-0123456789a"); s != "birch-cable-0123" {
		t.Errorf("Of an unknown ID = %q, want Short of it", s)
	}
	if s := ShortIDs(nil).Of("birch-cable-0123456789a"); s != "birch-cable-0123" {
		t.Errorf("Of on a nil map = %q", s)
	}

	// Every short ID typed back picks the snapshot it was shown for.
	for id, short := range got {
		if s, err := Resolve(snaps, short, time.Now()); err != nil || s.ID != id {
			t.Errorf("Resolve(%q) = %q, %v, want %q", short, s.ID, err, id)
		}
	}
}

func TestResolve(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	snaps := []Snapshot{
		{ID: "apple-bread-0001", Time: now.Add(-1 * time.Hour)},
		{ID: "apple-cloud-0002", Time: now.Add(-50 * time.Hour)},
		{ID: "delta-echo-0003", Time: now.Add(-10 * 24 * time.Hour)},
	}
	cases := map[string]string{
		"":                 "apple-bread-0001",
		"latest":           "apple-bread-0001",
		"delta":            "delta-echo-0003",
		"apple-cloud-0002": "apple-cloud-0002",
		"2 hours ago":      "apple-cloud-0002",
		"2 days ago":       "apple-cloud-0002",
		"3d":               "delta-echo-0003",
		"1 week ago":       "delta-echo-0003",
		"yesterday":        "apple-cloud-0002",
		"2026-09-24":       "apple-cloud-0002",
		"2026-09-16":       "delta-echo-0003",
	}
	for sel, want := range cases {
		got, err := Resolve(snaps, sel, now)
		if err != nil {
			t.Errorf("Resolve(%q): %v", sel, err)
			continue
		}
		if got.ID != want {
			t.Errorf("Resolve(%q) = %s, want %s", sel, got.ID, want)
		}
	}
	for _, sel := range []string{"apple", "1 year ago", "nonsense words"} {
		if _, err := Resolve(snaps, sel, now); err == nil {
			t.Errorf("Resolve(%q) should fail", sel)
		}
	}
}

func TestSafeRel(t *testing.T) {
	ok := map[string]string{
		"/home/me/a.txt": "home/me/a.txt",
		"C:/Users/me/a":  "C/Users/me/a",
		"/a/./b":         "a/b",
	}
	for in, want := range ok {
		got, err := SafeRel(in)
		if err != nil || got != want {
			t.Errorf("SafeRel(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"/../etc/passwd", "/a/../../b", "/", "", "a/../.."} {
		if got, err := SafeRel(in); err == nil {
			t.Errorf("SafeRel(%q) = %q, want error", in, got)
		}
	}
}

func TestDiff(t *testing.T) {
	a := &Tree{Files: []File{
		{Path: "/x/keep", Type: TypeFile, Chunks: []string{"1"}},
		{Path: "/x/change", Type: TypeFile, Chunks: []string{"2"}},
		{Path: "/x/gone", Type: TypeFile},
		{Path: "/x/touched", Type: TypeFile, ModTime: time.Unix(1, 0)},
	}}
	b := &Tree{Files: []File{
		{Path: "/x/keep", Type: TypeFile, Chunks: []string{"1"}},
		{Path: "/x/change", Type: TypeFile, Chunks: []string{"3"}},
		{Path: "/x/new", Type: TypeFile},
		{Path: "/x/touched", Type: TypeFile, ModTime: time.Unix(2, 0)},
	}}
	got := Diff(a, b)
	want := []struct {
		p string
		k ChangeKind
	}{{"/x/change", Modified}, {"/x/gone", Removed}, {"/x/new", Added}}
	if len(got) != len(want) {
		t.Fatalf("Diff = %+v", got)
	}
	for i, w := range want {
		if got[i].Path != w.p || got[i].Kind != w.k {
			t.Errorf("change %d = %s %s, want %s %s", i, got[i].Kind, got[i].Path, w.k, w.p)
		}
	}
}

func TestSafeRelBackslash(t *testing.T) {
	if runtime.GOOS != "windows" {
		// Elsewhere a backslash is part of a name, and `..\b` is a legal file.
		if _, err := SafeRel(`/home/a/..\b`); err != nil {
			t.Errorf("SafeRel rejected a legal Unix name: %v", err)
		}
		return
	}
	for _, p := range []string{`C:/x/..\..\evil`, `/home/a/..\b`, `a\..\..\b`} {
		if _, err := SafeRel(p); err == nil {
			t.Errorf("SafeRel(%q) accepted a path that climbs out on Windows", p)
		}
	}
	if got, err := SafeRel("C:/Users/me/notes..txt"); err != nil || got != "C/Users/me/notes..txt" {
		t.Errorf("SafeRel rejected or changed a normal path: %q, %v", got, err)
	}
}
