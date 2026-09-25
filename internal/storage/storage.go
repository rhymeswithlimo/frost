// Package storage defines the contract every frost backend implements.
//
// Backends are dumb object stores. They only ever see encrypted blobs under
// opaque keys like "chunks/ab/ab12..." or "snapshots/maple-otter-3f1c", so
// nothing here needs to know about encryption.
package storage

import (
	"context"
	"errors"
)

// ErrNotFound is returned by Get when the key doesn't exist.
var ErrNotFound = errors.New("object not found")

// Backend is a flat key/value object store.
//
// Keys use forward slashes and never start with one. Put must be safe to
// repeat with the same key and data. Implementations must be safe for
// concurrent use.
type Backend interface {
	// Put stores data under key, replacing anything already there.
	Put(ctx context.Context, key string, data []byte) error
	// Get returns the data stored under key, or ErrNotFound.
	Get(ctx context.Context, key string) ([]byte, error)
	// List returns every key that starts with prefix, in no particular order.
	List(ctx context.Context, prefix string) ([]string, error)
	// Delete removes key. Deleting a missing key isn't an error.
	Delete(ctx context.Context, key string) error
	// String describes the backend for humans, e.g. "s3://bucket/prefix".
	String() string
}
