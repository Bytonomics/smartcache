package smartcache_test

import (
	"context"
	"testing"
	"time"

	"github.com/Bytonomics/smartcache"
	"github.com/Bytonomics/smartcache/memstore"
)

// TestJitter_DownwardWithinBounds: with jitterRand fixed at 0.5, fraction 0.10,
// base TTL 100s => effective 100 - 0.5*0.10*100 = 95s captured at the Set call.
func TestJitter_DownwardWithinBounds(t *testing.T) {
	restore := smartcache.SetJitterRandForTest(func() float64 { return 0.5 })
	defer restore()

	cs := &captureStore{CacheStore: memstore.New()}
	mgr, err := smartcache.NewManager(cs)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "j", &smartcache.EntityOptions{TTL: ptr(100 * time.Second)})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, _, err := c.GetByKey(context.Background(), "k", func(ctx context.Context) (*sample, error) {
		return &sample{N: 1}, nil
	}); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := cs.lastTTL(); got != 95*time.Second {
		t.Errorf("jittered positive TTL: got %v, want 95s", got)
	}
}

// TestJitter_ZeroFractionDisables: fraction 0 => TTL is the raw base, unchanged.
func TestJitter_ZeroFractionDisables(t *testing.T) {
	restore := smartcache.SetJitterRandForTest(func() float64 { return 0.5 })
	defer restore()

	cs := &captureStore{CacheStore: memstore.New()}
	mgr, err := smartcache.NewManager(cs)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "j", &smartcache.EntityOptions{
		TTL:            ptr(100 * time.Second),
		JitterFraction: ptr(0.0),
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, _, err := c.GetByKey(context.Background(), "k", func(ctx context.Context) (*sample, error) {
		return &sample{N: 1}, nil
	}); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := cs.lastTTL(); got != 100*time.Second {
		t.Errorf("disabled jitter TTL: got %v, want 100s", got)
	}
}

// TestJitter_AppliedToNegativeTTL: negative-marker Set is jittered too. base 200s,
// fraction 0.10, rand 0.5 => 200 - 10 = 190s.
func TestJitter_AppliedToNegativeTTL(t *testing.T) {
	restore := smartcache.SetJitterRandForTest(func() float64 { return 0.5 })
	defer restore()

	cs := &captureStore{CacheStore: memstore.New()}
	mgr, err := smartcache.NewManager(cs)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "j", &smartcache.EntityOptions{
		TTL:         ptr(100 * time.Second),
		NegativeTTL: ptr(200 * time.Second),
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, _, err := c.GetByKey(context.Background(), "missing", func(ctx context.Context) (*sample, error) {
		return nil, smartcache.ErrNotFound
	}); err == nil {
		t.Fatal("Get: expected ErrNotFound")
	}
	if got := cs.lastTTL(); got != 190*time.Second {
		t.Errorf("jittered negative TTL: got %v, want 190s", got)
	}
}

// TestJitter_SkippedWhenAllowInfinite: AllowInfinite + TTL 0 => positive Set TTL
// is 0 (no expiry), jitter never applied.
func TestJitter_SkippedWhenAllowInfinite(t *testing.T) {
	restore := smartcache.SetJitterRandForTest(func() float64 { return 0.5 })
	defer restore()

	cs := &captureStore{CacheStore: memstore.New()}
	mgr, err := smartcache.NewManager(cs)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "j", &smartcache.EntityOptions{
		TTL:           ptr(time.Duration(0)),
		AllowInfinite: ptr(true),
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, _, err := c.GetByKey(context.Background(), "k", func(ctx context.Context) (*sample, error) {
		return &sample{N: 1}, nil
	}); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := cs.lastTTL(); got != 0 {
		t.Errorf("AllowInfinite positive TTL: got %v, want 0", got)
	}
}
