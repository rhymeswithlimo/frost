package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestSignatureNamespaceMustBeFile(t *testing.T) {
	k := newSigningKey(t)
	msg := []byte("hello\n")
	if err := verifySSHSig(k.pub, msg, k.signNS(msg, "git")); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("other namespace accepted: %v", err)
	}
	if err := verifySSHSig(k.pub, msg, k.sign(msg)); err != nil {
		t.Fatal(err)
	}
}

// The release key lives in three places. They must agree, or releases
// signed for the installer won't be accepted by frost update, or the
// other way round.
func TestReleaseKeyMatchesInstaller(t *testing.T) {
	pub, err := os.ReadFile("../../install/release-signing.pub")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(pub)); got != releaseKey {
		t.Fatalf("install/release-signing.pub = %q, key.go has %q", got, releaseKey)
	}
	sh, err := os.ReadFile("../../install/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^RELEASE_KEY="(.*)"$`).FindSubmatch(sh)
	if m == nil || string(m[1]) != releaseKey {
		t.Fatalf("RELEASE_KEY in install.sh doesn't match key.go")
	}
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(releaseKey)); err != nil {
		t.Fatalf("releaseKey doesn't parse: %v", err)
	}
}

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"v0.2.0", "v0.1.0", true},
		{"v0.1.0", "v0.2.0", false},
		{"v0.1.0", "v0.1.0", false},
		{"v0.10.0", "v0.9.9", true},
		{"v1.0.0", "v0.99.99", true},
		{"v0.1.1", "v0.1.0", true},
		{"v0.1.0", "v0.1.0-rc1", true},
		{"v0.1.0-rc1", "v0.1.0", false},
		{"v0.1.0-rc.2", "v0.1.0-rc.1", true},
		{"v0.1.0-rc.10", "v0.1.0-rc.9", true},
		{"v0.1.0-beta", "v0.1.0-alpha", true},
		{"v0.1.0-alpha.1", "v0.1.0-alpha", true},
		{"v0.1.0-alpha", "v0.1.0-1", true},
		{"v0.2.0", "dev", false},
		{"dev", "v0.1.0", false},
		{"0.2.0", "v0.1.0", false},
		{"v0.2", "v0.1.0", false},
		{"v01.2.0", "v0.1.0", false},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestArchiveNameMatchesReleaseScript(t *testing.T) {
	for _, c := range []struct{ goos, arch, want string }{
		{"darwin", "arm64", "frost_1.2.3_darwin_arm64.tar.gz"},
		{"linux", "armv7", "frost_1.2.3_linux_armv7.tar.gz"},
		{"windows", "amd64", "frost_1.2.3_windows_amd64.zip"},
	} {
		if got := ArchiveName("v1.2.3", c.goos, c.arch); got != c.want {
			t.Errorf("ArchiveName = %q, want %q", got, c.want)
		}
	}
	// Every platform frost runs on must be one release.sh builds.
	script, err := os.ReadFile("../../scripts/release.sh")
	if err != nil {
		t.Fatal(err)
	}
	goos, arch := Platform()
	if !strings.Contains(string(script), goos+"/"+arch) {
		t.Errorf("release.sh doesn't build %s/%s", goos, arch)
	}
}

func TestLatestAndInstall(t *testing.T) {
	r := newRelease(t, "v0.2.0")
	rel, err := Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version != "v0.2.0" || !strings.HasPrefix(rel.Archive, "frost_0.2.0_") || !strings.HasSuffix(rel.Page, "/tag/v0.2.0") {
		t.Fatalf("release = %+v", rel)
	}
	exe := oldBinary(t)
	if runtime.GOOS == "windows" {
		stubProbe(t, nil)
	}
	if err := Install(context.Background(), rel, exe); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(exe)
	if !bytes.Equal(got, r.bin) {
		t.Fatal("binary wasn't replaced")
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(exe); fi.Mode().Perm() != 0o755 {
			t.Fatalf("mode = %v", fi.Mode())
		}
	}
	assertClean(t, exe)
}

func TestNoReleaseYet(t *testing.T) {
	for _, h := range []http.HandlerFunc{
		func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/releases", http.StatusFound) },
		func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) },
	} {
		srv := httptest.NewServer(h)
		setBase(t, srv.URL+"/releases")
		if _, err := Latest(context.Background()); !errors.Is(err, ErrNoRelease) {
			t.Fatalf("err = %v, want ErrNoRelease", err)
		}
		srv.Close()
	}
}

func TestRejectsBadSignature(t *testing.T) {
	r := newRelease(t, "v0.2.0")
	other := newSigningKey(t)
	r.files["checksums.txt.sig"] = other.sign(r.files["checksums.txt"])
	if _, err := Latest(context.Background()); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("err = %v", err)
	}
	delete(r.files, "checksums.txt.sig")
	if _, err := Latest(context.Background()); err == nil {
		t.Fatal("missing signature accepted")
	}
}

func TestRejectsTamperedChecksums(t *testing.T) {
	r := newRelease(t, "v0.2.0")
	r.files["checksums.txt"] = append(r.files["checksums.txt"], "0000 extra\n"...)
	if _, err := Latest(context.Background()); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("err = %v", err)
	}
}

// An old, properly signed release served under a new tag doesn't list the
// file the new tag needs, so it can't be used to roll a machine back.
func TestRejectsOldReleaseUnderNewTag(t *testing.T) {
	r := newRelease(t, "v0.2.0")
	r.tag = "v0.3.0"
	if _, err := Latest(context.Background()); err == nil || !strings.Contains(err.Error(), "no build for") {
		t.Fatalf("err = %v", err)
	}
}

func TestMissingPlatform(t *testing.T) {
	r := newRelease(t, "v0.2.0")
	r.resign("deadbeef  frost_0.2.0_plan9_mips.tar.gz\n")
	if _, err := Latest(context.Background()); err == nil || !strings.Contains(err.Error(), "no build for") {
		t.Fatalf("err = %v", err)
	}
}

func TestRejectsOddTag(t *testing.T) {
	r := newRelease(t, "v0.2.0")
	r.tag = "v0.2.0%2F..%2Fx"
	if _, err := Latest(context.Background()); err == nil || errors.Is(err, ErrNoRelease) {
		t.Fatalf("err = %v", err)
	}
}

func TestTamperedArchiveIsNotInstalled(t *testing.T) {
	r := newRelease(t, "v0.2.0")
	rel, err := Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r.files[rel.Archive] = r.archive([]byte("evil"))
	exe := oldBinary(t)
	stubProbe(t, nil)
	if err := Install(context.Background(), rel, exe); err == nil || !strings.Contains(err.Error(), "signed checksum") {
		t.Fatalf("err = %v", err)
	}
	assertOld(t, exe)
	assertClean(t, exe)
}

func TestBrokenBinaryIsNotInstalled(t *testing.T) {
	newRelease(t, "v0.2.0")
	rel, err := Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	exe := oldBinary(t)
	stubProbe(t, errors.New("exec format error"))
	if err := Install(context.Background(), rel, exe); err == nil {
		t.Fatal("broken binary installed")
	}
	assertOld(t, exe)
	assertClean(t, exe)
}

// The probe runs the real staged file and checks it names the version.
func TestProbeChecksVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "frost")
	os.WriteFile(bin, []byte("#!/bin/sh\necho frost version v0.1.9\n"), 0o755)
	if err := probe(context.Background(), bin, "v0.1.9"); err != nil {
		t.Fatal(err)
	}
	if err := probe(context.Background(), bin, "v0.2.0"); err == nil {
		t.Fatal("wrong version accepted")
	}
	os.WriteFile(bin, []byte("#!/bin/sh\nexit 3\n"), 0o755)
	if err := probe(context.Background(), bin, "v0.1.9"); err == nil {
		t.Fatal("failing binary accepted")
	}
}

func TestInstallWaitsForOtherUpdate(t *testing.T) {
	newRelease(t, "v0.2.0")
	rel, err := Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	exe := oldBinary(t)
	stubProbe(t, nil)
	unlock, err := lock(filepath.Dir(exe))
	if err != nil {
		t.Fatal(err)
	}
	if err := Install(context.Background(), rel, exe); !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
	unlock()
	// A lock left by a crash expires.
	lockPath := filepath.Join(filepath.Dir(exe), lockName)
	os.WriteFile(lockPath, []byte("1\n"), 0o600)
	old := time.Now().Add(-time.Hour)
	os.Chtimes(lockPath, old, old)
	if err := Install(context.Background(), rel, exe); err != nil {
		t.Fatal(err)
	}
}

func TestSizeLimits(t *testing.T) {
	r := newRelease(t, "v0.2.0")
	r.resign(strings.Repeat("x", maxSmall+1))
	if _, err := Latest(context.Background()); err == nil || !strings.Contains(err.Error(), "too big") {
		t.Fatalf("err = %v", err)
	}
}

func TestRetriesServerErrors(t *testing.T) {
	r := newRelease(t, "v0.2.0")
	backoff = time.Millisecond
	t.Cleanup(func() { backoff = time.Second })
	r.failures.Store(2)
	if _, err := Latest(context.Background()); err != nil {
		t.Fatalf("didn't retry: %v", err)
	}
}

func TestRefusesHTTPSDowngrade(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("x")) }))
	defer plain.Close()
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL, http.StatusFound)
	}))
	defer tls.Close()
	saved := client.Transport
	client.Transport = tls.Client().Transport
	t.Cleanup(func() { client.Transport = saved })
	if _, err := get(context.Background(), tls.URL, 10); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("err = %v", err)
	}
}

func TestExtract(t *testing.T) {
	var tgz bytes.Buffer
	gz := gzip.NewWriter(&tgz)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "./frost", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"})
	tw.WriteHeader(&tar.Header{Name: "./README.md", Typeflag: tar.TypeReg, Size: 2, Mode: 0o644})
	tw.Write([]byte("hi"))
	tw.WriteHeader(&tar.Header{Name: "./frost", Typeflag: tar.TypeReg, Size: 3, Mode: 0o755})
	tw.Write([]byte("bin"))
	tw.Close()
	gz.Close()
	b, err := extract("frost_1.0.0_linux_amd64.tar.gz", tgz.Bytes())
	if err != nil || string(b) != "bin" {
		t.Fatalf("tar: %q, %v", b, err)
	}

	var z bytes.Buffer
	zw := zip.NewWriter(&z)
	w, _ := zw.Create("frost.exe")
	w.Write([]byte("exe"))
	zw.Close()
	b, err = extract("frost_1.0.0_windows_amd64.zip", z.Bytes())
	if err != nil || string(b) != "exe" {
		t.Fatalf("zip: %q, %v", b, err)
	}

	if _, err := extract("frost_1.0.0_linux_amd64.tar.gz", []byte("not gzip")); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestCleanup(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "frost.exe")
	stale := time.Now().Add(-2 * time.Hour)
	for _, n := range []string{"frost.exe", "frost.exe.old", "frost.exe.123.old", "frost.exe.backup.old", "frost.exe.old.txt", ".frost-update-1", ".frost-update-2", "other"} {
		os.WriteFile(filepath.Join(dir, n), nil, 0o644)
	}
	os.Chtimes(filepath.Join(dir, ".frost-update-1"), stale, stale)
	Cleanup(exe)
	var left []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		left = append(left, e.Name())
	}
	want := ".frost-update-2 frost.exe frost.exe.backup.old frost.exe.old.txt other"
	if got := strings.Join(left, " "); got != want {
		t.Fatalf("left %q, want %q", got, want)
	}
}

func TestManaged(t *testing.T) {
	for p, want := range map[string]bool{
		"/opt/homebrew/Cellar/frost/0.1.0/bin/frost":     true,
		"/nix/store/abc-frost/bin/frost":                 true,
		"/usr/bin/frost":                                 true,
		`C:\Users\me\scoop\apps\frost\current\frost.exe`: true,
		"/usr/local/bin/frost":                           false,
		"/home/me/.local/bin/frost":                      false,
		`C:\Users\me\bin\frost.exe`:                      false,
	} {
		if got := Managed(p) != ""; got != want {
			t.Errorf("Managed(%q) = %v", p, got)
		}
	}
}

func TestState(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "update.json")
	if s := LoadState(p); !s.Checked.IsZero() {
		t.Fatal("missing state isn't empty")
	}
	now := time.Now().Truncate(time.Second)
	if err := (State{Checked: now, Latest: "v0.2.0"}).Save(p); err != nil {
		t.Fatal(err)
	}
	if s := LoadState(p); !s.Checked.Equal(now) || s.Latest != "v0.2.0" {
		t.Fatalf("state = %+v", s)
	}
	os.WriteFile(p, []byte("{nope"), 0o600)
	if s := LoadState(p); s.Latest != "" {
		t.Fatal("damaged state wasn't reset")
	}
}

// ---- helpers ----

type signingKey struct {
	signer ssh.Signer
	pub    string
}

func newSigningKey(t *testing.T) signingKey {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signingKey{s, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.PublicKey())))}
}

func (k signingKey) sign(msg []byte) []byte { return k.signNS(msg, "file") }

// signNS makes a signature in the format ssh-keygen -Y sign writes.
func (k signingKey) signNS(msg []byte, ns string) []byte {
	h := sha512.Sum512(msg)
	signed := append([]byte("SSHSIG"), ssh.Marshal(struct {
		N string
		R []byte
		H string
		D []byte
	}{ns, nil, "sha512", h[:]})...)
	sig, err := k.signer.Sign(rand.Reader, signed)
	if err != nil {
		panic(err)
	}
	blob := append([]byte("SSHSIG"), ssh.Marshal(struct {
		V uint32
		P []byte
		N string
		R []byte
		H string
		S []byte
	}{1, k.signer.PublicKey().Marshal(), ns, nil, "sha512", ssh.Marshal(sig)})...)
	b64 := base64.StdEncoding.EncodeToString(blob)
	var out strings.Builder
	out.WriteString("-----BEGIN SSH SIGNATURE-----\n")
	for len(b64) > 70 {
		out.WriteString(b64[:70] + "\n")
		b64 = b64[70:]
	}
	out.WriteString(b64 + "\n-----END SSH SIGNATURE-----\n")
	return []byte(out.String())
}

// fakeRelease serves one release the way GitHub does.
type fakeRelease struct {
	key      signingKey
	tag      string
	bin      []byte
	files    map[string][]byte
	failures atomic.Int32 // answer this many requests with a 502 first
}

func newRelease(t *testing.T, tag string) *fakeRelease {
	r := &fakeRelease{key: newSigningKey(t), tag: tag, files: map[string][]byte{}}
	r.bin = []byte("#!/bin/sh\necho frost version " + tag + "\n")
	goos, arch := Platform()
	name := ArchiveName(tag, goos, arch)
	r.files[name] = r.archive(r.bin)
	sum := sha256.Sum256(r.files[name])
	r.resign(fmt.Sprintf("%x  %s\n%x  frost_%s_plan9_mips.tar.gz\n", sum, name, sum, strings.TrimPrefix(tag, "v")))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if r.failures.Add(-1) >= 0 {
			http.Error(w, "busy", http.StatusBadGateway)
			return
		}
		if req.URL.Path == "/releases/latest" {
			http.Redirect(w, req, "/releases/tag/"+r.tag, http.StatusFound)
			return
		}
		name, ok := strings.CutPrefix(req.URL.Path, "/releases/download/"+r.tag+"/")
		b, found := r.files[name]
		if !ok || !found {
			http.NotFound(w, req)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	setBase(t, srv.URL+"/releases")
	saved := trustedKey
	trustedKey = r.key.pub
	t.Cleanup(func() { trustedKey = saved })
	return r
}

func (r *fakeRelease) resign(sums string) {
	r.files["checksums.txt"] = []byte(sums)
	r.files["checksums.txt.sig"] = r.key.sign([]byte(sums))
}

func (r *fakeRelease) archive(bin []byte) []byte {
	var buf bytes.Buffer
	if runtime.GOOS == "windows" {
		zw := zip.NewWriter(&buf)
		w, _ := zw.Create("frost.exe")
		w.Write(bin)
		zw.Close()
		return buf.Bytes()
	}
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755})
	tw.WriteHeader(&tar.Header{Name: "./LICENSE", Typeflag: tar.TypeReg, Size: 3, Mode: 0o644})
	tw.Write([]byte("MIT"))
	tw.WriteHeader(&tar.Header{Name: "./frost", Typeflag: tar.TypeReg, Size: int64(len(bin)), Mode: 0o755})
	tw.Write(bin)
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func setBase(t *testing.T, u string) {
	saved := BaseURL
	BaseURL = u
	t.Cleanup(func() { BaseURL = saved })
}

func stubProbe(t *testing.T, err error) {
	saved := probe
	probe = func(context.Context, string, string) error { return err }
	t.Cleanup(func() { probe = saved })
}

func oldBinary(t *testing.T) string {
	name := "frost"
	if runtime.GOOS == "windows" {
		name = "frost.exe"
	}
	exe := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

func assertOld(t *testing.T, exe string) {
	t.Helper()
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Fatalf("binary changed to %q", b)
	}
}

// assertClean checks nothing but the binary is left next to it.
func assertClean(t *testing.T, exe string) {
	t.Helper()
	entries, _ := os.ReadDir(filepath.Dir(exe))
	for _, e := range entries {
		if e.Name() != filepath.Base(exe) && !strings.HasSuffix(e.Name(), ".old") {
			t.Errorf("left behind: %s", e.Name())
		}
	}
}
