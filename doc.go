// Package smartcache is a small, dependency-free, type-safe caching library.
//
// It provides a generic read-through / delete-on-write cache (Cache[T]) over a
// pluggable byte key-value backend (the CacheStore interface). Cache[T] owns
// serialization (JSON by default), enforces a positive TTL backstop so a missed
// or failed eviction can only cause bounded staleness, optionally negative-caches
// "not found" results, and de-duplicates concurrent cold loads with singleflight.
//
// The library performs no logging itself: Get returns an Outcome so the caller can
// meter cache-hit rate and detect populate failures. The backend is injected as an
// interface, so Redis (see subpackage redisstore) can be swapped for any other
// store (see subpackage memstore) without touching call sites.
package smartcache
