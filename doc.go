// Package smartcache is a small, type-safe caching library.
//
// It provides a generic read-through / write-through cache (Cache[T]) over a
// pluggable byte key-value backend (the CacheStore interface). Cache[T] owns
// serialization (JSON by default), enforces a positive TTL backstop so a missed
// or failed eviction can only cause bounded staleness, applies downward-only
// TTL jitter by default so keys written together don't expire in sync,
// optionally negative-caches "not found" results, and de-duplicates concurrent
// cold loads with singleflight.
//
// Every Cache[T] is built through a Manager: NewManager creates one over a
// CacheStore and a set of defaults, and the package-level generic function
// Register creates a named, type-safe Cache[T] on it (Go methods cannot have
// type parameters, so registration cannot be a method on Manager). GetMany
// batches a read-through lookup for several keys in one round trip, calling
// the caller's batch loader at most once for whatever keys are not already
// cached.
//
// The library performs no logging itself: Get, GetMany, and Put return an
// Outcome so the caller can meter cache-hit rate and detect populate failures
// without any of the library's own log lines. A Manager can optionally be
// configured with WithOTLP to export per-cache metrics (hit/miss/load/evict
// counters and a load-latency histogram) over OpenTelemetry OTLP, fire-and-
// forget on a background interval.
//
// The backend is injected as an interface, so Redis (see subpackage
// redisstore) can be swapped for any other store (see subpackage memstore)
// without touching call sites.
package smartcache
