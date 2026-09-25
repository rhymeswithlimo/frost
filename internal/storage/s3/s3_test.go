package s3

import (
	"net/http/httptest"
	"os"
	"testing"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

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
