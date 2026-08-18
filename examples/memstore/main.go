// Command memstore-example demonstrates smartcache.Cache[T] backed by the
// in-memory memstore.Store. It needs no external infrastructure — just run:
//
//	go run ./examples/memstore
package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Bytonomics/smartcache"
	"github.com/Bytonomics/smartcache/memstore"
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
	store := memstore.New()

	users, err := smartcache.New[User](store, smartcache.Options{
		Prefix:      "user",
		TTL:         time.Hour,
		NegativeTTL: 30 * time.Second, // cache "not found" briefly too
	})
	if err != nil {
		panic(err)
	}

	ctx := context.Background()
	loader := func(ctx context.Context) (*User, error) {
		return loadUserFromDB(ctx, "u_1")
	}

	fmt.Println("=== Step 1: First Get for \"u_1\" (the cache is empty) ===")
	fmt.Println("There is nothing under this key yet, so Get calls the loader you passed in —")
	fmt.Println("simulating a real database read. The loaded value is then written to the cache")
	fmt.Println("so the next lookup for the same key can skip the database entirely.")
	user, outcome, err := users.Get(ctx, "u_1", loader)
	must(err)
	fmt.Printf("Result: %+v\n", *user)
	fmt.Printf("Outcome: %s  (fetched from the source and cached for next time)\n\n", outcome)

	fmt.Println("=== Step 2: Second Get for \"u_1\" (same key, right after Step 1) ===")
	fmt.Println("The value is already cached, so the loader is NOT called — notice there is no")
	fmt.Println("[DB] line below. This is the whole point of a read-through cache: repeat reads")
	fmt.Println("for a hot key stop touching the database at all.")
	user, outcome, err = users.Get(ctx, "u_1", loader)
	must(err)
	fmt.Printf("Result: %+v\n", *user)
	fmt.Printf("Outcome: %s  (served entirely from cache, no database round trip)\n\n", outcome)

	fmt.Println("=== Step 3: Get for \"u_404\" (a user that does not exist) ===")
	fmt.Println("The loader reports \"not found\" for this id. Because Options.NegativeTTL was set")
	fmt.Println("to 30s, smartcache also remembers this \"not found\" result — so repeated lookups")
	fmt.Println("of the same missing id (a common source of cache-penetration load) skip the")
	fmt.Println("database too, for as long as the negative entry stays valid.")
	_, outcome, err = users.Get(ctx, "u_404", func(ctx context.Context) (*User, error) {
		return loadUserFromDB(ctx, "u_404")
	})
	fmt.Printf("Result: err=%v\n", err)
	fmt.Printf("Outcome: %s  (this call reached the loader; a repeat within 30s would report NegativeHit instead)\n", outcome)
	fmt.Printf("Caller check: errors.Is(err, smartcache.ErrNotFound) = %v\n\n", errors.Is(err, smartcache.ErrNotFound))

	fmt.Println("=== Step 4: Put \"u_2\" directly, without going through Get ===")
	fmt.Println("Put is for when you already hold the fresh value — e.g. right after your own")
	fmt.Println("code just inserted it into the database — and want to warm the cache immediately")
	fmt.Println("instead of waiting for the next Get to trigger a load.")
	must(users.Put(ctx, "u_2", &User{ID: "u_2", Name: "Grace Hopper"}))
	user, outcome, err = users.Get(ctx, "u_2", loader)
	must(err)
	fmt.Printf("Result: %+v\n", *user)
	fmt.Printf("Outcome: %s  (served from what Put stored; the loader above was never invoked)\n\n", outcome)

	fmt.Println("=== Step 5: Evict \"u_1\" (e.g. after updating the user elsewhere) ===")
	fmt.Println("Evict removes the cached entry right away — this is smartcache's delete-on-write")
	fmt.Println("path, used after a write to your source of truth so stale data is never served.")
	fmt.Println("The next Get for this key is therefore a cold read again, just like Step 1.")
	must(users.Evict(ctx, "u_1"))
	user, outcome, err = users.Get(ctx, "u_1", loader)
	must(err)
	fmt.Printf("Result: %+v\n", *user)
	fmt.Printf("Outcome: %s  (Evict forced this back to a cache miss)\n", outcome)
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
