// Package update finds, checks and installs new frost releases.
//
// A release is trusted only if its checksums.txt carries a valid signature
// by the pinned release key, and the archive for this platform matches the
// SHA-256 listed there. The version comes from the archive's name inside
// that signed file, so an old release can't be passed off as a new one.
package update

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Repo is the GitHub repository releases are published to.
const Repo = "rhymeswithlimo/frost"

// BaseURL is the releases page: BaseURL/latest redirects to the newest
// release, and assets live under BaseURL/download/<tag>/. Tests point it at
// a local server.
var BaseURL = "https://github.com/" + Repo + "/releases"

// trustedKey is the key checksums.txt must be signed with. Tests swap it.
var trustedKey = releaseKey

var (
	// ErrDevBuild means this binary wasn't built by the release script, so
	// there's no version to compare with.
	ErrDevBuild = errors.New("this frost was built from source, so it can't update itself. Rebuild it, or install a release with the installer: https://github.com/" + Repo + "#install")
	// ErrNoRelease means nothing has been published yet.
	ErrNoRelease = errors.New("no frost release has been published yet")
)

// Size caps, so a bad server can't make frost read forever.
const (
	maxSmall   = 1 << 16   // checksums.txt, its signature
	maxArchive = 128 << 20 // a release archive
	maxBinary  = 256 << 20 // the binary inside it
)

// Release is a verified release for this platform.
type Release struct {
	Version string // e.g. v0.2.0
	Archive string // file name for this platform
	Page    string // release notes
	sum     []byte // SHA-256 of Archive, from the signed checksums.txt
}

var versionRe = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$`)

// Valid reports whether v is a release version like v1.2.3 or v1.2.3-rc1.
// "dev" and anything else built without the release script isn't.
func Valid(v string) bool { return versionRe.MatchString(v) }

// Newer reports whether version a is newer than b. Invalid versions are
// never newer, and nothing is newer than an invalid b.
func Newer(a, b string) bool {
	x, y := versionRe.FindStringSubmatch(a), versionRe.FindStringSubmatch(b)
	if x == nil || y == nil {
		return false
	}
	for i := 1; i <= 3; i++ {
		p, _ := strconv.ParseUint(x[i], 10, 64)
		q, _ := strconv.ParseUint(y[i], 10, 64)
		if p != q {
			return p > q
		}
	}
	return comparePre(strings.TrimPrefix(x[4], "-"), strings.TrimPrefix(y[4], "-")) > 0
}

// comparePre orders pre-release suffixes the semver way: none beats any,
// then dot-separated parts compare numerically or as text.
func comparePre(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		n, errN := strconv.ParseUint(as[i], 10, 64)
		m, errM := strconv.ParseUint(bs[i], 10, 64)
		switch {
		case errN == nil && errM == nil:
			if n != m {
				return cmpInt(n > m)
			}
		case errN == nil: // numbers sort before words
			return -1
		case errM == nil:
			return 1
		case as[i] != bs[i]:
			return cmpInt(as[i] > bs[i])
		}
	}
	return cmpInt(len(as) > len(bs))
}

func cmpInt(gt bool) int {
	if gt {
		return 1
	}
	return -1
}

// Platform is the os and arch in release file names, e.g. linux, armv7.
func Platform() (goos, arch string) {
	goos, arch = runtime.GOOS, runtime.GOARCH
	switch {
	case arch == "arm":
		arch = "armv7"
	case goos == "darwin" && arch == "amd64" && appleSilicon():
		arch = "arm64" // running under Rosetta: switch to the native build
	}
	return goos, arch
}

// ArchiveName is the release file for a version and platform. It must match
// scripts/release.sh and install/install.sh.
func ArchiveName(version, goos, arch string) string {
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("frost_%s_%s_%s.%s", strings.TrimPrefix(version, "v"), goos, arch, ext)
}

// Latest finds the newest release and checks its signed checksums.txt.
// Pre-releases are never picked.
func Latest(ctx context.Context) (Release, error) {
	tag, err := latestTag(ctx)
	if err != nil {
		return Release{}, err
	}
	base := BaseURL + "/download/" + url.PathEscape(tag) + "/"
	sums, err := get(ctx, base+"checksums.txt", maxSmall)
	if err != nil {
		return Release{}, fmt.Errorf("downloading checksums.txt for %s: %w", tag, err)
	}
	sig, err := get(ctx, base+"checksums.txt.sig", maxSmall)
	if err != nil {
		return Release{}, fmt.Errorf("downloading checksums.txt.sig for %s: %w", tag, err)
	}
	if err := verifySSHSig(trustedKey, sums, sig); err != nil {
		return Release{}, fmt.Errorf("%s: %w", tag, err)
	}
	goos, arch := Platform()
	name := ArchiveName(tag, goos, arch)
	sum, ok := lookup(sums, name)
	if !ok {
		return Release{}, fmt.Errorf("%s has no build for %s/%s", tag, goos, arch)
	}
	return Release{Version: tag, Archive: name, Page: BaseURL + "/tag/" + url.PathEscape(tag), sum: sum}, nil
}

// latestTag follows BaseURL/latest one hop and reads the tag from where it
// points. That's a plain page redirect, so there's no API rate limit.
func latestTag(ctx context.Context) (string, error) {
	resp, err := do(ctx, BaseURL+"/latest", noRedirect)
	if err != nil {
		return "", fmt.Errorf("checking for the latest release: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, maxSmall))
	if resp.StatusCode == http.StatusNotFound {
		return "", ErrNoRelease
	}
	if resp.StatusCode < 300 || resp.StatusCode > 399 {
		return "", fmt.Errorf("checking for the latest release: %s", resp.Status)
	}
	loc, err := resp.Location()
	if err != nil {
		return "", fmt.Errorf("checking for the latest release: %w", err)
	}
	// github.com/<repo>/releases/tag/<tag>, or /releases when there's none.
	_, tag, ok := strings.Cut(loc.Path, "/releases/tag/")
	if !ok {
		return "", ErrNoRelease
	}
	if !Valid(tag) {
		return "", fmt.Errorf("the latest release has an odd tag %q", tag)
	}
	return tag, nil
}

// lookup finds name's SHA-256 in a sha256sum-style checksums file.
func lookup(sums []byte, name string) ([]byte, bool) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 || strings.TrimPrefix(f[1], "*") != name {
			continue
		}
		sum, err := hex.DecodeString(f[0])
		if err != nil || len(sum) != sha256.Size {
			return nil, false
		}
		return sum, true
	}
	return nil, false
}

// UserAgent is sent with every request. The CLI adds its version.
var UserAgent = "frost"

var (
	errTooManyRedirects = errors.New("too many redirects")
	errInsecureRedirect = errors.New("redirected away from https")
)

// transport gives up on a server that accepts the connection but never
// answers.
var transport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = 30 * time.Second
	return t
}()

var client = &http.Client{Transport: transport, Timeout: 10 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errTooManyRedirects
	}
	if via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
		return errInsecureRedirect
	}
	return nil
}}

var noRedirect = &http.Client{Transport: transport, Timeout: time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}}

// backoff is the wait before the first retry. Tests shorten it.
var backoff = time.Second

// do sends a GET, retrying network errors and server errors a couple of
// times. The caller closes the body.
func do(ctx context.Context, u string, c *http.Client) (*http.Response, error) {
	var last error
	for attempt := range 3 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * backoff):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", UserAgent)
		resp, err := c.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if errors.Is(err, errInsecureRedirect) || errors.Is(err, errTooManyRedirects) {
				return nil, err // the same thing would happen again
			}
			last = err
			continue
		}
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			last = errors.New(resp.Status)
			continue
		}
		return resp, nil
	}
	return nil, last
}

// get downloads u, refusing anything over limit bytes.
func get(ctx context.Context, u string, limit int64) ([]byte, error) {
	resp, err := do(ctx, u, client)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(resp.Status)
	}
	if resp.ContentLength > limit {
		return nil, fmt.Errorf("too big (%d bytes)", resp.ContentLength)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("too big")
	}
	return b, nil
}
