package smartcache

import (
	"context"
	"time"
)

// Store is the backend abstraction Cache[T] depends on: a byte key-value store.
// Cache[T] owns all (de)serialization, so the Store never sees the cached type T.
// Swapping the backend (Redis, in-memory, anything) means providing a different
// Store implementation; the cache API is unchanged.
type Store interface {
	// Get returns the raw bytes for key, or ErrStoreMiss if the key is absent.
	Get(ctx context.Context, key string) ([]byte, error)
	// Set stores val under key with the given ttl. A ttl <= 0 means no expiry.
	Set(ctx context.Context, key string, val []byte, ttl time.Duration) error
	// Delete removes key. Deleting an absent key is not an error.
	Delete(ctx context.Context, key string) error
	// Exists reports whether key is present (and not expired).
	Exists(ctx context.Context, key string) (bool, error)
}

// Codec serializes cached values to and from bytes. The default (when
// Options.Codec is nil) is encoding/json.
type Codec interface {
	Marshal(v any) ([]byte, error)
	Unmarshal(data []byte, v any) error
}
