// Package storage defines the contract every frost backend implements.
//
// Backends are dumb object stores. They only ever see encrypted blobs under
// opaque keys like "chunks/ab/ab12..." or "snapshots/maple-otter-3f1c", so
// nothing here needs to know about encryption.
package storage

import (
	"context"
	"errors"
	"io"
)

// ErrNotFound is returned by Get when the key doesn't exist.
var ErrNotFound = errors.New("object not found")

// ErrExists means a conditional create found an existing object.
var ErrExists = errors.New("object already exists")

// MaxObjectSize bounds ciphertext downloads, including large snapshot trees.
const MaxObjectSize = (256 << 20) + 64

// ReadBounded rejects oversized responses instead of exhausting memory.
func ReadBounded(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("storage response exceeds size limit")
	}
	return data, nil
}

// Backend is a flat key/value object store.
//
// Keys use forward slashes and never start with one. Put must be safe to
// repeat with the same key and data. Implementations must be safe for
// concurrent use.
type Backend interface {
	// Put stores data under key, replacing anything already there.
	Put(ctx context.Context, key string, data []byte) error
	// PutNew atomically creates an object, returning ErrExists without
	// modifying it if the key already exists.
	PutNew(ctx context.Context, key string, data []byte) error
	// Get returns the data stored under key, or ErrNotFound.
	Get(ctx context.Context, key string) ([]byte, error)
	// List returns every key that starts with prefix, in no particular order.
	List(ctx context.Context, prefix string) ([]string, error)
	// Delete removes key. Deleting a missing key isn't an error.
	Delete(ctx context.Context, key string) error
	// String describes the backend for humans, e.g. "s3://bucket/prefix".
	String() string
}
