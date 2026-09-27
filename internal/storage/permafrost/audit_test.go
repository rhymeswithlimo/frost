package permafrost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

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
