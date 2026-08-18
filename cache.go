package smartcache

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sync/singleflight"
)

// negativeMarker is stored in place of a real value to negative-cache "not found".
// JSON-encoded values never begin with a NUL byte, so this cannot collide with a
// real marshaled value.
var negativeMarker = []byte("\x00smartcache\x00negative\x00")

// jsonCodec is the default codec using encoding/json.
type jsonCodec struct{}

func (jsonCodec) Marshal(v any) ([]byte, error)   { return json.Marshal(v) }
func (jsonCodec) Unmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// Outcome describes how Cache[T].Get served a request, so the caller can meter
// hit rate and alarm on populate failures without the library logging anything.
type Outcome int

const (
	// Hit means the value was served from cache.
	Hit Outcome = iota
	// Loaded means a miss occurred, the loader ran, and the value was cached.
	Loaded
	// LoadedNotCached means a miss occurred, the loader ran, but writing the value
	// back to the store failed. The value is still returned; the read never fails.
	LoadedNotCached
	// NegativeHit means a cached "not found" marker was served.
	NegativeHit
)

// String returns a human-readable name for the outcome.
func (o Outcome) String() string {
	switch o {
	case Hit:
		return "Hit"
	case Loaded:
		return "Loaded"
	case LoadedNotCached:
		return "LoadedNotCached"
	case NegativeHit:
		return "NegativeHit"
	default:
		return fmt.Sprintf("Outcome(%d)", int(o))
	}
}

// Loader loads a value from the source of truth on a cache miss.
//
// Contract: return (val, nil) on success, (nil, ErrNotFound) for a cacheable
// not-found, or (nil, err) for a transient error (which is never cached).
type Loader[T any] func(ctx context.Context) (*T, error)

// Options configures a Cache[T].
type Options struct {
	// Prefix namespaces keys: the stored key is Prefix + ":" + key (or just key
	// when Prefix is empty).
	Prefix string
	// TTL is the positive backstop expiry applied to cached values. Required unless
	// AllowInfinite is true.
	TTL time.Duration
	// AllowInfinite opts in to TTL <= 0 (entries never expire).
	AllowInfinite bool
	// NegativeTTL, when > 0, enables negative caching of ErrNotFound for that
	// duration. Zero disables negative caching.
	NegativeTTL time.Duration
	// Codec overrides serialization. Nil means JSON.
	Codec Codec
	// DisableSingleflight turns off de-duplication of concurrent cold loads.
	DisableSingleflight bool
}

// Cache is a generic, type-safe read-through / delete-on-write cache over a CacheStore.
type Cache[T any] struct {
	store CacheStore
	opts  Options
	codec Codec
	group *singleflight.Group
}

// New builds a Cache[T]. It returns ErrInvalidTTL if opts.TTL <= 0 and
// opts.AllowInfinite is false.
func New[T any](store CacheStore, opts Options) (*Cache[T], error) {
	if opts.TTL <= 0 && !opts.AllowInfinite {
		return nil, ErrInvalidTTL
	}
	codec := opts.Codec
	if codec == nil {
		codec = jsonCodec{}
	}
	var group *singleflight.Group
	if !opts.DisableSingleflight {
		group = &singleflight.Group{}
	}
	return &Cache[T]{store: store, opts: opts, codec: codec, group: group}, nil
}

// fullKey prefixes the cache key with the configured prefix.
func (c *Cache[T]) fullKey(key string) string {
	if c.opts.Prefix == "" {
		return key
	}
	return c.opts.Prefix + ":" + key
}

// Get reads through to loader on a cache miss. See Outcome for the result modes.
func (c *Cache[T]) Get(ctx context.Context, key string, loader Loader[T]) (*T, Outcome, error) {
	k := c.fullKey(key)

	raw, getErr := c.store.Get(ctx, k)
	if getErr == nil {
		if bytes.Equal(raw, negativeMarker) {
			return nil, NegativeHit, ErrNotFound
		}
		var v T
		if uErr := c.codec.Unmarshal(raw, &v); uErr == nil {
			return &v, Hit, nil
		}
		// Corrupt/unreadable cached entry: fall through and reload as if it were a miss.
	}
	// Any store.Get error (including ErrStoreMiss) or an unmarshal failure is treated
	// as a miss — a backend read error must not fail the request more than the DB does.

	var val *T
	var lerr error
	if c.group != nil {
		res, sfErr, _ := c.group.Do(k, func() (any, error) { //nolint:not-an-error
			return loader(ctx)
		})
		lerr = sfErr
		if res != nil {
			val, _ = res.(*T) //nolint:not-an-error
		}
	} else {
		val, lerr = loader(ctx)
	}

	switch {
	case errors.Is(lerr, ErrNotFound):
		if c.opts.NegativeTTL > 0 {
			_ = c.store.Set(ctx, k, negativeMarker, c.opts.NegativeTTL)
		}
		return nil, Loaded, ErrNotFound
	case lerr != nil:
		return nil, Loaded, lerr
	case val == nil:
		// Loader violated its contract (nil, nil) — treat as not-found, do not cache.
		return nil, Loaded, ErrNotFound
	}

	b, mErr := c.codec.Marshal(val)
	if mErr != nil {
		return val, LoadedNotCached, nil
	}
	if sErr := c.store.Set(ctx, k, b, c.opts.TTL); sErr != nil {
		return val, LoadedNotCached, nil
	}
	return val, Loaded, nil
}

// Put write-through stores a known value under key.
func (c *Cache[T]) Put(ctx context.Context, key string, val *T) error {
	b, err := c.codec.Marshal(val)
	if err != nil {
		return err
	}
	return c.store.Set(ctx, c.fullKey(key), b, c.opts.TTL)
}

// Evict deletes key (delete-on-write). It returns the delete error so the caller
// can retry or alarm; the TTL backstop bounds staleness if it fails.
func (c *Cache[T]) Evict(ctx context.Context, key string) error {
	return c.store.Delete(ctx, c.fullKey(key))
}

// EvictMany deletes several keys, joining any errors.
func (c *Cache[T]) EvictMany(ctx context.Context, keys ...string) error {
	var errs []error
	for _, key := range keys {
		if err := c.store.Delete(ctx, c.fullKey(key)); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
