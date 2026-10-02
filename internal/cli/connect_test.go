package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/minio/minio-go/v7"

	"github.com/rhymeswithlimo/frost/internal/storage"
	"github.com/rhymeswithlimo/frost/internal/storage/storagetest"
	"github.com/rhymeswithlimo/frost/internal/tui"
)

type probeBackend struct {
	storage.Backend
	putNew func(context.Context, string, []byte) error
	list   func(context.Context, string) ([]string, error)
	delete func(context.Context, string) error
}

func (b probeBackend) PutNew(ctx context.Context, key string, data []byte) error {
	if b.putNew != nil {
		return b.putNew(ctx, key, data)
	}
	return b.Backend.PutNew(ctx, key, data)
}

func (b probeBackend) List(ctx context.Context, key string) ([]string, error) {
	if b.list != nil {
		return b.list(ctx, key)
	}
	return b.Backend.List(ctx, key)
}

func (b probeBackend) Delete(ctx context.Context, key string) error {
	if b.delete != nil {
		return b.delete(ctx, key)
	}
	return b.Backend.Delete(ctx, key)
}

func TestProbeConditionalWrites(t *testing.T) {
	denied := minio.ErrorResponse{Code: "AccessDenied", StatusCode: 403}
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"enforced", storage.ErrExists},
		{"ignored", nil},
		{"unsupported", storage.ErrConditionalUnsupported},
		{"permission", denied},
		{"timeout", context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mem := storagetest.NewMem()
			calls := 0
			b := probeBackend{Backend: mem, putNew: func(ctx context.Context, key string, data []byte) error {
				calls++
				if calls == 1 {
					return mem.PutNew(ctx, key, data)
				}
				if tc.err == nil {
					return mem.Put(ctx, key, data)
				}
				return tc.err
			}}
			err := probe(context.Background(), b)
			want := tc.err
			if tc.err == nil {
				want = storage.ErrConditionalUnsupported
			} else if errors.Is(tc.err, storage.ErrExists) {
				want = nil
			}
			if !errors.Is(err, want) {
				t.Fatalf("probe = %v, want %v", err, want)
			}
			if tc.name == "permission" {
				var ce *tui.ConnectError
				if !errors.As(explainConnect(err), &ce) || ce.About != "key" {
					t.Fatalf("permission failure lost: %v", explainConnect(err))
				}
			}
			keys, _ := mem.List(context.Background(), "")
			if len(keys) != 0 {
				t.Fatalf("probe left objects: %v", keys)
			}
		})
	}
}

func TestProbeChecksListingAndCleanup(t *testing.T) {
	denied := errors.New("permission denied")
	for _, step := range []string{"list", "delete", "missing from list", "changed after rejection"} {
		t.Run(step, func(t *testing.T) {
			mem := storagetest.NewMem()
			b := probeBackend{Backend: mem}
			switch step {
			case "list":
				b.list = func(context.Context, string) ([]string, error) { return nil, denied }
			case "delete":
				b.delete = func(context.Context, string) error { return denied }
			case "missing from list":
				b.list = func(context.Context, string) ([]string, error) { return nil, nil }
			case "changed after rejection":
				b.putNew = func(ctx context.Context, key string, data []byte) error {
					err := mem.PutNew(ctx, key, data)
					if errors.Is(err, storage.ErrExists) {
						mem.Put(ctx, key, data)
					}
					return err
				}
			}
			err := probe(context.Background(), b)
			if err == nil {
				t.Fatal("invalid storage passed probe")
			}
			if (step == "list" || step == "delete") && !errors.Is(err, denied) {
				t.Fatalf("permission error lost: %v", err)
			}
		})
	}
}

func TestProbeCleanupSurvivesCancellation(t *testing.T) {
	mem := storagetest.NewMem()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deleted := false
	b := probeBackend{Backend: mem}
	b.list = func(context.Context, string) ([]string, error) {
		cancel()
		return nil, context.Canceled
	}
	b.delete = func(cleanup context.Context, key string) error {
		if cleanup.Err() != nil {
			t.Fatalf("cleanup inherited cancellation: %v", cleanup.Err())
		}
		if _, ok := cleanup.Deadline(); !ok {
			t.Fatal("cleanup has no deadline")
		}
		deleted = true
		return mem.Delete(cleanup, key)
	}
	if err := probe(ctx, b); !errors.Is(err, context.Canceled) || !deleted {
		t.Fatalf("probe = %v, deleted = %v", err, deleted)
	}
}

func TestConnectExplainsUnsupportedStorage(t *testing.T) {
	err := explainConnect(storage.ErrConditionalUnsupported)
	if !strings.Contains(err.Error(), "Choose storage that supports them") {
		t.Fatal(err)
	}
}
