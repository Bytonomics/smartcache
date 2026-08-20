package smartcache

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/Bytonomics/smartcache/internal/keyspace"
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
	metrics        *cacheMetrics // nil => telemetry disabled
	aliasOps       AliasOps      // non-nil only for alias-group caches (RegisterAliasGroup)
	isAliasGroup   bool          // true for caches created via RegisterAliasGroup
}

// fullKey builds a NON-alias cache value key: bc:<ns>:<key>. Alias-group caches never call this
// (their keys are owned by the AliasOps strategy handle); they route reads/writes via getValue/
// setValue/deleteValue instead.
func (c *Cache[T]) fullKey(key string) string {
	return keyspace.NonAliasKey(c.opts.Prefix, key)
}

// getValue reads a value for a key. Alias-group caches resolve via the AliasOps handle; normal
// caches do a plain store Get on the full key.
func (c *Cache[T]) getValue(ctx context.Context, key string) ([]byte, error) {
	if c.isAliasGroup {
		return c.aliasOps.GetValue(ctx, key)
	}
	return c.store.Get(ctx, c.fullKey(key))
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

// GetByKey reads through to loader on a cache miss. See Outcome for the result
// modes.
//
// Sharing note: when singleflight is enabled (the default), a Loaded or
// LoadedNotCached result may be the exact same *T handed to every concurrent
// caller deduped onto the same loader call — that is singleflight.Do's own
// contract. Treat a Loaded/LoadedNotCached result as read-only; copy it
// before mutating. A Hit result is always freshly unmarshaled per call and
// is never shared with another caller.
func (c *Cache[T]) GetByKey(ctx context.Context, key string, loader Loader[T]) (*T, Outcome, error) {
	raw, getErr := c.getValue(ctx, key)
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
		res, lerr, _ = c.group.Do(key, doLoad) //nolint:not-an-error -- discards singleflight's shared bool
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

// PutByKey performs a write-through: it calls writer to persist the value to your
// source of truth, then caches exactly the value writer returned. See Outcome
// for the result modes.
//
// If writer fails, its error is returned unchanged and the cache is
// untouched. If writer succeeds but returns a nil value, PutByKey returns
// ErrNilWrite and the cache is untouched. If the cache-side write fails after
// writer succeeded, PutByKey still returns the value with Outcome ==
// WrittenNotCached — the real write already happened; only caching it
// failed, and that must never look like a failed write to the caller.
//
// writer is never deduplicated the way GetByKey's loader is: two concurrent PutByKey
// calls for the same key are two distinct writes, and singleflight would
// silently drop one of them.
func (c *Cache[T]) PutByKey(ctx context.Context, key string, writer Writer[T]) (*T, Outcome, error) {
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

// PutValueByKey writes a value you already hold directly into the cache — no
// external write happens. Use this when you performed the real write
// yourself and only need the cache updated to match it.
func (c *Cache[T]) PutValueByKey(ctx context.Context, key string, val *T) error {
	b, err := c.codec.Marshal(val)
	if err != nil {
		return err
	}
	return c.setValue(ctx, key, b, c.positiveTTL())
}

// EvictByKey deletes key (delete-on-write). It returns the delete error so the caller
// can retry or alarm; the TTL backstop bounds staleness if it fails.
func (c *Cache[T]) EvictByKey(ctx context.Context, key string) error {
	c.metrics.recordEvict(ctx, 1)
	return c.deleteValue(ctx, key)
}

// EvictManyByKey deletes several keys, joining any errors.
func (c *Cache[T]) EvictManyByKey(ctx context.Context, keys ...string) error {
	c.metrics.recordEvict(ctx, int64(len(keys)))
	var errs []error
	for _, key := range keys {
		if err := c.deleteValue(ctx, key); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// GetManyByKey reads several keys in one batch: cache hits (and warm negative
// hits) are served without touching loadMissing; keys not found in the cache
// are collected and loaded in ONE loadMissing call, then populated back into
// the cache (with per-key downward jitter, on both the positive TTL and
// NegativeTTL). Keys loadMissing does not return are negative-cached (when
// NegativeTTL > 0) and omitted from the result map. Unlike GetByKey, GetManyByKey is
// never deduplicated via singleflight.
func (c *Cache[T]) GetManyByKey(
	ctx context.Context,
	keys []string,
	loadMissing func(ctx context.Context, missing []string) (map[string]*T, error),
) (map[string]*T, error) {
	if len(keys) == 0 {
		return map[string]*T{}, nil
	}
	uniq := c.dedupKeys(keys)
	raw := c.readManyRaw(ctx, uniq)
	result, missing := c.splitHitsAndMisses(ctx, uniq, raw)
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

// dedupKeys returns the unique logical keys in order.
func (c *Cache[T]) dedupKeys(keys []string) []string {
	seen := make(map[string]struct{}, len(keys))
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return out
}

// readManyRaw reads each logical key's raw bytes. A non-alias cache with a batch store uses one
// MGET; alias caches (no batch alias read) and non-batch stores fall back to per-key getValue. A
// read error is treated as a miss (never fails the request), matching Get.
func (c *Cache[T]) readManyRaw(ctx context.Context, keys []string) map[string][]byte {
	out := make(map[string][]byte, len(keys))
	if !c.isAliasGroup && c.batch != nil {
		full := make([]string, len(keys))
		fullToLogical := make(map[string]string, len(keys))
		for i, k := range keys {
			fk := c.fullKey(k)
			full[i] = fk
			fullToLogical[fk] = k
		}
		got, bErr := c.batch.GetMany(ctx, full)
		if bErr != nil {
			return out
		}
		for fk, b := range got {
			out[fullToLogical[fk]] = b
		}
		return out
	}
	for _, k := range keys {
		if b, gErr := c.getValue(ctx, k); gErr == nil {
			out[k] = b
		}
	}
	return out
}

// splitHitsAndMisses classifies each logical key: hit (added to result), negative hit (metered,
// omitted), or miss (absent or corrupt — appended to missing).
func (c *Cache[T]) splitHitsAndMisses(ctx context.Context, keys []string, raw map[string][]byte) (result map[string]*T, missing []string) {
	result = make(map[string]*T, len(keys))
	for _, k := range keys {
		b, ok := raw[k]
		if !ok {
			missing = append(missing, k)
			continue
		}
		if bytes.Equal(b, negativeMarker) {
			c.metrics.recordOutcome(ctx, NegativeHit)
			continue
		}
		var v T
		if uErr := c.codec.Unmarshal(b, &v); uErr != nil {
			missing = append(missing, k)
			continue
		}
		result[k] = &v
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

// uniqueKeyOf derives the cache key from a value's CacheUniqueKey(). Returns ErrNilWrite when the
// value is nil, or ErrNotUniqueKeyed when T does not implement UniqueKeyed or CacheUniqueKey()
// is empty (an empty key has no identity and would collide distinct values on one key).
func (c *Cache[T]) uniqueKeyOf(val *T) (string, error) {
	if val == nil {
		return "", ErrNilWrite
	}
	u, ok := any(val).(UniqueKeyed)
	if !ok {
		return "", ErrNotUniqueKeyed
	}
	key := u.CacheUniqueKey()
	if key == "" {
		return "", ErrNotUniqueKeyed
	}
	return key, nil
}

// Put is the value-derived write-through: it runs writer, derives the key from the returned
// value's CacheUniqueKey(), then caches it. If the writer committed but the key is underivable
// (T is not UniqueKeyed, or CacheUniqueKey() is empty), it returns the value with WrittenNotCached
// and ErrNotUniqueKeyed — the committed write is never reported as a failed write to the caller.
func (c *Cache[T]) Put(ctx context.Context, writer Writer[T]) (*T, Outcome, error) {
	val, err := writer(ctx)
	if err != nil {
		return nil, Written, err
	}
	if val == nil {
		return nil, Written, ErrNilWrite
	}
	key, kErr := c.uniqueKeyOf(val)
	if kErr != nil {
		// The real write already committed; an underivable key means we cannot cache the
		// value, but a committed write must never look like a failed write to the caller.
		c.metrics.recordOutcome(ctx, WrittenNotCached)
		return val, WrittenNotCached, kErr
	}
	return c.PutByKey(ctx, key, func(context.Context) (*T, error) { return val, nil })
}

// PutValue is the value-derived direct write: it derives the key from val.CacheUniqueKey().
func (c *Cache[T]) PutValue(ctx context.Context, val *T) error {
	key, err := c.uniqueKeyOf(val)
	if err != nil {
		return err
	}
	return c.PutValueByKey(ctx, key, val)
}

// Evict is the value-derived delete: it derives the key from val.CacheUniqueKey().
func (c *Cache[T]) Evict(ctx context.Context, val *T) error {
	key, err := c.uniqueKeyOf(val)
	if err != nil {
		return err
	}
	return c.EvictByKey(ctx, key)
}
