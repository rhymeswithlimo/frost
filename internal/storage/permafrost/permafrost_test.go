package permafrost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rhymeswithlimo/frost/internal/crypto"
	"github.com/rhymeswithlimo/frost/internal/repo"
	"github.com/rhymeswithlimo/frost/internal/snapshot"
	"github.com/rhymeswithlimo/frost/internal/storage/storagetest"
)

func newTest(t *testing.T) (*Backend, *refServer) {
	t.Helper()
	ref := &refServer{token: "tok", pageSize: 2, objs: map[string][]byte{}}
	srv := httptest.NewServer(ref)
	t.Cleanup(srv.Close)
	b, err := New(srv.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	b.sleep = func(context.Context, time.Duration) error { return nil }
	return b, ref
}

func TestConformance(t *testing.T) {
	b, _ := newTest(t)
	storagetest.Conformance(t, b) // pageSize 2 also exercises pagination
}

func TestRetriesServerErrors(t *testing.T) {
	b, ref := newTest(t)
	ref.failNext = 2
	if err := b.Put(context.Background(), "chunks/aa/aa", []byte("x")); err != nil {
		t.Fatalf("put after transient errors: %v", err)
	}
	if ref.requests != 3 {
		t.Fatalf("requests = %d, want 3", ref.requests)
	}
}

func TestGivesUpEventually(t *testing.T) {
	b, ref := newTest(t)
	ref.failNext = 10
	err := b.Put(context.Background(), "a", []byte("x"))
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 503 {
		t.Fatalf("err = %v", err)
	}
	if ref.requests != maxAttempts {
		t.Fatalf("requests = %d, want %d", ref.requests, maxAttempts)
	}
}

func TestBadToken(t *testing.T) {
	b, ref := newTest(t)
	b.token = "wrong"
	err := b.Put(context.Background(), "a", []byte("x"))
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "unauthorized" {
		t.Fatalf("err = %v", err)
	}
	if ref.requests != 1 {
		t.Fatal("401 shouldn't be retried")
	}
}

func TestRequiresHTTPS(t *testing.T) {
	if _, err := New("http://permafrost.example.com", "t"); err == nil {
		t.Fatal("plain http to a remote host accepted")
	}
	if _, err := New("http://127.0.0.1:8080", "t"); err != nil {
		t.Fatalf("localhost http rejected: %v", err)
	}
	if _, err := New("https://permafrost.example.com", ""); err == nil {
		t.Fatal("empty token accepted")
	}
}

func TestLocationTellsAccountsApart(t *testing.T) {
	a, _ := New("https://pf.example.com", "one")
	b, _ := New("https://pf.example.com", "two")
	if a.String() != b.String() || a.Location() == b.Location() {
		t.Fatalf("accounts: %s %s, %s %s", a, a.Location(), b, b.Location())
	}
}

func TestRejectsRedirects(t *testing.T) {
	called := false
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer dest.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dest.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	b, err := New(source.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Get(context.Background(), "frost.repo"); err == nil {
		t.Fatal("redirect accepted")
	}
	if called {
		t.Fatal("followed credential-bearing redirect")
	}
}

func TestRejectsPaginationCycle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"keys":["a"],"next_cursor":"again"}`)) }))
	defer srv.Close()
	b, _ := New(srv.URL, "secret")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := b.List(ctx, ""); err == nil || ctx.Err() != nil {
		t.Fatalf("cycle not detected: %v", err)
	}
}

func TestRejectsUnsafeURLs(t *testing.T) {
	for _, u := range []string{"https://user:pass@example.com", "https://example.com?query=1", "https://example.com#fragment", "ftp://example.com"} {
		if _, err := New(u, "secret"); err == nil {
			t.Errorf("accepted %s", u)
		}
	}
	for _, u := range []string{"http://example.com/checkout", "file://example.com/key", "https://user:pass@example.com/checkout"} {
		if c, err := StartCheckout(u); err == nil {
			c.Close()
			t.Errorf("accepted checkout %s", u)
		}
	}
}

// A snapshot's file list can be far bigger than Permafrost's 16 MiB object
// limit, because it's stored in chunks.
func TestFileListLargerThanObjectLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("uploads about 35 MB")
	}
	b, _ := newTest(t)
	ctx := context.Background()
	k, _ := crypto.NewKey()
	r, err := repo.Init(ctx, b, k)
	if err != nil {
		t.Fatal(err)
	}
	rnd := rand.New(rand.NewSource(1))
	tree := &snapshot.Tree{}
	for i := range 1000 {
		f := snapshot.File{Path: fmt.Sprintf("/vm/disk-%04d.img", i), Type: snapshot.TypeFile, Size: 500 << 20}
		for range 500 {
			var id crypto.ID
			rnd.Read(id[:])
			f.Chunks = append(f.Chunks, id.String())
		}
		tree.Files = append(tree.Files, f)
	}
	// Stored as one sealed object, as it used to be, it's refused.
	raw, _ := json.Marshal(tree)
	if err := b.PutNew(ctx, "trees/whole", k.Seal(raw, "trees/whole")); err == nil {
		t.Fatalf("a %d byte file list fit in one object, so this proves nothing", len(raw))
	}
	s := snapshot.Snapshot{ID: snapshot.NewID()}
	if _, err := r.SaveSnapshot(ctx, s, tree, nil); err != nil {
		t.Fatal(err)
	}
	got, err := r.LoadTree(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != 1000 || got.Files[999].Chunks[499] != tree.Files[999].Chunks[499] {
		t.Fatal("file list didn't come back intact")
	}
}
