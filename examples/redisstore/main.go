// Command redisstore-example demonstrates smartcache.Cache[T] backed by a
// real Redis via redisstore.Store. Start a local Redis first:
//
//	docker run --rm -p 6379:6379 redis:7
//
// Then run (REDIS_ADDR defaults to localhost:6379 if unset):
//
//	go run ./examples/redisstore
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Bytonomics/smartcache"
	"github.com/Bytonomics/smartcache/redisstore"
)

// User is the example's cached type. Any type works with Cache[T] — there is
// nothing memstore- or redisstore-specific about it.
type User struct {
	ID   string
	Name string
}

// loadUserFromDB stands in for a real database call. It "finds" u_1 and
// u_2, and reports anything else as not found.
func loadUserFromDB(_ context.Context, id string) (*User, error) {
	fmt.Printf("  [DB] loading %s...\n", id)
	switch id {
	case "u_1":
		return &User{ID: "u_1", Name: "Ada Lovelace"}, nil
	case "u_2":
		return &User{ID: "u_2", Name: "Grace Hopper"}, nil
	default:
		return nil, smartcache.ErrNotFound
	}
}

func main() {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}

	rdb := redis.NewClient(&redis.Options{Addr: addr})
	defer func() { _ = rdb.Close() }()

	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		fmt.Printf("could not reach Redis at %s: %v\n", addr, err)
		fmt.Println("start one with: docker run --rm -p 6379:6379 redis:7")
		os.Exit(1)
	}

	store := redisstore.New(rdb)

	users, err := smartcache.New[User](store, smartcache.Options{
		Prefix:      "smartcache-example:user",
		TTL:         time.Hour,
		NegativeTTL: 30 * time.Second, // cache "not found" briefly too
	})
	if err != nil {
		panic(err)
	}

	loader := func(ctx context.Context) (*User, error) {
		return loadUserFromDB(ctx, "u_1")
	}

	fmt.Println("=== Step 1: First Get for \"u_1\" (nothing in Redis under this key yet) ===")
	fmt.Println("The cache has no entry for this key, so Get calls the loader you passed in —")
	fmt.Println("simulating a real database read. The loaded value is then written to Redis, over")
	fmt.Println("the network, so the next lookup for the same key can skip the database entirely.")
	user, outcome, err := users.Get(ctx, "u_1", loader)
	must(err)
	fmt.Printf("Result: %+v\n", *user)
	fmt.Printf("Outcome: %s  (fetched from the source and written to Redis for next time)\n\n", outcome)

	fmt.Println("=== Step 2: Second Get for \"u_1\" (same key, right after Step 1) ===")
	fmt.Println("The value is already sitting in Redis, so the loader is NOT called — notice there")
	fmt.Println("is no [DB] line below. This round trip only talks to Redis, not your database.")
	user, outcome, err = users.Get(ctx, "u_1", loader)
	must(err)
	fmt.Printf("Result: %+v\n", *user)
	fmt.Printf("Outcome: %s  (served entirely from Redis, no database round trip)\n\n", outcome)

	fmt.Println("=== Step 3: Get for \"u_404\" (a user that does not exist) ===")
	fmt.Println("The loader reports \"not found\" for this id. Because Options.NegativeTTL was set")
	fmt.Println("to 30s, smartcache also writes this \"not found\" result to Redis — so repeated")
	fmt.Println("lookups of the same missing id (a common source of cache-penetration load) skip")
	fmt.Println("the database too, for as long as the negative entry stays valid.")
	_, outcome, err = users.Get(ctx, "u_404", func(ctx context.Context) (*User, error) {
		return loadUserFromDB(ctx, "u_404")
	})
	fmt.Printf("Result: err=%v\n", err)
	fmt.Printf("Outcome: %s  (this call reached the loader; a repeat within 30s would report NegativeHit instead)\n", outcome)
	fmt.Printf("Caller check: errors.Is(err, smartcache.ErrNotFound) = %v\n\n", errors.Is(err, smartcache.ErrNotFound))

	fmt.Println("=== Step 4: PutValue \"u_2\" directly, without going through Get ===")
	fmt.Println("PutValue is for when you already hold the fresh value — e.g. right after your own")
	fmt.Println("code just inserted it into the database — and want to warm Redis immediately")
	fmt.Println("instead of waiting for the next Get to trigger a load.")
	must(users.PutValue(ctx, "u_2", &User{ID: "u_2", Name: "Grace Hopper"}))
	user, outcome, err = users.Get(ctx, "u_2", loader)
	must(err)
	fmt.Printf("Result: %+v\n", *user)
	fmt.Printf("Outcome: %s  (served from what PutValue stored; the loader above was never invoked)\n\n", outcome)

	fmt.Println("=== Step 5: Put \"u_3\" through a writer function (write-through) ===")
	fmt.Println("Put is for when the write itself should go through smartcache: it calls your")
	fmt.Println("writer function to persist the value to the source of truth, then caches exactly")
	fmt.Println("the value writer returned. Unlike Get's loader, writer is never deduplicated —")
	fmt.Println("every Put call performs its own write, since dropping a concurrent write would")
	fmt.Println("silently lose data.")
	newUser := &User{ID: "u_3", Name: "Katherine Johnson"}
	_, outcome, err = users.Put(ctx, "u_3", func(_ context.Context) (*User, error) {
		fmt.Printf("  [DB] writing %s...\n", newUser.ID)
		return newUser, nil
	})
	must(err)
	fmt.Printf("Outcome: %s  (writer ran and the value it returned was cached in Redis)\n\n", outcome)

	user, outcome, err = users.Get(ctx, "u_3", loader)
	must(err)
	fmt.Printf("Result: %+v\n", *user)
	fmt.Printf("Outcome: %s  (served from what Put cached; the loader above was never invoked)\n\n", outcome)

	fmt.Println("=== Step 6: Evict \"u_1\" (e.g. after updating the user elsewhere) ===")
	fmt.Println("Evict deletes the entry from Redis right away — this is smartcache's")
	fmt.Println("delete-on-write path, used after a write to your source of truth so stale data")
	fmt.Println("is never served. The next Get for this key is therefore a cold read again, just")
	fmt.Println("like Step 1.")
	must(users.Evict(ctx, "u_1"))
	user, outcome, err = users.Get(ctx, "u_1", loader)
	must(err)
	fmt.Printf("Result: %+v\n", *user)
	fmt.Printf("Outcome: %s  (Evict forced this back to a cache miss)\n\n", outcome)

	fmt.Println("=== Step 7: Cleanup ===")
	fmt.Println("Removing this example's keys from Redis so re-running it starts from a clean")
	fmt.Println("slate — real applications rely on TTL expiry instead of doing this manually.")
	must(users.EvictMany(ctx, "u_1", "u_2", "u_3", "u_404"))
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
