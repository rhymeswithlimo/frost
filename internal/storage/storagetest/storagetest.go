// Package storagetest has an in-memory backend and a conformance suite that
// every real backend's tests run against. It's for tests only.
package storagetest

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/rhymeswithlimo/frost/internal/storage"
)

// Mem is an in-memory Backend.
type Mem struct {
	mu   sync.Mutex
	objs map[string][]byte
	Puts int // number of successful Put calls
	Gets int // number of successful Get calls
}

// NewMem returns an empty in-memory backend.
func NewMem() *Mem { return &Mem{objs: map[string][]byte{}} }

func (m *Mem) Put(_ context.Context, key string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objs[key] = bytes.Clone(data)
	m.Puts++
	return nil
}

func (m *Mem) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.objs[key]
	if !ok {
		return nil, storage.ErrNotFound
	}
	m.Gets++
	return bytes.Clone(d), nil
}

func (m *Mem) List(_ context.Context, prefix string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var keys []string
	for k := range m.objs {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (m *Mem) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objs, key)
	return nil
}

func (m *Mem) String() string { return "memory" }

// Raw gives direct access to a stored object, for tampering in tests.
func (m *Mem) Raw(key string) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.objs[key]
}

// SetRaw overwrites a stored object without counting it as a Put.
func (m *Mem) SetRaw(key string, data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objs[key] = data
}

// Conformance checks that b behaves like a Backend should. b must start empty.
func Conformance(t *testing.T, b storage.Backend) {
	t.Helper()
	ctx := context.Background()

	if _, err := b.Get(ctx, "missing/key"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Get missing: want ErrNotFound, got %v", err)
	}

	data := []byte("encrypted bytes \x00\x01\x02")
	for _, k := range []string{"chunks/ab/ab01", "chunks/ab/ab02", "chunks/cd/cd01", "snapshots/one"} {
		if err := b.Put(ctx, k, data); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}
	if err := b.Put(ctx, "chunks/ab/ab01", data); err != nil {
		t.Fatalf("repeated Put: %v", err)
	}

	got, err := b.Get(ctx, "chunks/ab/ab01")
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("Get: %q, %v", got, err)
	}

	keys, err := b.List(ctx, "chunks/")
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(keys)
	want := []string{"chunks/ab/ab01", "chunks/ab/ab02", "chunks/cd/cd01"}
	if !slices.Equal(keys, want) {
		t.Fatalf("List chunks/ = %v, want %v", keys, want)
	}

	if err := b.Delete(ctx, "chunks/ab/ab02"); err != nil {
		t.Fatal(err)
	}
	if err := b.Delete(ctx, "chunks/ab/ab02"); err != nil {
		t.Fatalf("Delete missing: %v", err)
	}
	if _, err := b.Get(ctx, "chunks/ab/ab02"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Get deleted: %v", err)
	}
	keys, _ = b.List(ctx, "")
	if len(keys) != 3 {
		t.Fatalf("List all after delete = %v", keys)
	}
}
