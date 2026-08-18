# smartcache

A small, dependency-light, type-safe generic read-through and delete-on-write cache for Go over a pluggable byte key-value backend.

## Install

```bash
go get github.com/Bytonomics/smartcache
```

## Core Concepts

- **`Store` interface** — the injected backend. `memstore` (in-memory) and `redisstore` (go-redis) are provided; bring your own implementation for any other system.
- **Generic `Cache[T]`** — read-through `Get` with a loader function; `Put` for write-through; `Evict` and `EvictMany` for delete-on-write.
- **Required positive TTL backstop** — `Options.TTL` must be set. Opt in to no expiry via `AllowInfinite: true`.
- **Optional negative caching** — cache "not found" results for a separate duration via `Options.NegativeTTL`.
- **Singleflight de-duplication** — concurrent cold loads for the same key are serialized (on by default; disable via `DisableSingleflight: true`).
- **Pluggable `Codec`** — customize serialization. JSON is the default when `Options.Codec` is nil.

## Failure Semantics

- **Populate failures** — if the backend `Set` call fails after a loader runs, `Get` still returns the loaded value and reports `Outcome == LoadedNotCached`. The cache does not fail the read.
- **Evict failures** — if a backend `Delete` call fails, the error is returned to the caller so it can retry or alert.
- **TTL backstop** — the TTL bounds how long any missed or failed eviction can leave a stale value in the cache.

## Outcomes

`Get` returns one of four outcomes so callers can meter cache-hit rate or alert on failures:

- `Hit` — value was in the cache (and not expired).
- `Loaded` — value was loaded from the backend and cached.
- `LoadedNotCached` — value was loaded but the backend `Set` failed; not cached.
- `NegativeHit` — a previous "not found" was cached and has not expired.

## Usage

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
	user, outcome, err := users.Get(ctx, "u_123", func(ctx context.Context) (*User, error) {
		return loadUserFromDB(ctx, "u_123")
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(user.Name, outcome)

	// After updating the user in your database:
	if err := users.Evict(ctx, "u_123"); err != nil {
		panic(err)
	}
}
```

## Examples

Runnable, end-to-end examples live in [`examples/`](./examples), demonstrating the same `User`-caching walkthrough
against both backends:

```bash
cd examples

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

### Adapting another cache library

`Store` has four methods (`Get`/`Set`/`Delete`/`Exists`, all `[]byte`-in/`[]byte`-out). To back `Cache[T]` with any
existing Go cache (an LRU, `ristretto`, `bigcache`, `patrickmn/go-cache`, …), write a small adapter type that
implements `Store` by calling into that library — the same pattern `memstore` and `redisstore` already use. `Get`
must return `ErrStoreMiss` (not the underlying library's own miss value) so `Cache[T]` recognizes it as a
read-through miss.

## License

MIT
