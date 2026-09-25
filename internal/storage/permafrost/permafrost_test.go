package permafrost

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

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
