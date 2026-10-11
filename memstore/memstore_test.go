package memstore_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Bytonomics/smartcache"
	"github.com/Bytonomics/smartcache/memstore"
)

// TestSetGet_RoundTrip verifies basic set and get operations.
func TestSetGet_RoundTrip(t *testing.T) {
	ctx := context.Background()
	s := memstore.New()

	err := s.Set(ctx, "k", []byte("hello"), time.Minute)
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	b, err := s.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if string(b) != "hello" {
		t.Fatalf("expected 'hello', got %q", string(b))
	}
}

// TestGet_Miss verifies that Get returns ErrStoreMiss for absent keys.
func TestGet_Miss(t *testing.T) {
	ctx := context.Background()
	s := memstore.New()

	b, err := s.Get(ctx, "absent")
	if b != nil {
		t.Fatalf("expected nil bytes for missing key, got %v", b)
	}

	if !errors.Is(err, smartcache.ErrStoreMiss) {
		t.Fatalf("expected ErrStoreMiss, got %v", err)
	}
}

// TestDelete_RemovesAndAbsentIsOK verifies delete removes keys and tolerates absent keys.
func TestDelete_RemovesAndAbsentIsOK(t *testing.T) {
	ctx := context.Background()
	s := memstore.New()

	// Set a key and delete it.
	err := s.Set(ctx, "k", []byte("v"), time.Minute)
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	err = s.Delete(ctx, "k")
	if err != nil {
		t.Fatalf("Delete existing key failed: %v", err)
	}

	// Verify the key is gone.
	_, err = s.Get(ctx, "k")
	if !errors.Is(err, smartcache.ErrStoreMiss) {
		t.Fatalf("expected ErrStoreMiss after delete, got %v", err)
	}

	// Delete a key that never existed.
	err = s.Delete(ctx, "never-existed")
	if err != nil {
		t.Fatalf("Delete absent key should return nil, got %v", err)
	}
}

// TestExists verifies existence checks for present and absent keys.
func TestExists(t *testing.T) {
	ctx := context.Background()
	s := memstore.New()

	// Set a key and check existence.
	err := s.Set(ctx, "k", []byte("v"), time.Minute)
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	ok, err := s.Exists(ctx, "k")
	if err != nil {
		t.Fatalf("Exists on present key failed: %v", err)
	}
	if !ok {
		t.Fatalf("Exists should return true for present key")
	}

	// Check existence of absent key.
	ok, err = s.Exists(ctx, "absent")
	if err != nil {
		t.Fatalf("Exists on absent key failed: %v", err)
	}
	if ok {
		t.Fatalf("Exists should return false for absent key")
	}
}

// TestTTLExpiry verifies that expired keys return ErrStoreMiss and Exists returns false.
func TestTTLExpiry(t *testing.T) {
	ctx := context.Background()
	s := memstore.New()

	// Set a key with a short TTL.
	err := s.Set(ctx, "k", []byte("v"), 30*time.Millisecond)
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Get immediately; should still be valid.
	_, err = s.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get on fresh key failed: %v", err)
	}

	// Wait for expiry.
	time.Sleep(80 * time.Millisecond)

	// Get should now fail with ErrStoreMiss.
	_, err = s.Get(ctx, "k")
	if !errors.Is(err, smartcache.ErrStoreMiss) {
		t.Fatalf("expected ErrStoreMiss after expiry, got %v", err)
	}

	// Exists should also return false.
	ok, err := s.Exists(ctx, "k")
	if err != nil {
		t.Fatalf("Exists on expired key failed: %v", err)
	}
	if ok {
		t.Fatalf("Exists should return false for expired key")
	}
}

// TestGetReturnsCopy verifies that Get returns a defensive copy, not the internal slice.
func TestGetReturnsCopy(t *testing.T) {
	ctx := context.Background()
	s := memstore.New()

	// Set a key.
	err := s.Set(ctx, "k", []byte("abc"), time.Minute)
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Get the value and mutate it.
	b, err := s.Get(ctx, "k")
	if err != nil {
		t.Fatalf("First Get failed: %v", err)
	}
	b[0] = 'X'

	// Get the value again; should be unchanged.
	b2, err := s.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Second Get failed: %v", err)
	}

	if string(b2) != "abc" {
		t.Fatalf("expected 'abc' after mutation of first copy, got %q", string(b2))
	}
}

// TestConcurrentAccess verifies that concurrent operations are safe (no panics or data races).
func TestConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	s := memstore.New()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("k%d", i%5)
			if err := s.Set(ctx, key, []byte("v"), time.Minute); err != nil {
				t.Errorf("Set: unexpected error %v", err)
			}
			// A concurrent Delete on the same key can legitimately race this Get,
			// so only a non-ErrStoreMiss error is unexpected here.
			if _, err := s.Get(ctx, key); err != nil && !errors.Is(err, smartcache.ErrStoreMiss) {
				t.Errorf("Get: unexpected error %v", err)
			}
			if _, err := s.Exists(ctx, key); err != nil {
				t.Errorf("Exists: unexpected error %v", err)
			}
			if err := s.Delete(ctx, key); err != nil {
				t.Errorf("Delete: unexpected error %v", err)
			}
		}(i)
	}
	wg.Wait()
}

// TestGetMany_ReturnsFoundOmitsMissing verifies the batch read returns present
// keys and omits absent ones.
func TestGetMany_ReturnsFoundOmitsMissing(t *testing.T) {
	ctx := context.Background()
	s := memstore.New()
	bs, ok := s.(smartcache.BatchCacheStore)
	if !ok {
		t.Fatal("memstore must implement smartcache.BatchCacheStore")
	}
	if err := s.Set(ctx, "a", []byte("A"), time.Minute); err != nil {
		t.Fatalf("Set a: %v", err)
	}
	if err := s.Set(ctx, "c", []byte("C"), time.Minute); err != nil {
		t.Fatalf("Set c: %v", err)
	}
	got, err := bs.GetMany(ctx, []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("GetMany: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 present, got %d (%v)", len(got), got)
	}
	if string(got["a"]) != "A" || string(got["c"]) != "C" {
		t.Errorf("wrong values: %v", got)
	}
	if _, present := got["b"]; present {
		t.Error("absent key b must be omitted from the result")
	}
}

// prefixStoreFor returns s viewed as a smartcache.PrefixCacheStore, failing the test if it is not one.
func prefixStoreFor(t *testing.T, s smartcache.CacheStore) smartcache.PrefixCacheStore {
	t.Helper()
	pc, ok := s.(smartcache.PrefixCacheStore)
	if !ok {
		t.Fatal("memstore must implement smartcache.PrefixCacheStore")
	}
	return pc
}

// TestDeleteByPrefix_RemovesOnlyMatching verifies that DeleteByPrefix removes exactly the entries
// whose key starts with the literal prefix and reports how many it removed.
func TestDeleteByPrefix_RemovesOnlyMatching(t *testing.T) {
	cases := []struct {
		name      string
		keys      []string
		prefix    string
		wantCount int64
		wantGone  []string
		wantKept  []string
	}{
		{
			name:      "matching subset is removed and counted",
			keys:      []string{"app-1::a", "app-1::b", "app-2::a"},
			prefix:    "app-1::",
			wantCount: 2,
			wantGone:  []string{"app-1::a", "app-1::b"},
			wantKept:  []string{"app-2::a"},
		},
		{
			name:      "empty store returns zero",
			prefix:    "app-1::",
			wantCount: 0,
		},
		{
			name:      "no key matches leaves the store untouched",
			keys:      []string{"k1", "k2"},
			prefix:    "zz",
			wantCount: 0,
			wantKept:  []string{"k1", "k2"},
		},
		{
			name:      "prefix must match at the start, not as a substring",
			keys:      []string{"xapp-1::a"},
			prefix:    "app-1::",
			wantCount: 0,
			wantKept:  []string{"xapp-1::a"},
		},
		{
			name:      "star is literal, not a glob",
			keys:      []string{"a*1", "axb"},
			prefix:    "a*",
			wantCount: 1,
			wantGone:  []string{"a*1"},
			wantKept:  []string{"axb"},
		},
		{
			name:      "question mark is literal, not a glob",
			keys:      []string{"a?1", "axb"},
			prefix:    "a?",
			wantCount: 1,
			wantGone:  []string{"a?1"},
			wantKept:  []string{"axb"},
		},
		{
			name:      "bracket is literal, not a character class",
			keys:      []string{"a[1", "a1"},
			prefix:    "a[",
			wantCount: 1,
			wantGone:  []string{"a[1"},
			wantKept:  []string{"a1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s := memstore.New()
			pc := prefixStoreFor(t, s)
			for _, k := range tc.keys {
				if err := s.Set(ctx, k, []byte("v"), time.Minute); err != nil {
					t.Fatalf("Set %q: %v", k, err)
				}
			}

			got, err := pc.DeleteByPrefix(ctx, tc.prefix)
			if err != nil {
				t.Fatalf("DeleteByPrefix: %v", err)
			}
			if got != tc.wantCount {
				t.Errorf("count: want %d, got %d", tc.wantCount, got)
			}
			for _, k := range tc.wantGone {
				if _, err := s.Get(ctx, k); !errors.Is(err, smartcache.ErrStoreMiss) {
					t.Errorf("key %q must be removed, got err=%v", k, err)
				}
			}
			for _, k := range tc.wantKept {
				if _, err := s.Get(ctx, k); err != nil {
					t.Errorf("key %q must survive, got err=%v", k, err)
				}
			}
		})
	}
}

// TestDeleteByPrefix_ExpiredMatchingEntryNotCounted verifies that an expired entry under the prefix
// is not counted, while a live entry under the prefix is counted and removed.
func TestDeleteByPrefix_ExpiredMatchingEntryNotCounted(t *testing.T) {
	ctx := context.Background()
	s := memstore.New()
	pc := prefixStoreFor(t, s)

	if err := s.Set(ctx, "p:live", []byte("v"), time.Minute); err != nil {
		t.Fatalf("Set live: %v", err)
	}
	if err := s.Set(ctx, "p:dead", []byte("v"), 30*time.Millisecond); err != nil {
		t.Fatalf("Set dead: %v", err)
	}
	time.Sleep(80 * time.Millisecond)

	got, err := pc.DeleteByPrefix(ctx, "p:")
	if err != nil {
		t.Fatalf("DeleteByPrefix: %v", err)
	}
	if got != 1 {
		t.Fatalf("want 1 (expired entry must not be counted), got %d", got)
	}
	if _, err := s.Get(ctx, "p:live"); !errors.Is(err, smartcache.ErrStoreMiss) {
		t.Errorf("live matching key must be removed, got err=%v", err)
	}
}

// TestDeleteByPrefix_ConcurrentWithSet verifies concurrent Set and DeleteByPrefix are safe (no panics
// or data races under -race).
func TestDeleteByPrefix_ConcurrentWithSet(t *testing.T) {
	ctx := context.Background()
	s := memstore.New()
	pc := prefixStoreFor(t, s)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("app-%d::k%d", i%3, i%5)
			if err := s.Set(ctx, key, []byte("v"), time.Minute); err != nil {
				t.Errorf("Set: unexpected error %v", err)
			}
			if _, err := pc.DeleteByPrefix(ctx, "app-1::"); err != nil {
				t.Errorf("DeleteByPrefix: unexpected error %v", err)
			}
		}(i)
	}
	wg.Wait()
}
