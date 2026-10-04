// Package s3 is a storage backend for S3-compatible object stores that support
// atomic conditional object creation.
package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	loc    string
}

// New connects to an S3-compatible endpoint. It doesn't make any requests.
func New(c Config) (*Backend, error) {
	endpoint, secure := c.Endpoint, !c.Insecure
	if u, err := url.Parse(c.Endpoint); err == nil && u.Host != "" {
		if (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("s3: endpoint must be an http or https host URL without credentials, a path, query or fragment")
		}
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
		loc:    "s3://" + strings.ToLower(endpoint) + "/" + c.Bucket + "/" + prefix,
	}, nil
}

// Location includes the endpoint, which String leaves out.
func (b *Backend) Location() string { return b.loc }

func (b *Backend) Put(ctx context.Context, key string, data []byte) error {
	_, err := b.client.PutObject(ctx, b.bucket, b.prefix+key, bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: "application/octet-stream"})
	if err != nil {
		return fmt.Errorf("s3 put %s: %w", key, err)
	}
	return nil
}

func (b *Backend) PutNew(ctx context.Context, key string, data []byte) error {
	opts := minio.PutObjectOptions{ContentType: "application/octet-stream", DisableMultipart: true}
	opts.SetMatchETagExcept("*")
	_, err := b.client.PutObject(ctx, b.bucket, b.prefix+key, bytes.NewReader(data), int64(len(data)), opts)
	if minio.ToErrorResponse(err).StatusCode == 412 {
		return storage.ErrExists
	}
	if minio.ToErrorResponse(err).StatusCode == 501 {
		return fmt.Errorf("%w: %w", storage.ErrConditionalUnsupported, err)
	}
	if err != nil {
		return fmt.Errorf("s3 put new %s: %w", key, err)
	}
	return nil
}

func (b *Backend) Get(ctx context.Context, key string) ([]byte, error) {
	obj, info, _, err := (minio.Core{Client: b.client}).GetObject(ctx, b.bucket, b.prefix+key, minio.GetObjectOptions{})
	if err != nil {
		if minio.ToErrorResponse(err).Code == minio.NoSuchKey {
			return nil, storage.ErrNotFound
		}
		return nil, fmt.Errorf("s3 get %s: %w", key, err)
	}
	defer obj.Close()
	limit := int64(storage.MaxObjectSize)
	if strings.HasPrefix(key, "chunks/") {
		limit = (8 << 20) + 64
	}
	data, err := storage.ReadBoundedSize(obj, info.Size, limit)
	if err != nil {
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
