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

	fmt.Println("1. First Get (cold) — reads through to the DB:")
	user, outcome, err := users.Get(ctx, "u_1", loader)
	must(err)
	fmt.Printf("   got %+v, outcome=%s\n\n", *user, outcome)

	fmt.Println("2. Second Get (same key) — served from Redis, DB not touched:")
	user, outcome, err = users.Get(ctx, "u_1", loader)
	must(err)
	fmt.Printf("   got %+v, outcome=%s\n\n", *user, outcome)

	fmt.Println("3. Get a nonexistent user — negative-cached:")
	_, outcome, err = users.Get(ctx, "u_404", func(ctx context.Context) (*User, error) {
		return loadUserFromDB(ctx, "u_404")
	})
	fmt.Printf("   err=%v, outcome=%s, is-not-found=%v\n\n", err, outcome, errors.Is(err, smartcache.ErrNotFound))

	fmt.Println("4. Put a value directly (e.g. right after an insert):")
	must(users.Put(ctx, "u_2", &User{ID: "u_2", Name: "Grace Hopper"}))
	user, outcome, err = users.Get(ctx, "u_2", loader)
	must(err)
	fmt.Printf("   got %+v, outcome=%s\n\n", *user, outcome)

	fmt.Println("5. Evict after an update — the next Get reads through again:")
	must(users.Evict(ctx, "u_1"))
	user, outcome, err = users.Get(ctx, "u_1", loader)
	must(err)
	fmt.Printf("   got %+v, outcome=%s\n\n", *user, outcome)

	fmt.Println("6. Cleaning up example keys from Redis...")
	must(users.EvictMany(ctx, "u_1", "u_2", "u_404"))
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
