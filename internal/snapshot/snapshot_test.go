package snapshot

import (
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
