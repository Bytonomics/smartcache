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

// Outcome describes how Cache[T].Get or Cache[T].Put served a request, so the
// caller can meter hit rate and alarm on populate failures without the
// library logging anything. Hit, Loaded, LoadedNotCached, and NegativeHit are
// Get-only (they name a load that happened on a read). Written and
// WrittenNotCached are Put-only (they name a write that happened, not a
// load) — Put never returns a Get-only value and Get never returns a
// Put-only value.
type Outcome int

const (
	// Hit means Get served the value from cache.
	Hit Outcome = iota
	// Loaded means Get missed, the loader ran, and the value was cached.
	Loaded
	// LoadedNotCached means Get missed, the loader ran, but writing the value
	// back to the store failed. The value is still returned; the read never fails.
	LoadedNotCached
	// NegativeHit means Get served a cached "not found" marker.
	NegativeHit
	// Written means Put's writer succeeded and the value was cached.
	Written
	// WrittenNotCached means Put's writer succeeded, but writing the value to
	// the store failed. The value is still returned; the write never fails on
	// account of the cache.
	WrittenNotCached
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
	case Written:
		return "Written"
	case WrittenNotCached:
		return "WrittenNotCached"
	default:
		return fmt.Sprintf("Outcome(%d)", int(o))
	}
}

// Loader loads a value from the source of truth on a cache miss.
//
// Contract: return (val, nil) on success, (nil, ErrNotFound) for a cacheable
// not-found, or (nil, err) for a transient error (which is never cached).
type Loader[T any] func(ctx context.Context) (*T, error)

// Writer persists a value to the source of truth and returns exactly what
// was written, so Put can cache that same value.
//
// Contract: return (val, nil) with val != nil on success. Any non-nil error
// is returned to the caller unchanged and nothing is cached. Returning
// (nil, nil) violates the contract — Put returns ErrNilWrite, because the
// cache is never set to a nil value.
type Writer[T any] func(ctx context.Context) (*T, error)

// Options is the resolved configuration for a Cache[T], produced by Register.
type Options struct {
	// Prefix namespaces keys: the stored key is bc:<Prefix>:<key> for a normal
	// cache and bc:{<Prefix>}:<key> for an alias-group cache (built in keyspace.go).
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

// Cache is a generic, type-safe read-through / delete-on-write cache over a
// CacheStore. It is constructed only via Register on a Manager, never directly.
type Cache[T any] struct {
	store          CacheStore
	batch          BatchCacheStore // non-nil only when store implements BatchCacheStore
	opts           Options
	codec          Codec
	group          *singleflight.Group
	jitterFraction float64
	metrics        *cacheMetrics   // nil => telemetry disabled
	aliasStore     AliasCacheStore // non-nil only for alias-group caches (RegisterAliasGroup)
	isAliasGroup   bool            // true for caches created via RegisterAliasGroup
}

// fullKey builds the value key for this cache via the central keyspace builder (keyspace.go).
// Alias-group caches hash-tag the namespace ({ns}); non-alias caches use plain bc:<ns>:<key>.
func (c *Cache[T]) fullKey(key string) string {
	return valueKey(c.opts.Prefix, key, c.isAliasGroup)
}

// positiveTTL returns the jittered positive TTL a value is stored under. When
// AllowInfinite is set the base TTL (which may be <= 0, meaning no expiry) is
// passed through unchanged; otherwise a downward jitter is applied.
func (c *Cache[T]) positiveTTL() time.Duration {
	if c.opts.AllowInfinite {
		return c.opts.TTL
	}
	return applyJitter(c.opts.TTL, c.jitterFraction)
}

// negativeTTL returns the jittered TTL for a negative-cache marker. It is only
// called when NegativeTTL > 0.
func (c *Cache[T]) negativeTTL() time.Duration {
	return applyJitter(c.opts.NegativeTTL, c.jitterFraction)
}

// Get reads through to loader on a cache miss. See Outcome for the result
// modes.
//
// Sharing note: when singleflight is enabled (the default), a Loaded or
// LoadedNotCached result may be the exact same *T handed to every concurrent
// caller deduped onto the same loader call — that is singleflight.Do's own
// contract. Treat a Loaded/LoadedNotCached result as read-only; copy it
// before mutating. A Hit result is always freshly unmarshaled per call and
// is never shared with another caller.
func (c *Cache[T]) Get(ctx context.Context, key string, loader Loader[T]) (*T, Outcome, error) {
	k := c.fullKey(key)

	raw, getErr := c.store.Get(ctx, k)
	if getErr == nil {
		if bytes.Equal(raw, negativeMarker) {
			c.metrics.recordOutcome(ctx, NegativeHit)
			return nil, NegativeHit, ErrNotFound
		}
		var v T
		if uErr := c.codec.Unmarshal(raw, &v); uErr == nil {
			c.metrics.recordOutcome(ctx, Hit)
			return &v, Hit, nil
		}
		// Corrupt/unreadable cached entry: fall through and reload as if it were a miss.
	}
	// Any store.Get error (including ErrStoreMiss) or an unmarshal failure is treated
	// as a miss — a backend read error must not fail the request more than the DB does.

	// doLoad runs the loader and populates the cache in one step. Running the
	// populate step here (inside the deduped call) instead of after group.Do
	// returns means only the one goroutine that actually calls the loader also
	// does the Marshal+Set — concurrent waiters share this one result instead
	// of each redundantly re-populating the cache.
	type loadResult struct {
		val       *T
		populated bool
	}
	// doLoad records only load-event metrics (latency for every loader run,
	// load_error on a transient failure). The request-outcome counters are
	// recorded once per caller AFTER group.Do returns, so that under
	// singleflight every deduped waiter counts the request it was served while
	// these load-event metrics still count the single actual load.
	doLoad := func() (any, error) {
		start := time.Now()
		val, lerr := loader(ctx)
		c.metrics.recordLoadLatencySeconds(ctx, time.Since(start).Seconds())
		switch {
		case errors.Is(lerr, ErrNotFound):
			if c.opts.NegativeTTL > 0 {
				if sErr := c.setValue(ctx, key, negativeMarker, c.negativeTTL()); sErr != nil {
					return nil, ErrNotFound
				}
			}
			return nil, ErrNotFound
		case lerr != nil:
			c.metrics.recordLoadError(ctx)
			return nil, lerr
		case val == nil:
			// Loader violated its contract (nil, nil) — treat as not-found, do not cache.
			return nil, ErrNotFound
		}

		b, mErr := c.codec.Marshal(val)
		if mErr != nil {
			return &loadResult{val: val}, nil
		}
		if sErr := c.setValue(ctx, key, b, c.positiveTTL()); sErr != nil {
			return &loadResult{val: val}, nil
		}
		return &loadResult{val: val, populated: true}, nil
	}

	var res any
	var lerr error
	if c.group != nil {
		res, lerr, _ = c.group.Do(k, doLoad) //nolint:not-an-error -- discards singleflight's "shared" bool, which this code has no use for
	} else {
		res, lerr = doLoad()
	}

	if errors.Is(lerr, ErrNotFound) {
		// A fresh not-found folds into Loaded (the warm counterpart is
		// NegativeHit, recorded on the fast path). Per caller: N deduped waiters
		// each count the request they were served.
		c.metrics.recordOutcome(ctx, Loaded)
		return nil, Loaded, ErrNotFound
	}
	if lerr != nil {
		// Transient load error: already metered as load_error inside doLoad;
		// never counted as a served outcome.
		return nil, Loaded, lerr
	}
	out, _ := res.(*loadResult) //nolint:not-an-error -- doLoad always returns *loadResult on nil error
	if out.populated {
		c.metrics.recordOutcome(ctx, Loaded)
		return out.val, Loaded, nil
	}
	c.metrics.recordOutcome(ctx, LoadedNotCached)
	return out.val, LoadedNotCached, nil
}

// Put performs a write-through: it calls writer to persist the value to your
// source of truth, then caches exactly the value writer returned. See Outcome
// for the result modes.
//
// If writer fails, its error is returned unchanged and the cache is
// untouched. If writer succeeds but returns a nil value, Put returns
// ErrNilWrite and the cache is untouched. If the cache-side write fails after
// writer succeeded, Put still returns the value with Outcome ==
// WrittenNotCached — the real write already happened; only caching it
// failed, and that must never look like a failed write to the caller.
//
// writer is never deduplicated the way Get's loader is: two concurrent Put
// calls for the same key are two distinct writes, and singleflight would
// silently drop one of them.
func (c *Cache[T]) Put(ctx context.Context, key string, writer Writer[T]) (*T, Outcome, error) {
	val, err := writer(ctx)
	if err != nil {
		return nil, Written, err
	}
	if val == nil {
		return nil, Written, ErrNilWrite
	}

	b, mErr := c.codec.Marshal(val)
	if mErr != nil {
		c.metrics.recordOutcome(ctx, WrittenNotCached)
		return val, WrittenNotCached, nil
	}
	if sErr := c.setValue(ctx, key, b, c.positiveTTL()); sErr != nil {
		c.metrics.recordOutcome(ctx, WrittenNotCached)
		return val, WrittenNotCached, nil
	}
	c.metrics.recordOutcome(ctx, Written)
	return val, Written, nil
}

// PutValue writes a value you already hold directly into the cache — no
// external write happens. Use this when you performed the real write
// yourself and only need the cache updated to match it.
func (c *Cache[T]) PutValue(ctx context.Context, key string, val *T) error {
	b, err := c.codec.Marshal(val)
	if err != nil {
		return err
	}
	return c.setValue(ctx, key, b, c.positiveTTL())
}

// Evict deletes key (delete-on-write). It returns the delete error so the caller
// can retry or alarm; the TTL backstop bounds staleness if it fails.
func (c *Cache[T]) Evict(ctx context.Context, key string) error {
	c.metrics.recordEvict(ctx, 1)
	return c.deleteValue(ctx, key)
}

// EvictMany deletes several keys, joining any errors.
func (c *Cache[T]) EvictMany(ctx context.Context, keys ...string) error {
	c.metrics.recordEvict(ctx, int64(len(keys)))
	var errs []error
	for _, key := range keys {
		if err := c.deleteValue(ctx, key); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// GetMany reads several keys in one batch: cache hits (and warm negative
// hits) are served without touching loadMissing; keys not found in the cache
// are collected and loaded in ONE loadMissing call, then populated back into
// the cache (with per-key downward jitter, on both the positive TTL and
// NegativeTTL). Keys loadMissing does not return are negative-cached (when
// NegativeTTL > 0) and omitted from the result map. Unlike Get, GetMany is
// never deduplicated via singleflight.
func (c *Cache[T]) GetMany(
	ctx context.Context,
	keys []string,
	loadMissing func(ctx context.Context, missing []string) (map[string]*T, error),
) (map[string]*T, error) {
	if len(keys) == 0 {
		return map[string]*T{}, nil
	}

	fullToOrig, fullKeys := c.dedupKeys(keys)
	raw := c.readManyRaw(ctx, fullKeys)
	result, missing := c.splitHitsAndMisses(ctx, fullKeys, fullToOrig, raw)

	if len(missing) == 0 {
		return result, nil
	}

	start := time.Now()
	loaded, lErr := loadMissing(ctx, missing)
	c.metrics.recordLoadLatencySeconds(ctx, time.Since(start).Seconds())
	if lErr != nil {
		c.metrics.recordLoadError(ctx)
		return nil, fmt.Errorf("smartcache: GetMany load: %w", lErr)
	}

	c.populateLoaded(ctx, missing, loaded, result)
	return result, nil
}

// dedupKeys maps each unique key to its full (prefixed) form, returning both
// the lookup map and the ordered list of unique full keys.
func (c *Cache[T]) dedupKeys(keys []string) (fullToOrig map[string]string, fullKeys []string) {
	fullToOrig = make(map[string]string, len(keys))
	fullKeys = make([]string, 0, len(keys))
	for _, key := range keys {
		fk := c.fullKey(key)
		if _, seen := fullToOrig[fk]; seen {
			continue
		}
		fullToOrig[fk] = key
		fullKeys = append(fullKeys, fk)
	}
	return fullToOrig, fullKeys
}

// readManyRaw reads fullKeys via the batch store when available, otherwise
// falling back to one Get per key. A batch-read error is treated as a total
// miss (never fails the request), matching Get's read-error-is-a-miss policy.
func (c *Cache[T]) readManyRaw(ctx context.Context, fullKeys []string) map[string][]byte {
	if c.batch != nil {
		raw, bErr := c.batch.GetMany(ctx, fullKeys)
		if bErr != nil {
			return map[string][]byte{}
		}
		return raw
	}
	raw := make(map[string][]byte, len(fullKeys))
	for _, fk := range fullKeys {
		b, gErr := c.store.Get(ctx, fk)
		if gErr == nil {
			raw[fk] = b
		}
	}
	return raw
}

// splitHitsAndMisses classifies each full key's raw bytes into a cache hit
// (added to result), a negative hit (metered, omitted from result), or a miss
// (absent or corrupt — appended to missing for loadMissing to resolve).
func (c *Cache[T]) splitHitsAndMisses(
	ctx context.Context,
	fullKeys []string,
	fullToOrig map[string]string,
	raw map[string][]byte,
) (result map[string]*T, missing []string) {
	result = make(map[string]*T, len(fullKeys))
	for _, fk := range fullKeys {
		origKey := fullToOrig[fk]
		b, ok := raw[fk]
		if !ok {
			missing = append(missing, origKey)
			continue
		}
		if bytes.Equal(b, negativeMarker) {
			c.metrics.recordOutcome(ctx, NegativeHit)
			continue
		}
		var v T
		if uErr := c.codec.Unmarshal(b, &v); uErr != nil {
			// Corrupt/unreadable cached entry: treat as a miss and reload.
			missing = append(missing, origKey)
			continue
		}
		result[origKey] = &v
		c.metrics.recordOutcome(ctx, Hit)
	}
	return result, missing
}

// populateLoaded merges loadMissing's result into result and populates the
// cache: a key found in loaded is cached under positiveTTL; a key absent (or
// nil) is negative-cached under NegativeTTL when enabled, and is never added
// to result either way.
func (c *Cache[T]) populateLoaded(ctx context.Context, missing []string, loaded, result map[string]*T) {
	for _, origKey := range missing {
		v, ok := loaded[origKey]
		if ok && v != nil {
			result[origKey] = v
			b, mErr := c.codec.Marshal(v)
			if mErr != nil {
				c.metrics.recordOutcome(ctx, LoadedNotCached)
				continue
			}
			if sErr := c.setValue(ctx, origKey, b, c.positiveTTL()); sErr != nil {
				c.metrics.recordOutcome(ctx, LoadedNotCached)
				continue
			}
			c.metrics.recordOutcome(ctx, Loaded)
			continue
		}
		// Not found in the source of truth: negative-cache when enabled, but
		// never add it to the result map either way.
		if c.opts.NegativeTTL > 0 {
			if sErr := c.setValue(ctx, origKey, negativeMarker, c.negativeTTL()); sErr != nil {
				c.metrics.recordOutcome(ctx, LoadedNotCached)
				continue
			}
		}
		c.metrics.recordOutcome(ctx, Loaded)
	}
}
