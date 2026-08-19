# smartcache Roadmap

A living record of what smartcache can do today and what is planned next.

smartcache began as a small read-through cache and grew into a general-purpose
caching layer. This document lists **features** — things you can use to build
value — in the rough order they arrived, and the features still to come. It is
updated as the library evolves.

**Status key:** ✅ = shipped (usable now) · ⚠️ = planned (not built yet)

---

## Design north star

The rules used to decide what to accept next.

| Principle | What it means |
|---|---|
| Correct by default | The safe choice is automatic: bounded lifetime, delete-on-write, jittered expiry, and a cache failure that never fails your operation. |
| Bounded staleness, never permanent | A missed invalidation always self-heals within a window — which is why serve-stale is planned and write-behind (silent data-loss risk) is excluded. |
| Small surface | New backends, compression, and encryption arrive as adapters, not as new core features. |

---

The tables below are the index: each feature name links to its full description
in [Feature details](#feature-details).

## Shipped

### Milestone 1 — Read-through cache (Aug 18, 2026)

The starting point: a typed cache you read *through* to your own data source.

| Feature | Status | In short |
|---|---|---|
| [Type-safe cache](#type-safe-cache) | ✅ | Any value type, no casts. |
| [Read-through reads](#read-through-reads) | ✅ | A miss loads, then serves from cache. |
| [Bring-your-own backend](#bring-your-own-backend) | ✅ | In-memory, Redis, or your own. |
| [Pluggable serialization](#pluggable-serialization) | ✅ | JSON default; swappable. |
| [Per-cache key namespacing](#per-cache-key-namespacing) | ✅ | No key collisions across caches. |
| [Required expiry backstop](#required-expiry-backstop) | ✅ | Bounded lifetime; opt-in infinite. |
| [Optional negative caching](#optional-negative-caching) | ✅ | Remember "not found" briefly. |
| [Cache-stampede protection](#cache-stampede-protection) | ✅ | One load for a herd of misses. |
| [Result reporting](#result-reporting) | ✅ | How each read was served. |
| [Delete-on-write invalidation](#delete-on-write-invalidation) | ✅ | Evict so the next read reloads. |
| [Runnable examples](#runnable-examples) | ✅ | End-to-end demos, both backends. |

### Milestone 2 — Write-through (Aug 18, 2026)

Writes can now flow through the cache, not only reads.

| Feature | Status | In short |
|---|---|---|
| [Write-through writes](#write-through-writes) | ✅ | Persist via your writer, then cache. |
| [Direct cache warming](#direct-cache-warming) | ✅ | Put a value you already hold. |

### Milestone 3 — Advanced cache (Aug 19, 2026)

smartcache became a central caching layer for many entity types at once.

| Feature | Status | In short |
|---|---|---|
| [Central cache manager with named caches](#central-cache-manager-with-named-caches) | ✅ | Shared defaults, per-entity overrides. |
| [Batch read-through](#batch-read-through) | ✅ | Many keys, one load for the misses. |
| [Anti-synchronized-expiry (TTL jitter)](#anti-synchronized-expiry-ttl-jitter) | ✅ | Spread expiry to avoid mass stampede. |
| [Built-in metrics export](#built-in-metrics-export) | ✅ | Per-cache OpenTelemetry metrics. |

---

## Planned

### Freshness & availability

| Feature | Status | In short |
|---|---|---|
| [Serve-stale-while-refreshing](#serve-stale-while-refreshing) | ⚠️ | Serve stale, refresh in background. |
| [Refresh-before-expiry](#refresh-before-expiry) | ⚠️ | Refresh hot keys before they expire. |

### Scale-out

| Feature | Status | In short |
|---|---|---|
| [Cross-instance stampede protection](#cross-instance-stampede-protection) | ⚠️ | One reload across the whole fleet. |
| [Two-level caching](#two-level-caching) | ⚠️ | In-process layer over the shared cache. |
| [Bulk invalidation by group](#bulk-invalidation-by-group) | ⚠️ | Evict a whole family in one call. |
| [Alias-based multi-key caching](#alias-based-multi-key-caching) | ⚠️ | One value, many lookup keys; delete by any cascades to all. |
| [Bloom-filter alias routing](#bloom-filter-alias-routing) | ⚠️ | Skip the Lua path for provably-non-aliased keys. |
| [Batch stampede protection](#batch-stampede-protection) | ⚠️ | Dedupe overlapping batch loads. |

### Store & operations

| Feature | Status | In short |
|---|---|---|
| [Size-bounded in-memory cache](#size-bounded-in-memory-cache) | ⚠️ | Memory ceiling with LRU eviction. |
| [Backend health-gating](#backend-health-gating) | ⚠️ | Bypass an unhealthy backend. |
| [Cache warming](#cache-warming) | ⚠️ | Preload hot keys at start-up. |

### Telemetry

| Feature | Status | In short |
|---|---|---|
| [More metrics outputs](#more-metrics-outputs) | ⚠️ | Pull endpoint + trace links. |

---

## Available today with no new code

These deliver value now through the existing plug-in points — you write a small adapter, not a change to smartcache.

| Feature | Status | In short |
|---|---|---|
| [Compressed cached values](#compressed-cached-values) | ✅ | Shrink large entries. |
| [Encrypted-at-rest cached values](#encrypted-at-rest-cached-values) | ✅ | Protect sensitive entries. |
| [Any other storage backend](#any-other-storage-backend) | ✅ | LRU, managed cache, or fake. |

---

## Feature details

### Type-safe cache
Cache any value type directly. The cache is generic over your type, so reads and writes return your type with no `interface{}` and no manual casts. One cache instance holds one type.

### Read-through reads
On a miss, the loader you pass runs, its result is stored, and the value is returned. Later reads for the same key are served from the cache and never call your loader — the whole point of a read-through cache: repeat reads for a hot key stop touching your database.

### Bring-your-own backend
The cache talks to a small backend interface, not a concrete store. An in-memory store and a Redis store ship in the box; any other store — an LRU library, a managed cache service, or a test fake — works by implementing the same interface. Swapping the backend never changes a call site.

### Pluggable serialization
The cache owns turning your value into bytes and back. JSON is the default; you can supply your own encoding — for speed, size, or a specific format — without touching call sites.

### Per-cache key namespacing
Each cache can carry a key prefix, so two caches that both use the id "42" never collide inside a shared backend.

### Required expiry backstop
Every entry gets a bounded lifetime by default. A missed or failed invalidation can therefore only leave data stale for a known window, never forever. You can opt into never-expire explicitly when a value truly never changes.

### Optional negative caching
Briefly remember a "not found" result. Repeated look-ups of a bad, missing, or cross-tenant id are then answered from the cache instead of hitting your database every time — a defense against cache-penetration load. Off by default; activates only when you set a negative lifetime.

### Cache-stampede protection
When many requests miss the same cold key at the same instant, only one runs the loader; the rest wait and share that one result, instead of a herd of identical loads hitting your database at once. On by default; can be turned off.

### Result reporting
Every read reports how it was served — from cache, freshly loaded, loaded-but-not-cached (the backend write failed), or a cached "not found". You meter hit-rate and alarm on failures with this signal; the library never logs on its own.

### Delete-on-write invalidation
Evict a key, or a small set of keys, so the next read reloads fresh data. This is delete-on-write — remove, don't overwrite — which avoids the stale-set race that update-in-place caches suffer.

### Runnable examples
End-to-end example programs for both the in-memory and Redis backends, showing every method with commentary on why each step behaves as it does.

### Write-through writes
Persist a value through your own write function, then cache exactly what was written — so a read right after is served from cache with no round trip. Concurrent writes are never coalesced: two writes to the same key are two real writes, never silently dropped.

### Direct cache warming
Place a value you already hold straight into the cache, for when your own code already did the real write and only needs the cache updated to match.

### Central cache manager with named caches
A manager holds the backend, a set of default options, and (optionally) a metrics exporter. You register one named cache per entity type against it; each inherits the manager's defaults and overrides only what it needs. One place owns caching configuration for a whole application.

### Batch read-through
Look up many keys in one call. Keys already cached are served directly; every key that misses is collected and loaded in a single call to your batch loader, then each result is cached. It uses the backend's bulk read when the backend has one, and falls back to per-key reads otherwise. Keys that do not exist are omitted from the result.

### Anti-synchronized-expiry (TTL jitter)
Each entry's lifetime is shortened by a small random amount by default. A batch of keys written together therefore does not all expire at the same instant, which prevents a synchronized mass-expiry that would stampede your database. It is downward-only (a jittered key never lives longer than its configured lifetime), on by default, tunable, and can be turned off per cache.

### Built-in metrics export
Point a manager at an OpenTelemetry endpoint and it exports per-cache counters (hit, miss, load, load-error, eviction) and a load-latency measure, in the background, with no manual wiring and no logging. Caches without metrics configured pay nothing.

### Serve-stale-while-refreshing
Problem: when an entry expires, the next reader waits for a fresh load (a latency spike), and if the source is down the read fails. This gives an entry a soft and a hard expiry: past the soft expiry the value is stale-but-usable — return it at once and refresh in the background; only past the hard expiry is it gone. If a refresh fails, keep serving the last good value. Value: no latency spike at expiry, and reads survive a brief source outage.

### Refresh-before-expiry
Problem: a hot key expires and the first reader after it pays the full reload cost. This refreshes a hot key shortly before it expires — on a schedule, or probabilistically as expiry nears. Value: a hot key never actually goes cold. Contrast with serve-stale: this refreshes before expiry (never stale); serve-stale refreshes after expiry (stale meanwhile).

### Cross-instance stampede protection
Problem: today's stampede protection dedupes loads within one process, so with N instances each can still reload the same cold key — N loads hit the database. This adds a distributed lock so only one instance across the fleet recomputes; the rest wait or serve stale. Value: caps database load at about one reload per cold key fleet-wide.

### Two-level caching
Problem: even a Redis hit costs a network round-trip. This puts a small in-process cache in front of the shared cache: reads check the in-process layer first and fall through on a miss. It needs an in-process invalidation plan — a short local lifetime, or a notify-on-write signal. Value: cuts round-trips and latency for very hot keys and offloads the shared cache.

### Bulk invalidation by group
Problem: sometimes you must invalidate many related keys at once — everything for a tenant, or all of a user's data when their membership changes — and today you would evict each key individually. This tags keys with a group and evicts the whole group in one call (via a per-tag key set, or a versioned prefix). Value: one call invalidates a whole family — what a tenant/permission fan-out needs for correctness.

### Batch stampede protection
Problem: batch read-through does not dedupe across concurrent batch calls, so two overlapping batch look-ups each load the overlap. This extends the one-load-per-key idea to batches, so an id is loaded once even across concurrent batch calls. Value: the same stampede protection for batch reads that single-key reads already have.

### Alias-based multi-key caching
Problem: a record is often reachable by more than one key (a user by id, email, and slug), so a caller must write and invalidate every key by hand — miss one and it serves stale data. This lets the library associate many lookup keys with one value: the value is stored once at a primary key, each alias is a small pointer to it, and a per-primary members set indexes them. A delete or update through any key cascades to the whole group, cleaned atomically via a Lua script, with one shared TTL across every key. Opt-in via a dedicated `RegisterAliasGroup` constructor (one group per cache, name auto-derived); non-aliasing caches keep today's light path unchanged; single-process (mems4) is exact. Value: the caller stops tracking multi-key sets, and a missed invalidation can no longer leak or serve stale. Full design: `docs/1-alias-cache-design.md`.

### Bloom-filter alias routing
Problem: an alias-enabled cache routes even its plain, non-grouped keys through the Lua path — correct but wasteful. This adds an in-memory bloom filter that proves a key is definitely not grouped so it can skip Lua and use the light path — a pure performance optimization with no role in correctness. Deferred: an in-memory filter is per-process, so it is exact single-process but needs a warming strategy to be safe as a routing gate across instances; a cuckoo filter is recommended if immediate deletion is wanted. Value: cuts the grouped-path cost for non-aliased keys in an aliasing cache. Full analysis (memory math, cross-instance hazard, cuckoo recommendation): `docs/2-future-bloom-filter-routing.md`.

### Size-bounded in-memory cache
Problem: the shipped in-memory store is unbounded — fine for tests, unsafe as a real in-process cache because it grows without limit. This adds a maximum size with automatic eviction of least-recently-used entries. Value: use the in-memory backend in production with a memory ceiling.

### Backend health-gating
Problem: if the cache backend is slow or down, every request still tries it, adding latency and piling onto a struggling backend. A circuit breaker opens after repeated failures — skip the cache and go straight to the source for a cool-down window, then probe to recover. Value: a degraded cache backend stops dragging request latency down.

### Cache warming
Problem: right after a restart or deploy the cache is empty, so the first wave of requests all miss and hit the database. This preloads known-hot keys at start-up (or on a schedule) so the cache is warm before traffic arrives. Value: avoids the cold-start miss storm.

### More metrics outputs
Problem: metrics today go out via OTLP push; some environments scrape (pull), and some want to tie a slow load back to a request trace. This adds a pull-based scrape endpoint alongside push, and links a cache metric to the trace that produced it. Value: fits pull-based monitoring stacks and speeds up debugging.

### Compressed cached values
Wrap serialization to compress large entries before they reach the backend, and decompress on read. Available today by supplying a compressing serializer — no change to smartcache itself.

### Encrypted-at-rest cached values
Wrap serialization to encrypt sensitive entries before they reach the backend, and decrypt on read. Available today by supplying an encrypting serializer.

### Any other storage backend
Back the cache with any store — an LRU library, a managed cache service, or a test fake — by implementing the same small backend interface the shipped stores use.
