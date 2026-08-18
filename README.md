# smartcache

A small, dependency-light Go cache library whose one generic, type-safe `Cache[T]` does **both read-through and write-through** over a **pluggable, backend-agnostic store** — so you stop re-implementing the miss → load → populate → invalidate dance for every entity, and you can swap Redis for in-memory (or your own backend) without touching a single call site.

## Install

```bash
go get github.com/Bytonomics/smartcache
```

## Why smartcache

Most Go caching options are either a **raw store** (a Redis client, an in-memory map) that hands you `get`/`set` and leaves you to build — and repeat — the read-through, write-through, and invalidation logic at every call site, or an **in-memory-only cache** with no way to put a real backend behind the same API. smartcache is the missing middle: one primitive that owns that orchestration for you, generically and type-safely, over whatever backend you inject.

**The core value:**

- **One `Cache[T]` for both directions** — read-through (`Get` + a loader) *and* write-through (`Put` + a writer) in the same type-safe primitive; no `interface{}`, no manual casts.
- **Backend-agnostic** — `Cache[T]` talks only to a small `CacheStore` interface. `redisstore` and `memstore` ship in the box; anything else (an LRU, `ristretto`, `bigcache`, your own service) is a tiny adapter, and swapping backends never changes a call site.

**Correct by default** — the choices that prevent the classic caching bugs are made for you, not left as footguns:

- **Required TTL backstop** — a value can never be cached forever by accident, so a missed or failed invalidation self-heals within a bounded window (opt into no-expiry explicitly with `AllowInfinite`).
- **Delete-on-write** — writes evict rather than overwrite in place, avoiding the concurrent stale-set race.
- **A cache failure never fails your operation** — if writing to the backend fails, your `Get`/`Put` still returns the real value; the cache stays a performance layer, not a hard dependency.
- **Cache-stampede protection** — concurrent reads that miss the same key are coalesced into a single load (see below).
- **Optional negative caching** — briefly remember "not found" so repeated probes of bad or non-existent ids don't reach your database.

### Cache stampede

A **cache stampede** — also called a **thundering herd** (or "dog-piling") — happens when a hot key is missing or has just expired and many concurrent requests all miss it at the same instant, so they *all* fall through to the database at once and can overwhelm it. smartcache prevents this on reads: concurrent `Get` calls for the same key are coalesced so that only one runs your loader and performs the write-back, while the rest wait and share that single result. This is on by default (disable with `DisableSingleflight`). Writes are never coalesced — each `Put`/`PutValue` is a distinct intended write.

Background reading: [Cache stampede (Wikipedia)](https://en.wikipedia.org/wiki/Cache_stampede).

## Features

- **Read-through (`Get` + loader)** — on a cache miss, `Get` runs the loader function you pass, caches exactly what it returned, and returns it; on a hit, the loader never runs. Repeat reads for a hot key stop touching your database.
- **Write-through (`Put` + writer)** — `Put` persists a value to your source of truth via your writer function, then caches exactly what was written, so the next `Get` for that key is served from cache with no database round trip. (`PutValue` caches a value you already hold, with no external write.)
- **Delete-on-write (`Evict` / `EvictMany`)** — after a write or update, you evict the key rather than overwrite it in place; the next read reloads the truth. Evicting instead of updating avoids the concurrent *stale-set race*, where two overlapping updates can leave the older value cached.
- **Required TTL backstop** — `Options.TTL` must be positive (or you must opt into no expiry explicitly with `AllowInfinite: true`). No entry is ever cached forever by accident, so a missed or failed eviction can only leave a stale value for a bounded window, never permanently.
- **Negative caching (opt-in via `NegativeTTL`)** — a loader that reports "not found" (`ErrNotFound`) is remembered briefly, so repeated lookups of a bad or non-existent id don't keep falling through to your database — a defense against cache-penetration load.
- **Single-process stampede protection (on by default; `DisableSingleflight` to turn off)** — concurrent `Get` calls that miss the same key are coalesced so only one runs the loader and the write-back, while the rest wait and share that single result. This prevents a *cache stampede / thundering herd* (see [Why smartcache](#why-smartcache)). It de-duplicates within one process; across multiple instances each process still loads once.
- **Pluggable backend + codec** — `Cache[T]` talks only to the small `CacheStore` interface, so `memstore` (in-memory), `redisstore` (go-redis), or your own adapter are interchangeable without changing call sites. Serialization is a pluggable `Codec` (JSON by default when `Options.Codec` is nil) — swap it to compress or encrypt values.
- **Prefix namespacing** — each `Cache[T]` can carry an `Options.Prefix`, so different entity caches (`user:`, `org:`, …) never collide on keys in a shared backend.
- **A cache failure never fails your operation** — if writing the result to the backend fails, `Get` still returns the loaded value (`LoadedNotCached`) and `Put` still returns the written value (`WrittenNotCached`). The cache stays a performance layer, not a hard dependency. (An `Evict` failure *is* returned, so you can retry or alert — see [Failure Semantics](#failure-semantics).)
- **Type-safety + pointer guard** — `Cache[T]` is generic: no `interface{}`, no manual casts. `New[T]` panics at construction if `T` is itself a pointer type, catching the `New[*User]` mistake (a `**User` that could smuggle a nil through a non-nil outer pointer) up front rather than as a surprise nil at runtime.

## Failure Semantics

- **Populate failures** — if the backend `Set` call fails after a loader runs, `Get` still returns the loaded value and reports `Outcome == LoadedNotCached`. If the backend `Set` call fails after a writer runs, `Put` still returns the written value and reports `Outcome == WrittenNotCached`. Neither the read nor the write fails — only caching the result failed.
- **Writer failures** — if `writer` returns a non-nil error, `Put` returns that error unchanged and the cache is left untouched. If `writer` returns `(nil, nil)`, `Put` returns `ErrNilWrite` and the cache is left untouched — the cache is never set to a nil value.
- **Evict failures** — if a backend `Delete` call fails, the error is returned to the caller so it can retry or alert.
- **TTL backstop** — the TTL bounds how long any missed or failed eviction can leave a stale value in the cache.

## Outcomes

`Get` and `Put` each report an `Outcome` so callers can meter cache-hit rate and alert on populate failures. `Get` never returns a `Put`-only outcome, and `Put` never returns a `Get`-only outcome — the two are read-side and write-side vocabularies, not interchangeable.

`Get` returns one of:

- `Hit` — value was served from the cache (and not expired).
- `Loaded` — a miss occurred, the loader ran, and the value was cached.
- `LoadedNotCached` — a miss occurred, the loader ran, but the backend `Set` failed; the value is still returned, uncached.
- `NegativeHit` — a previously cached "not found" was served.

`Put` returns one of:

- `Written` — the writer ran and the value it returned was cached.
- `WrittenNotCached` — the writer ran, but the backend `Set` failed; the value is still returned, uncached.

`PutValue` returns only an `error` — it has no `Outcome`, since it performs no external write to characterize.

## Usage

Setup — one `Cache[User]` instance, shared by every snippet below:

```go
import (
	"context"
	"fmt"
	"time"

	"github.com/Bytonomics/smartcache"
	"github.com/Bytonomics/smartcache/redisstore"
	"github.com/redis/go-redis/v9"
)

type User struct {
	ID   string
	Name string
}

func main() {
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	store := redisstore.New(rdb)

	users, err := smartcache.New[User](store, smartcache.Options{
		Prefix: "user",
		TTL:    time.Hour,
	})
	if err != nil {
		panic(err)
	}
	ctx := context.Background()

	// ... the snippets below continue here, using ctx and users.
}
```

### `Get` — read-through

```go
user, outcome, err := users.Get(ctx, "u_123", func(ctx context.Context) (*User, error) {
	return loadUserFromDB(ctx, "u_123") // your own database call
})
if err != nil {
	panic(err)
}
fmt.Println(user.Name, outcome)
```

`Get` checks the cache for `"u_123"` first. On a miss, it calls the loader function you passed in, caches exactly
the value the loader returned, and returns that value. On a hit, the loader is never called at all — this is what
makes repeat reads for a hot key stop touching your database. `outcome` reports which of these happened: `Hit`,
`Loaded`, `LoadedNotCached`, or `NegativeHit` — see [Outcomes](#outcomes) for what each one means.

### `Put` — write-through

```go
newUser := &User{ID: "u_456", Name: "Ada Lovelace"}
saved, outcome, err := users.Put(ctx, "u_456", func(ctx context.Context) (*User, error) {
	if err := saveUserToDB(ctx, newUser); err != nil { // your own database call
		return nil, err
	}
	return newUser, nil
})
if err != nil {
	panic(err)
}
fmt.Println(saved.Name, outcome)
```

`Put` is for when the write itself should go through smartcache. It calls your `writer` function to persist the
value to your own source of truth, then caches exactly the value `writer` returned — so a `Get` for `"u_456"`
right after this `Put` is served from cache with no database round trip. If `writer` returns a non-nil error,
`Put` returns that error unchanged and the cache stays untouched. If `writer` returns `(nil, nil)`, `Put` returns
`ErrNilWrite`, because the cache is never set to a nil value. Unlike `Get`'s loader, `writer` is **never**
deduplicated: two concurrent `Put` calls for the same key are two distinct writes, and singleflight would
silently drop one of them. `outcome` is `Written` or `WrittenNotCached` — see [Outcomes](#outcomes).

### `PutValue` — direct cache write

```go
if err := users.PutValue(ctx, "u_456", newUser); err != nil {
	panic(err)
}
```

`PutValue` writes a value you already hold straight into the cache — no external write happens, no writer
function is called. Use it when your own code already performed the real write (e.g. you just ran the `INSERT`
yourself) and you only need the cache updated to match it. `PutValue` returns only an `error`; it has no
`Outcome`, since there is no external write for one to characterize.

### `Evict` — delete-on-write

```go
// After updating the user elsewhere (e.g. via Put's writer, or your own code):
if err := users.Evict(ctx, "u_123"); err != nil {
	panic(err)
}
```

`Evict` removes the cached entry for `"u_123"` right away. Call it after the source of truth changes outside of
`Put`/`PutValue`, so the next `Get` for that key is a clean read-through instead of serving stale data.
`EvictMany` does the same for several keys at once, joining any errors.

## Examples

Runnable, end-to-end examples live in [`examples/`](./examples), demonstrating every method above — `Get` (miss
then hit), negative caching, `PutValue`, `Put`, and `Evict` — against both backends:

```bash
cd examples
make deps   # first time only — downloads examples' own dependencies

# No infrastructure needed:
make run-memstore

# Needs a local Redis:
docker run --rm -p 6379:6379 redis:7
make run-redisstore
```

`examples` is its own Go module (own `go.mod`/`go.sum`, `replace`d to the local checkout) so trying things out —
or adding a new example with its own dependencies — never touches the root module's dependency graph.

## Implementation Notes

The backend is an interface, so any store — or a fake, for tests — can replace Redis or in-memory storage without touching call sites.

### Shared values under singleflight

When singleflight is enabled (the default), a `Get` call that returns `Loaded` or `LoadedNotCached` may hand back
the exact same `*T` given to every other concurrent caller deduped onto that same loader call — this is
`singleflight.Do`'s own contract: all duplicate callers receive the one result the leader produced. Treat a
`Loaded`/`LoadedNotCached` result as read-only; copy it before mutating. A `Hit` result is different: `Get`
unmarshals a fresh value from the cache on every call, so it is never shared with another caller.

### The `CacheStore` interface

To back `Cache[T]` with your own storage system, implement `CacheStore`:

```go
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
```

`Cache[T]` owns all serialization — `CacheStore` only ever sees raw bytes, never the cached type `T`. Pass your
implementation to `smartcache.New[T](store, opts)` in place of `memstore.New()` or `redisstore.New(rdb)`.

### Adapting another cache library

To back `Cache[T]` with any existing Go cache (an LRU, `ristretto`, `bigcache`, `patrickmn/go-cache`, …), write a
small adapter type that implements `CacheStore` by calling into that library — the same pattern `memstore` and
`redisstore` already use. `Get` must return `ErrStoreMiss` (not the underlying library's own miss value) so
`Cache[T]` recognizes it as a read-through miss.

## License

MIT
