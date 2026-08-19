package smartcache_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Bytonomics/smartcache"
	"github.com/Bytonomics/smartcache/memstore"
)

// TestGet_ReadThrough_MissThenHit verifies cache miss followed by cache hit behavior.
func TestGet_ReadThrough_MissThenHit(t *testing.T) {
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "read-through", &smartcache.EntityOptions{Prefix: ptr("p"), TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	var calls int
	loader := func(ctx context.Context) (*sample, error) {
		calls++
		return &sample{N: 7}, nil
	}

	ctx := context.Background()

	// First Get: cache miss, loader called
	val1, outcome1, err1 := c.GetByKey(ctx, "k", loader)
	if err1 != nil {
		t.Fatalf("First Get failed: %v", err1)
	}
	if val1 == nil || val1.N != 7 {
		t.Errorf("First Get value: got %v, want N=7", val1)
	}
	if outcome1 != smartcache.Loaded {
		t.Errorf("First Get outcome: got %v, want Loaded", outcome1)
	}
	if calls != 1 {
		t.Errorf("First Get calls: got %d, want 1", calls)
	}

	// Second Get: cache hit, loader not called
	val2, outcome2, err2 := c.GetByKey(ctx, "k", loader)
	if err2 != nil {
		t.Fatalf("Second Get failed: %v", err2)
	}
	if val2 == nil || val2.N != 7 {
		t.Errorf("Second Get value: got %v, want N=7", val2)
	}
	if outcome2 != smartcache.Hit {
		t.Errorf("Second Get outcome: got %v, want Hit", outcome2)
	}
	if calls != 1 {
		t.Errorf("Second Get calls: got %d, want 1", calls)
	}
}

// TestEvict_ReloadsOnNextGet verifies eviction forces reload.
func TestEvict_ReloadsOnNextGet(t *testing.T) {
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "evict-reload", &smartcache.EntityOptions{Prefix: ptr("p"), TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	var calls int
	loader := func(ctx context.Context) (*sample, error) {
		calls++
		return &sample{N: 7}, nil
	}

	ctx := context.Background()

	// First Get: cache miss
	_, outcome1, err1 := c.GetByKey(ctx, "k", loader)
	if err1 != nil {
		t.Fatalf("First Get failed: %v", err1)
	}
	if outcome1 != smartcache.Loaded {
		t.Errorf("First Get outcome: got %v, want Loaded", outcome1)
	}

	// Second Get: cache hit
	_, outcome2, err2 := c.GetByKey(ctx, "k", loader)
	if err2 != nil {
		t.Fatalf("Second Get failed: %v", err2)
	}
	if outcome2 != smartcache.Hit {
		t.Errorf("Second Get outcome: got %v, want Hit", outcome2)
	}
	if calls != 1 {
		t.Errorf("After two Gets, calls: got %d, want 1", calls)
	}

	// Evict the key
	err = c.EvictByKey(ctx, "k")
	if err != nil {
		t.Fatalf("Evict failed: %v", err)
	}

	// Third Get: cache miss again
	_, outcome3, err3 := c.GetByKey(ctx, "k", loader)
	if err3 != nil {
		t.Fatalf("Third Get failed: %v", err3)
	}
	if outcome3 != smartcache.Loaded {
		t.Errorf("Third Get outcome: got %v, want Loaded", outcome3)
	}
	if calls != 2 {
		t.Errorf("After Evict and third Get, calls: got %d, want 2", calls)
	}
}

// TestNegativeCaching_Enabled verifies negative caching when enabled.
func TestNegativeCaching_Enabled(t *testing.T) {
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "neg-on", &smartcache.EntityOptions{
		TTL:         ptr(time.Minute),
		NegativeTTL: ptr(time.Minute),
	})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	var calls int
	loader := func(ctx context.Context) (*sample, error) {
		calls++
		return nil, smartcache.ErrNotFound
	}

	ctx := context.Background()

	// First Get: smartcache.ErrNotFound, cached as negative
	val1, outcome1, err1 := c.GetByKey(ctx, "k", loader)
	if val1 != nil {
		t.Errorf("First Get value: got %v, want nil", val1)
	}
	if !errors.Is(err1, smartcache.ErrNotFound) {
		t.Errorf("First Get error: got %v, want smartcache.ErrNotFound", err1)
	}
	if outcome1 != smartcache.Loaded {
		t.Errorf("First Get outcome: got %v, want Loaded", outcome1)
	}
	if calls != 1 {
		t.Errorf("First Get calls: got %d, want 1", calls)
	}

	// Second Get: negative cache hit
	_, outcome2, err2 := c.GetByKey(ctx, "k", loader)
	if !errors.Is(err2, smartcache.ErrNotFound) {
		t.Errorf("Second Get error: got %v, want smartcache.ErrNotFound", err2)
	}
	if outcome2 != smartcache.NegativeHit {
		t.Errorf("Second Get outcome: got %v, want NegativeHit", outcome2)
	}
	if calls != 1 {
		t.Errorf("Second Get calls: got %d, want 1 (no reload)", calls)
	}
}

// TestNegativeCaching_Disabled verifies smartcache.ErrNotFound is not cached when NegativeTTL is 0.
func TestNegativeCaching_Disabled(t *testing.T) {
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "neg-off", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	var calls int
	loader := func(ctx context.Context) (*sample, error) {
		calls++
		return nil, smartcache.ErrNotFound
	}

	ctx := context.Background()

	// First Get
	_, outcome1, err1 := c.GetByKey(ctx, "k", loader)
	if !errors.Is(err1, smartcache.ErrNotFound) {
		t.Errorf("First Get error: got %v, want smartcache.ErrNotFound", err1)
	}
	if outcome1 != smartcache.Loaded {
		t.Errorf("First Get outcome: got %v, want Loaded", outcome1)
	}

	// Second Get: should call loader again
	_, outcome2, err2 := c.GetByKey(ctx, "k", loader)
	if !errors.Is(err2, smartcache.ErrNotFound) {
		t.Errorf("Second Get error: got %v, want smartcache.ErrNotFound", err2)
	}
	if outcome2 != smartcache.Loaded {
		t.Errorf("Second Get outcome: got %v, want Loaded", outcome2)
	}
	if calls != 2 {
		t.Errorf("After two Gets, calls: got %d, want 2", calls)
	}
}

// TestGet_PopulateFailure_NonFatal verifies non-fatal store failure on populate.
func TestGet_PopulateFailure_NonFatal(t *testing.T) {
	store := failSetStore{memstore.New()}
	mgr, err := smartcache.NewManager(store)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "populate-fail", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	loader := func(ctx context.Context) (*sample, error) {
		return &sample{N: 3}, nil
	}

	ctx := context.Background()
	val, outcome, err := c.GetByKey(ctx, "k", loader)

	if err != nil {
		t.Errorf("Get with store Set failure: got error %v, want nil", err)
	}
	if val == nil || val.N != 3 {
		t.Errorf("Get value: got %v, want N=3", val)
	}
	if outcome != smartcache.LoadedNotCached {
		t.Errorf("Get outcome: got %v, want LoadedNotCached", outcome)
	}
}

// TestEvict_Failure_Surfaced verifies Evict error is returned.
func TestEvict_Failure_Surfaced(t *testing.T) {
	store := failDeleteStore{memstore.New()}
	mgr, err := smartcache.NewManager(store)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "evict-fail", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx := context.Background()
	if err := c.EvictByKey(ctx, "k"); err == nil {
		t.Fatal("Evict with store Delete failure: expected error, got nil")
	}
}

// TestEvictMany_JoinsErrors verifies EvictMany returns error on Delete failure.
func TestEvictMany_JoinsErrors(t *testing.T) {
	store := failDeleteStore{memstore.New()}
	mgr, err := smartcache.NewManager(store)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "evictmany-fail", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx := context.Background()
	if err := c.EvictManyByKey(ctx, "a", "b"); err == nil {
		t.Fatal("EvictMany with store Delete failure: expected error, got nil")
	}
}

// TestPutValue_ThenHit verifies PutValue populates cache and is served on next Get.
func TestPutValue_ThenHit(t *testing.T) {
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "putvalue", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx := context.Background()

	// PutValue a value directly
	err = c.PutValueByKey(ctx, "k", &sample{N: 9})
	if err != nil {
		t.Fatalf("PutValue failed: %v", err)
	}

	// Get with a loader that should not be called
	loader := func(ctx context.Context) (*sample, error) {
		panic("loader should not be called")
	}

	val, outcome, err := c.GetByKey(ctx, "k", loader)
	if err != nil {
		t.Fatalf("Get after PutValue failed: %v", err)
	}
	if val == nil || val.N != 9 {
		t.Errorf("Get value: got %v, want N=9", val)
	}
	if outcome != smartcache.Hit {
		t.Errorf("Get outcome: got %v, want Hit", outcome)
	}
}

// TestPut_WriterSuccess_ThenHit verifies Put runs writer, caches its result,
// and the value is served from cache on the next Get.
func TestPut_WriterSuccess_ThenHit(t *testing.T) {
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "put-success", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx := context.Background()
	var writerCalls int
	writer := func(ctx context.Context) (*sample, error) {
		writerCalls++
		return &sample{N: 11}, nil
	}

	val, outcome, err := c.PutByKey(ctx, "k", writer)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if val == nil || val.N != 11 {
		t.Errorf("Put value: got %v, want N=11", val)
	}
	if outcome != smartcache.Written {
		t.Errorf("Put outcome: got %v, want Written", outcome)
	}
	if writerCalls != 1 {
		t.Errorf("writer calls: got %d, want 1", writerCalls)
	}

	loader := func(ctx context.Context) (*sample, error) {
		panic("loader should not be called")
	}
	val2, outcome2, err2 := c.GetByKey(ctx, "k", loader)
	if err2 != nil {
		t.Fatalf("Get after Put failed: %v", err2)
	}
	if val2 == nil || val2.N != 11 {
		t.Errorf("Get value: got %v, want N=11", val2)
	}
	if outcome2 != smartcache.Hit {
		t.Errorf("Get outcome: got %v, want Hit", outcome2)
	}
}

// TestPut_WriterError_PropagatedNotCached verifies a writer error is returned
// unchanged and the cache is left untouched.
func TestPut_WriterError_PropagatedNotCached(t *testing.T) {
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "put-writer-err", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx := context.Background()
	writerErr := errors.New("write to source of truth failed")
	writer := func(ctx context.Context) (*sample, error) {
		return nil, writerErr
	}

	val, outcome, err := c.PutByKey(ctx, "k", writer)
	if val != nil {
		t.Errorf("Put value: got %v, want nil", val)
	}
	if !errors.Is(err, writerErr) {
		t.Errorf("Put error: got %v, want %v", err, writerErr)
	}
	if outcome != smartcache.Written {
		t.Errorf("Put outcome: got %v, want Written", outcome)
	}

	// Confirm nothing was cached: Get must call the loader.
	var loaderCalls int
	loader := func(ctx context.Context) (*sample, error) {
		loaderCalls++
		return &sample{N: 1}, nil
	}
	if _, _, err := c.GetByKey(ctx, "k", loader); err != nil {
		t.Fatalf("Get after failed Put failed: %v", err)
	}
	if loaderCalls != 1 {
		t.Errorf("loader calls after failed Put: got %d, want 1 (nothing should have been cached)", loaderCalls)
	}
}

// TestPut_WriterNilValue_ErrNilWrite verifies a writer returning (nil, nil)
// is rejected with ErrNilWrite and the cache is left untouched.
func TestPut_WriterNilValue_ErrNilWrite(t *testing.T) {
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "put-nil-write", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx := context.Background()
	writer := func(ctx context.Context) (*sample, error) {
		return nil, nil
	}

	val, outcome, err := c.PutByKey(ctx, "k", writer)
	if val != nil {
		t.Errorf("Put value: got %v, want nil", val)
	}
	if !errors.Is(err, smartcache.ErrNilWrite) {
		t.Errorf("Put error: got %v, want smartcache.ErrNilWrite", err)
	}
	if outcome != smartcache.Written {
		t.Errorf("Put outcome: got %v, want Written", outcome)
	}

	var loaderCalls int
	loader := func(ctx context.Context) (*sample, error) {
		loaderCalls++
		return &sample{N: 1}, nil
	}
	if _, _, err := c.GetByKey(ctx, "k", loader); err != nil {
		t.Fatalf("Get after nil-write Put failed: %v", err)
	}
	if loaderCalls != 1 {
		t.Errorf("loader calls after nil-write Put: got %d, want 1 (nothing should have been cached)", loaderCalls)
	}
}

// TestPut_PopulateFailure_NonFatal verifies a cache-side Set failure after a
// successful writer still returns the written value, with Outcome ==
// WrittenNotCached, and never fails the call.
func TestPut_PopulateFailure_NonFatal(t *testing.T) {
	store := failSetStore{memstore.New()}
	mgr, err := smartcache.NewManager(store)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "put-populate-fail", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx := context.Background()
	writer := func(ctx context.Context) (*sample, error) {
		return &sample{N: 5}, nil
	}

	val, outcome, err := c.PutByKey(ctx, "k", writer)
	if err != nil {
		t.Errorf("Put with store Set failure: got error %v, want nil", err)
	}
	if val == nil || val.N != 5 {
		t.Errorf("Put value: got %v, want N=5", val)
	}
	if outcome != smartcache.WrittenNotCached {
		t.Errorf("Put outcome: got %v, want WrittenNotCached", outcome)
	}
}

// TestPut_ConcurrentCalls_NotDeduped verifies Put never uses singleflight:
// two concurrent Put calls for the same key both run their own writer, unlike
// Get's loader deduplication.
func TestPut_ConcurrentCalls_NotDeduped(t *testing.T) {
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "put-not-deduped", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx := context.Background()
	var writerCalls int64
	writer := func(ctx context.Context) (*sample, error) {
		time.Sleep(20 * time.Millisecond)
		n := atomic.AddInt64(&writerCalls, 1)
		return &sample{N: int(n)}, nil
	}

	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, _, putErr := c.PutByKey(ctx, "same", writer); putErr != nil {
				t.Errorf("concurrent Put failed: %v", putErr)
			}
		})
	}
	wg.Wait()

	callCount := atomic.LoadInt64(&writerCalls)
	if callCount != 20 {
		t.Errorf("writer calls after 20 concurrent Puts: got %d, want 20 (Put must not dedupe via singleflight)", callCount)
	}
}

// TestSingleflight_DedupsColdLoads verifies concurrent Gets deduplicate loader calls.
func TestSingleflight_DedupsColdLoads(t *testing.T) {
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "dedup-cold", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	var calls int64
	loader := func(ctx context.Context) (*sample, error) {
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt64(&calls, 1)
		return &sample{N: 1}, nil
	}

	ctx := context.Background()

	// Launch 20 concurrent Gets on the same key
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, _, getErr := c.GetByKey(ctx, "same", loader); getErr != nil {
				t.Errorf("concurrent Get failed: %v", getErr)
			}
		})
	}
	wg.Wait()

	// Verify loader was called only once
	callCount := atomic.LoadInt64(&calls)
	if callCount != 1 {
		t.Errorf("Loader calls after 20 concurrent Gets: got %d, want 1", callCount)
	}
}

// TestSingleflight_PopulatesOnce verifies that under 20 concurrent cold Gets
// on the same key, the cache is populated exactly once — not once per
// waiter. Regression test: populate used to run after the singleflight
// closure returned, so every waiter redundantly re-populated the cache.
func TestSingleflight_PopulatesOnce(t *testing.T) {
	store := &captureStore{CacheStore: memstore.New()}
	mgr, err := smartcache.NewManager(store)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "populate-once", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	loader := func(ctx context.Context) (*sample, error) {
		time.Sleep(20 * time.Millisecond)
		return &sample{N: 1}, nil
	}

	ctx := context.Background()
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, _, getErr := c.GetByKey(ctx, "same", loader); getErr != nil {
				t.Errorf("concurrent Get failed: %v", getErr)
			}
		})
	}
	wg.Wait()

	if got := len(store.setTTLs); got != 1 {
		t.Errorf("Set calls after 20 concurrent cold Gets: got %d, want 1 (populate must run once, not once per waiter)", got)
	}
}

// TestSingleflight_PopulatesNegativeMarkerOnce verifies the same for the
// negative-cache path: 20 concurrent Gets for a missing key write the
// "not found" marker exactly once.
func TestSingleflight_PopulatesNegativeMarkerOnce(t *testing.T) {
	store := &captureStore{CacheStore: memstore.New()}
	mgr, err := smartcache.NewManager(store)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "populate-neg-once", &smartcache.EntityOptions{TTL: ptr(time.Minute), NegativeTTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	loader := func(ctx context.Context) (*sample, error) {
		time.Sleep(20 * time.Millisecond)
		return nil, smartcache.ErrNotFound
	}

	ctx := context.Background()
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, _, getErr := c.GetByKey(ctx, "missing", loader); !errors.Is(getErr, smartcache.ErrNotFound) {
				t.Errorf("concurrent Get error: got %v, want smartcache.ErrNotFound", getErr)
			}
		})
	}
	wg.Wait()

	if got := len(store.setTTLs); got != 1 {
		t.Errorf("Set calls after 20 concurrent cold Gets for a missing key: got %d, want 1", got)
	}
}

// TestGet_TransientLoaderError_NotCached verifies transient errors are not cached.
func TestGet_TransientLoaderError_NotCached(t *testing.T) {
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "transient-err", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	var calls int
	loader := func(ctx context.Context) (*sample, error) {
		calls++
		return nil, errors.New("db down")
	}

	ctx := context.Background()

	// First Get: transient error
	val1, outcome1, err1 := c.GetByKey(ctx, "k", loader)
	if val1 != nil {
		t.Errorf("First Get value: got %v, want nil", val1)
	}
	if err1 == nil {
		t.Fatal("First Get error: got nil, want error")
	}
	if errors.Is(err1, smartcache.ErrNotFound) {
		t.Errorf("First Get error should not be smartcache.ErrNotFound: got %v", err1)
	}
	if outcome1 != smartcache.Loaded {
		t.Errorf("First Get outcome: got %v, want Loaded", outcome1)
	}
	if calls != 1 {
		t.Errorf("First Get calls: got %d, want 1", calls)
	}

	// Second Get: should call loader again (not cached)
	val2, _, err2 := c.GetByKey(ctx, "k", loader)
	if val2 != nil {
		t.Errorf("Second Get value: got %v, want nil", val2)
	}
	if err2 == nil {
		t.Fatal("Second Get error: got nil, want error")
	}
	if calls != 2 {
		t.Errorf("Second Get calls: got %d, want 2", calls)
	}
}

// uniqueKeyedSample is a test type implementing UniqueKeyed.
type uniqueKeyedSample struct {
	ID string
	N  int
}

func (s uniqueKeyedSample) CacheUniqueKey() string { return s.ID }

// testUniqueKeyedPutValueThenGetByKey verifies PutValue(ctx, val) caches a UniqueKeyed
// value under its derived key, readable back via GetByKey.
func testUniqueKeyedPutValueThenGetByKey(t *testing.T) {
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[uniqueKeyedSample](mgr, "unique-putvalue", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx := context.Background()
	val := &uniqueKeyedSample{ID: "key1", N: 42}

	if err := c.PutValue(ctx, val); err != nil {
		t.Fatalf("PutValue failed: %v", err)
	}

	loader := func(ctx context.Context) (*uniqueKeyedSample, error) {
		panic("loader should not be called")
	}
	got, outcome, err := c.GetByKey(ctx, val.CacheUniqueKey(), loader)
	if err != nil {
		t.Fatalf("GetByKey failed: %v", err)
	}
	if outcome != smartcache.Hit {
		t.Errorf("GetByKey outcome: got %v, want Hit", outcome)
	}
	if got == nil || got.N != 42 || got.ID != "key1" {
		t.Errorf("GetByKey value: got %+v, want {ID: key1, N: 42}", got)
	}
}

// testUniqueKeyedPutWriterSuccess verifies Put(ctx, writer) runs writer and caches the
// result under its derived key, readable back via GetByKey.
func testUniqueKeyedPutWriterSuccess(t *testing.T) {
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[uniqueKeyedSample](mgr, "unique-put", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx := context.Background()
	writer := func(ctx context.Context) (*uniqueKeyedSample, error) {
		return &uniqueKeyedSample{ID: "key2", N: 99}, nil
	}

	val, outcome, err := c.Put(ctx, writer)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if outcome != smartcache.Written {
		t.Errorf("Put outcome: got %v, want Written", outcome)
	}
	if val == nil || val.N != 99 || val.ID != "key2" {
		t.Errorf("Put value: got %+v, want {ID: key2, N: 99}", val)
	}

	loader := func(ctx context.Context) (*uniqueKeyedSample, error) {
		panic("loader should not be called")
	}
	got, outcome, err := c.GetByKey(ctx, val.CacheUniqueKey(), loader)
	if err != nil {
		t.Fatalf("GetByKey after Put failed: %v", err)
	}
	if outcome != smartcache.Hit {
		t.Errorf("GetByKey outcome: got %v, want Hit", outcome)
	}
	if got == nil || got.N != 99 || got.ID != "key2" {
		t.Errorf("GetByKey value: got %+v, want {ID: key2, N: 99}", got)
	}
}

// testUniqueKeyedEvictRemovesEntry verifies Evict(ctx, val) removes the entry cached
// under val's derived key.
func testUniqueKeyedEvictRemovesEntry(t *testing.T) {
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[uniqueKeyedSample](mgr, "unique-evict", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx := context.Background()
	val := &uniqueKeyedSample{ID: "key3", N: 77}

	if err := c.PutValue(ctx, val); err != nil {
		t.Fatalf("PutValue failed: %v", err)
	}
	if err := c.Evict(ctx, val); err != nil {
		t.Fatalf("Evict failed: %v", err)
	}

	var loaderCalled bool
	loader := func(ctx context.Context) (*uniqueKeyedSample, error) {
		loaderCalled = true
		return nil, smartcache.ErrNotFound
	}
	_, _, err = c.GetByKey(ctx, val.CacheUniqueKey(), loader)
	if !loaderCalled {
		t.Errorf("Evict did not remove entry: loader was not called")
	}
	if !errors.Is(err, smartcache.ErrNotFound) {
		t.Errorf("GetByKey after Evict: got %v, want ErrNotFound", err)
	}
}

// testNotUniqueKeyedPutValueReturnsError verifies PutValue(ctx, val) returns
// ErrNotUniqueKeyed when T does not implement UniqueKeyed.
func testNotUniqueKeyedPutValueReturnsError(t *testing.T) {
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "not-unique", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx := context.Background()
	val := &sample{N: 42}

	if err := c.PutValue(ctx, val); !errors.Is(err, smartcache.ErrNotUniqueKeyed) {
		t.Errorf("PutValue: got %v, want ErrNotUniqueKeyed", err)
	}
}

// testNotUniqueKeyedPutReturnsError verifies Put(ctx, writer) returns ErrNotUniqueKeyed
// when T does not implement UniqueKeyed.
func testNotUniqueKeyedPutReturnsError(t *testing.T) {
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "not-unique-put", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx := context.Background()
	writer := func(ctx context.Context) (*sample, error) {
		return &sample{N: 42}, nil
	}

	val, outcome, err := c.Put(ctx, writer)
	if !errors.Is(err, smartcache.ErrNotUniqueKeyed) {
		t.Errorf("Put: got %v, want ErrNotUniqueKeyed", err)
	}
	if val != nil {
		t.Errorf("Put value: got %v, want nil on error", val)
	}
	if outcome != smartcache.Written {
		t.Errorf("Put outcome: got %v, want Written (attempt before key derivation)", outcome)
	}
}

// testNotUniqueKeyedEvictReturnsError verifies Evict(ctx, val) returns ErrNotUniqueKeyed
// when T does not implement UniqueKeyed.
func testNotUniqueKeyedEvictReturnsError(t *testing.T) {
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "not-unique-evict", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx := context.Background()
	val := &sample{N: 42}

	if err := c.Evict(ctx, val); !errors.Is(err, smartcache.ErrNotUniqueKeyed) {
		t.Errorf("Evict: got %v, want ErrNotUniqueKeyed", err)
	}
}

// TestValueDerivedMethods_PutAndEvict verifies the new value-derived methods:
// Put(ctx, writer), PutValue(ctx, val), and Evict(ctx, val) work correctly
// for types implementing UniqueKeyed, and return ErrNotUniqueKeyed for types
// that do not implement UniqueKeyed.
func TestValueDerivedMethods_PutAndEvict(t *testing.T) {
	tests := []struct {
		name string
		fn   func(t *testing.T)
	}{
		{name: "UniqueKeyed_PutValue_ThenGetByKey", fn: testUniqueKeyedPutValueThenGetByKey},
		{name: "UniqueKeyed_Put_WriterSuccess", fn: testUniqueKeyedPutWriterSuccess},
		{name: "UniqueKeyed_Evict_RemovesEntry", fn: testUniqueKeyedEvictRemovesEntry},
		{name: "NotUniqueKeyed_PutValue_ReturnsError", fn: testNotUniqueKeyedPutValueReturnsError},
		{name: "NotUniqueKeyed_Put_ReturnsError", fn: testNotUniqueKeyedPutReturnsError},
		{name: "NotUniqueKeyed_Evict_ReturnsError", fn: testNotUniqueKeyedEvictReturnsError},
	}

	for i := range tests {
		tc := &tests[i]
		t.Run(tc.name, func(t *testing.T) {
			tc.fn(t)
		})
	}
}

// TestRegister_PanicsOnPointerType verifies T being a pointer type panics: it is a
// programming error (the wrong generic instantiation at the call site),
// caught at construction rather than returned as a runtime error.
func TestRegister_PanicsOnPointerType(t *testing.T) {
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("Register[*sample]: expected a panic, got none")
		}
		err, ok := r.(error)
		if !ok {
			t.Fatalf("panic value is not an error: %v (%T)", r, r)
		}
		if !errors.Is(err, smartcache.ErrPointerType) {
			t.Errorf("panic error: got %v, want smartcache.ErrPointerType", err)
		}
		if !strings.Contains(err.Error(), "sample") {
			t.Errorf("panic error does not name the offending type: %v", err)
		}
	}()
	c, err := smartcache.Register[*sample](mgr, "ptr-panic", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register[*sample] returned an error instead of panicking: %v", err)
	}
	if c != nil {
		t.Fatalf("Register[*sample] returned a non-nil Cache instead of panicking: %+v", c)
	}
}
