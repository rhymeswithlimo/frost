package snapshot

import "testing"

func TestCommonDir(t *testing.T) {
	for _, c := range []struct {
		paths []string
		want  string
	}{
		{nil, ""},
		{[]string{"/home/me/a.txt"}, "/home/me/a.txt"},
		{[]string{"/home/me/a.txt", "/home/me/b.txt"}, "/home/me"},
		{[]string{"/home/me/docs/a.txt", "/home/me/pics/b.png"}, "/home/me"},
		{[]string{"/home/me/ab", "/home/me/abc"}, "/home/me"},
		{[]string{"/home/me", "/srv"}, "/"},
		{[]string{"/"}, "/"},
		{[]string{"C:/Users/me/a.txt", "C:/Users/me/b.txt"}, "C:/Users/me"},
		{[]string{"C:/a", "C:/b"}, "C:/"},
		{[]string{"C:/a", "D:/b"}, ""},
	} {
		if got := CommonDir(c.paths); got != c.want {
			t.Errorf("%q: got %q, want %q", c.paths, got, c.want)
		}
	}
}

func TestRestoreBase(t *testing.T) {
	for _, c := range []struct {
		paths []string
		want  string
	}{
		{[]string{"/home/me/docs"}, "/home/me"},
		{[]string{"/home/me/a.txt"}, "/home/me"},
		{[]string{"/home/me/docs/a", "/home/me/pics/b"}, "/home/me"},
		{[]string{"/home/me/docs", "/home/me/docs/a"}, "/home/me"},
		{[]string{"/etc", "/home/me"}, "/"},
		{[]string{"C:/Users/me/docs"}, "C:/Users/me"},
		{[]string{"C:/docs"}, "C:/"},
		{[]string{"C:/a/b", "D:/c/d"}, ""},
	} {
		if got := RestoreBase(c.paths); got != c.want {
			t.Errorf("%q: got %q, want %q", c.paths, got, c.want)
		}
	}
}

func TestRestoreRel(t *testing.T) {
	for _, c := range []struct {
		p, base, want string
		bad           bool
	}{
		{"/home/me/docs/a.txt", "/home/me", "docs/a.txt", false},
		{"/home/me/docs", "/home/me", "docs", false},
		{"/etc/hosts", "/", "etc/hosts", false},
		{"C:/Users/me/a.txt", "C:/Users", "me/a.txt", false},
		{"C:/a", "C:/", "a", false},
		{"C:/a", "", "C/a", false},
		{"/home/me", "/home/me", "", true},
		{"/home", "/home/me", "", true},
		{"/home/meta/x", "/home/me", "", true},
		{"/home/me/../x", "/home/me", "", true},
	} {
		got, err := RestoreRel(c.p, c.base)
		if (err != nil) != c.bad || got != c.want {
			t.Errorf("RestoreRel(%q, %q) = %q, %v", c.p, c.base, got, err)
		}
	}
}

func TestIsRoot(t *testing.T) {
	for p, want := range map[string]bool{"/": true, "C:/": true, "": false, "/home": false, "C:/a": false} {
		if IsRoot(p) != want {
			t.Errorf("%q: got %v", p, !want)
		}
	}
}
