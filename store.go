package smartcache

import (
	"context"
	"time"
)

// CacheStore is the backend abstraction Cache[T] depends on: a byte key-value
// cache store — never the application's own database. Cache[T] owns all
// (de)serialization, so CacheStore never sees the cached type T. Swapping the
// backend (Redis, in-memory, anything) means providing a different CacheStore
// implementation; the Cache[T] API is unchanged.
type CacheStore interface {
	// Get returns the raw bytes for key, or ErrStoreMiss if the key is absent.
	Get(ctx context.Context, key string) ([]byte, error)
	// Set stores val under key with the given ttl. A ttl <= 0 means no expiry.
	Set(ctx context.Context, key string, val []byte, ttl time.Duration) error
	// Delete removes key. Deleting an absent key is not an error.
	Delete(ctx context.Context, key string) error
	// Exists reports whether key is present (and not expired).
	Exists(ctx context.Context, key string) (bool, error)
}

// BatchCacheStore is an optional extension of CacheStore for backends that can
// read many keys in one round trip. Cache[T].GetMany uses it when the injected
// store implements it (redisstore does, via MGET); stores that do not are
// handled transparently by GetMany's per-key fallback.
type BatchCacheStore interface {
	CacheStore
	// GetMany returns the raw bytes for the keys that are present. Keys that are
	// absent (or expired) are omitted from the returned map — a miss is never an
	// error here.
	GetMany(ctx context.Context, keys []string) (map[string][]byte, error)
}

// Codec serializes cached values to and from bytes. The default (when
// Options.Codec is nil) is encoding/json.
type Codec interface {
	Marshal(v any) ([]byte, error)
	Unmarshal(data []byte, v any) error
}
