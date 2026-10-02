package s3

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
	"github.com/minio/minio-go/v7"

	"github.com/rhymeswithlimo/frost/internal/storage"
	"github.com/rhymeswithlimo/frost/internal/storage/storagetest"
)

func TestConformanceFake(t *testing.T) {
	mem := s3mem.New()
	if err := mem.CreateBucket("frost"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(gofakes3.New(mem).Server())
	defer srv.Close()

	b, err := New(Config{Endpoint: srv.URL, Bucket: "frost", Prefix: "backups/", AccessKeyID: "x", SecretAccessKey: "y", Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	storagetest.Conformance(t, b)
}

func TestPutNewErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
		want   error
	}{
		{412, "PreconditionFailed", storage.ErrExists},
		{501, "NotImplemented", storage.ErrConditionalUnsupported},
		{403, "AccessDenied", nil},
	} {
		t.Run(tc.code, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPut || r.Header.Get("If-None-Match") != "*" || r.URL.Path != "/frost/backups/frost.repo" {
					t.Errorf("unexpected request: %s %s, If-None-Match = %q", r.Method, r.URL, r.Header.Get("If-None-Match"))
				}
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(tc.status)
				fmt.Fprintf(w, "<Error><Code>%s</Code><Message>test failure</Message></Error>", tc.code)
			}))
			defer srv.Close()
			b, err := New(Config{Endpoint: srv.URL, Region: "us-east-1", Bucket: "frost", Prefix: "backups", AccessKeyID: "x", SecretAccessKey: "y"})
			if err != nil {
				t.Fatal(err)
			}
			err = b.PutNew(context.Background(), "frost.repo", []byte("test"))
			if tc.want != nil {
				if !errors.Is(err, tc.want) {
					t.Fatalf("PutNew = %v, want %v", err, tc.want)
				}
			} else {
				var api minio.ErrorResponse
				if !errors.As(err, &api) || api.Code != tc.code || errors.Is(err, storage.ErrConditionalUnsupported) {
					t.Fatalf("permission error lost: %v", err)
				}
			}
		})
	}
}

// TestConformanceReal runs against a real endpoint when FROST_TEST_S3_ENDPOINT
// is set, e.g. a local MinIO. The bucket must exist and be empty.
func TestConformanceReal(t *testing.T) {
	ep := os.Getenv("FROST_TEST_S3_ENDPOINT")
	if ep == "" {
		t.Skip("FROST_TEST_S3_ENDPOINT not set")
	}
	b, err := New(Config{
		Endpoint:        ep,
		Bucket:          os.Getenv("FROST_TEST_S3_BUCKET"),
		Region:          os.Getenv("FROST_TEST_S3_REGION"),
		AccessKeyID:     os.Getenv("FROST_TEST_S3_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("FROST_TEST_S3_SECRET_ACCESS_KEY"),
		Prefix:          "frost-conformance",
	})
	if err != nil {
		t.Fatal(err)
	}
	storagetest.Conformance(t, b)
}

// The same bucket name at another provider is somewhere else.
func TestLocationIncludesEndpoint(t *testing.T) {
	a, _ := New(Config{Endpoint: "s3.us-west-004.backblazeb2.com", Bucket: "b", Prefix: "frost"})
	b, _ := New(Config{Endpoint: "https://abc.r2.cloudflarestorage.com", Bucket: "b", Prefix: "frost"})
	c, _ := New(Config{Endpoint: "s3.us-west-004.backblazeb2.com", Bucket: "b", Prefix: "other"})
	if a.String() != b.String() {
		t.Fatalf("names differ: %s, %s", a, b)
	}
	if a.Location() == b.Location() || a.Location() == c.Location() {
		t.Fatalf("locations collide: %s, %s, %s", a.Location(), b.Location(), c.Location())
	}
}
