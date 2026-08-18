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
