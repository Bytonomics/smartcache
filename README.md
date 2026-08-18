# smartcache

A small, dependency-light, type-safe generic read-through and delete-on-write cache for Go over a pluggable byte key-value backend.

## Install

```bash
go get github.com/Bytonomics/smartcache
```

## Core Concepts

- **`CacheStore` interface** — the injected cache backend (never the application's own database). `memstore` (in-memory) and `redisstore` (go-redis) are provided; bring your own implementation for any other system.
- **Generic `Cache[T]`** — read-through `Get` with a loader function; write-through `Put` with a writer function; `PutValue` to cache a value you already hold, with no external write; `Evict` and `EvictMany` for delete-on-write.
- **Required positive TTL backstop** — `Options.TTL` must be set. Opt in to no expiry via `AllowInfinite: true`.
- **Optional negative caching** — cache "not found" results for a separate duration via `Options.NegativeTTL`.
- **Singleflight de-duplication** — concurrent cold loads for the same key are serialized (on by default; disable via `DisableSingleflight: true`).
- **Pluggable `Codec`** — customize serialization. JSON is the default when `Options.Codec` is nil.

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
