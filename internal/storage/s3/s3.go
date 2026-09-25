// Package s3 is a storage backend for any S3-compatible object store: AWS,
// Backblaze B2, Cloudflare R2, Wasabi, MinIO, Garage and so on.
package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/rhymeswithlimo/frost/internal/storage"
)

// Config holds connection settings.
type Config struct {
	Endpoint        string // host[:port], or a full URL
	Region          string
	Bucket          string
	Prefix          string // optional folder inside the bucket
	AccessKeyID     string
	SecretAccessKey string
	Insecure        bool // plain http
}

// Backend stores objects in an S3 bucket.
type Backend struct {
	client *minio.Client
	bucket string
	prefix string
	desc   string
}

// New connects to an S3-compatible endpoint. It doesn't make any requests.
func New(c Config) (*Backend, error) {
	endpoint, secure := c.Endpoint, !c.Insecure
	if u, err := url.Parse(c.Endpoint); err == nil && u.Host != "" {
		endpoint, secure = u.Host, u.Scheme != "http"
	}
	if endpoint == "" || c.Bucket == "" {
		return nil, errors.New("s3: endpoint and bucket are required")
	}
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(c.AccessKeyID, c.SecretAccessKey, ""),
		Secure: secure,
		Region: c.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("s3: %w", err)
	}
	prefix := strings.Trim(c.Prefix, "/")
	if prefix != "" {
		prefix += "/"
	}
	return &Backend{
		client: client,
		bucket: c.Bucket,
		prefix: prefix,
		desc:   "s3://" + c.Bucket + "/" + prefix,
	}, nil
}

func (b *Backend) Put(ctx context.Context, key string, data []byte) error {
	_, err := b.client.PutObject(ctx, b.bucket, b.prefix+key, bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: "application/octet-stream"})
	if err != nil {
		return fmt.Errorf("s3 put %s: %w", key, err)
	}
	return nil
}

func (b *Backend) Get(ctx context.Context, key string) ([]byte, error) {
	obj, err := b.client.GetObject(ctx, b.bucket, b.prefix+key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("s3 get %s: %w", key, err)
	}
	defer obj.Close()
	data, err := io.ReadAll(obj)
	if err != nil {
		if minio.ToErrorResponse(err).Code == minio.NoSuchKey {
			return nil, storage.ErrNotFound
		}
		return nil, fmt.Errorf("s3 get %s: %w", key, err)
	}
	return data, nil
}

func (b *Backend) List(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	for obj := range b.client.ListObjects(ctx, b.bucket, minio.ListObjectsOptions{Prefix: b.prefix + prefix, Recursive: true}) {
		if obj.Err != nil {
			return nil, fmt.Errorf("s3 list %s: %w", prefix, obj.Err)
		}
		keys = append(keys, strings.TrimPrefix(obj.Key, b.prefix))
	}
	return keys, nil
}

func (b *Backend) Delete(ctx context.Context, key string) error {
	if err := b.client.RemoveObject(ctx, b.bucket, b.prefix+key, minio.RemoveObjectOptions{}); err != nil {
		if minio.ToErrorResponse(err).Code == minio.NoSuchKey {
			return nil
		}
		return fmt.Errorf("s3 delete %s: %w", key, err)
	}
	return nil
}

func (b *Backend) String() string { return b.desc }
