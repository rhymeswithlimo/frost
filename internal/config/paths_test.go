package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAddPath(t *testing.T) {
	dir := t.TempDir()
	docs := filepath.Join(dir, "Documents")
	os.MkdirAll(filepath.Join(docs, "taxes"), 0o755)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644)

	type tc struct {
		add     string
		have    []string
		wantErr string
		inside  int
	}
	cases := []tc{
		{add: docs + "/"},
		{add: "Documents", wantErr: "full path"},
		{add: filepath.Join(dir, "notes.txt"), wantErr: "a file"},
		{add: docs, have: []string{docs}, wantErr: "already on the list"},
		{add: docs + "/.", have: []string{docs}, wantErr: "already on the list"},
		{add: filepath.Join(docs, "taxes"), have: []string{docs}, wantErr: "inside"},
		{add: dir, have: []string{docs}, inside: 1},
		{add: filepath.Join(dir, "Missing"), have: []string{docs}},
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		cases = append(cases, tc{add: filepath.Join(dir, "documents"), have: []string{docs}, wantErr: "already on the list"})
	}
	for _, c := range cases {
		got, inside, err := AddPath(c.add, c.have)
		switch {
		case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
			t.Errorf("AddPath(%q, %v) = %q, %v; want error with %q", c.add, c.have, got, err, c.wantErr)
		case c.wantErr == "" && err != nil:
			t.Errorf("AddPath(%q, %v): %v", c.add, c.have, err)
		case len(inside) != c.inside:
			t.Errorf("AddPath(%q, %v): %d entries inside, want %d", c.add, c.have, len(inside), c.inside)
		}
	}
	if got, _, _ := AddPath(" "+docs+"/ ", nil); got != docs {
		t.Errorf("not tidied: %q", got)
	}
}
