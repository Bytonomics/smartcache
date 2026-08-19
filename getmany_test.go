package smartcache_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Bytonomics/smartcache"
	"github.com/Bytonomics/smartcache/memstore"
)

// failGetManyStore wraps a CacheStore AND implements BatchCacheStore, but its
// GetMany always fails — used to verify a batch-read error is treated as a
// total cache miss (never fails the overall GetMany call).
type failGetManyStore struct {
	smartcache.CacheStore
}

func (f *failGetManyStore) GetMany(ctx context.Context, keys []string) (map[string][]byte, error) {
	return nil, errors.New("batch read failed")
}

var _ smartcache.BatchCacheStore = (*failGetManyStore)(nil)

// TestGetMany_AllHits_NoLoad verifies that when every key is already cached,
// loadMissing is never called.
func TestGetMany_AllHits_NoLoad(t *testing.T) {
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "gm-all-hits", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx := context.Background()
	if err := c.PutValueByKey(ctx, "a", &sample{N: 1}); err != nil {
		t.Fatalf("PutValue a failed: %v", err)
	}
	if err := c.PutValueByKey(ctx, "b", &sample{N: 2}); err != nil {
		t.Fatalf("PutValue b failed: %v", err)
	}

	loadMissing := func(ctx context.Context, missing []string) (map[string]*sample, error) {
		t.Fatalf("loadMissing should not be called, got missing=%v", missing)
		return nil, nil
	}

	out, err := c.GetManyByKey(ctx, []string{"a", "b"}, loadMissing)
	if err != nil {
		t.Fatalf("GetMany failed: %v", err)
	}
	if out["a"] == nil || out["a"].N != 1 {
		t.Errorf("a: got %v, want N=1", out["a"])
	}
	if out["b"] == nil || out["b"].N != 2 {
		t.Errorf("b: got %v, want N=2", out["b"])
	}
}

// TestGetMany_PartialMiss_OneBatchedLoad verifies a mix of hits and misses
// results in exactly one loadMissing call carrying only the missing keys, and
// that the loaded values are then cached for a subsequent call.
func TestGetMany_PartialMiss_OneBatchedLoad(t *testing.T) {
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "gm-partial-miss", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx := context.Background()
	if err := c.PutValueByKey(ctx, "a", &sample{N: 1}); err != nil {
		t.Fatalf("PutValue a failed: %v", err)
	}

	var loadCalls int
	var gotMissing []string
	loadMissing := func(ctx context.Context, missing []string) (map[string]*sample, error) {
		loadCalls++
		gotMissing = append([]string{}, missing...)
		return map[string]*sample{"b": {N: 2}, "c": {N: 3}}, nil
	}

	out, err := c.GetManyByKey(ctx, []string{"a", "b", "c"}, loadMissing)
	if err != nil {
		t.Fatalf("GetMany failed: %v", err)
	}
	if loadCalls != 1 {
		t.Errorf("loadMissing calls: got %d, want 1", loadCalls)
	}
	if len(gotMissing) != 2 {
		t.Errorf("missing passed to loadMissing: got %v, want 2 entries", gotMissing)
	}
	if out["a"] == nil || out["a"].N != 1 {
		t.Errorf("a (from cache): got %v, want N=1", out["a"])
	}
	if out["b"] == nil || out["b"].N != 2 {
		t.Errorf("b (from load): got %v, want N=2", out["b"])
	}
	if out["c"] == nil || out["c"].N != 3 {
		t.Errorf("c (from load): got %v, want N=3", out["c"])
	}

	loadMissing2 := func(ctx context.Context, missing []string) (map[string]*sample, error) {
		t.Fatalf("loadMissing should not be called on second GetMany, got missing=%v", missing)
		return nil, nil
	}
	out2, err := c.GetManyByKey(ctx, []string{"a", "b", "c"}, loadMissing2)
	if err != nil {
		t.Fatalf("second GetMany failed: %v", err)
	}
	if out2["b"] == nil || out2["b"].N != 2 {
		t.Errorf("b (second call, from cache): got %v, want N=2", out2["b"])
	}
}

// TestGetMany_NegativeCachesNotFound verifies an id absent from loadMissing's
// result is negative-cached (when NegativeTTL > 0), omitted from the returned
// map, and served as a warm negative hit on the next call.
func TestGetMany_NegativeCachesNotFound(t *testing.T) {
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "gm-negative", &smartcache.EntityOptions{TTL: ptr(time.Minute), NegativeTTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx := context.Background()
	var loadCalls int
	loadMissing := func(ctx context.Context, missing []string) (map[string]*sample, error) {
		loadCalls++
		return map[string]*sample{}, nil
	}

	out, err := c.GetManyByKey(ctx, []string{"missing-id"}, loadMissing)
	if err != nil {
		t.Fatalf("GetMany failed: %v", err)
	}
	if _, ok := out["missing-id"]; ok {
		t.Errorf("missing-id: expected absent from result map, got %v", out["missing-id"])
	}
	if loadCalls != 1 {
		t.Errorf("loadMissing calls: got %d, want 1", loadCalls)
	}

	loadMissing2 := func(ctx context.Context, missing []string) (map[string]*sample, error) {
		t.Fatalf("loadMissing should not be called; missing-id should be a warm negative hit, got missing=%v", missing)
		return nil, nil
	}
	out2, err := c.GetManyByKey(ctx, []string{"missing-id"}, loadMissing2)
	if err != nil {
		t.Fatalf("second GetMany failed: %v", err)
	}
	if _, ok := out2["missing-id"]; ok {
		t.Errorf("missing-id (second call): expected absent, got %v", out2["missing-id"])
	}
}

// TestGetMany_NFallback_WhenNotBatchStore verifies that when the injected store
// does not implement BatchCacheStore, GetMany reads each key with an individual
// Get call.
func TestGetMany_NFallback_WhenNotBatchStore(t *testing.T) {
	store := &countingStore{CacheStore: memstore.New()}
	mgr, err := smartcache.NewManager(store)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "gm-fallback", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx := context.Background()
	if err := c.PutValueByKey(ctx, "a", &sample{N: 1}); err != nil {
		t.Fatalf("PutValue failed: %v", err)
	}

	loadMissing := func(ctx context.Context, missing []string) (map[string]*sample, error) {
		return map[string]*sample{}, nil
	}
	if _, err := c.GetManyByKey(ctx, []string{"a", "b"}, loadMissing); err != nil {
		t.Fatalf("GetMany failed: %v", err)
	}

	if store.getCalls != 2 {
		t.Errorf("Get calls via N-Get fallback: got %d, want 2 (one per key; countingStore is not a BatchCacheStore)", store.getCalls)
	}
}

// TestGetMany_BatchReadError_TreatedAsMiss verifies a BatchCacheStore.GetMany
// error is treated as a total miss (every key is loaded), never failing the
// overall GetMany call.
func TestGetMany_BatchReadError_TreatedAsMiss(t *testing.T) {
	store := &failGetManyStore{CacheStore: memstore.New()}
	mgr, err := smartcache.NewManager(store)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "gm-batch-err", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx := context.Background()
	var loadCalls int
	loadMissing := func(ctx context.Context, missing []string) (map[string]*sample, error) {
		loadCalls++
		out := make(map[string]*sample, len(missing))
		for _, k := range missing {
			out[k] = &sample{N: 1}
		}
		return out, nil
	}

	out, err := c.GetManyByKey(ctx, []string{"a", "b"}, loadMissing)
	if err != nil {
		t.Fatalf("GetMany should not fail on a batch-read error: %v", err)
	}
	if loadCalls != 1 {
		t.Errorf("loadMissing calls: got %d, want 1 (batch-read error must be treated as a total miss)", loadCalls)
	}
	if out["a"] == nil || out["b"] == nil {
		t.Errorf("expected both keys loaded despite the batch-read error, got %v", out)
	}
}

// TestGetMany_LoadError_Propagates verifies loadMissing's error is wrapped and
// returned, with a nil result map.
func TestGetMany_LoadError_Propagates(t *testing.T) {
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "gm-load-err", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx := context.Background()
	loadErr := errors.New("db down")
	loadMissing := func(ctx context.Context, missing []string) (map[string]*sample, error) {
		return nil, loadErr
	}

	out, err := c.GetManyByKey(ctx, []string{"a"}, loadMissing)
	if !errors.Is(err, loadErr) {
		t.Errorf("GetMany error: got %v, want wrapping %v", err, loadErr)
	}
	if out != nil {
		t.Errorf("GetMany result: got %v, want nil", out)
	}
}

// TestGetMany_CorruptEntry_Reloads verifies an unmarshalable cached entry is
// treated as a miss and reloaded.
func TestGetMany_CorruptEntry_Reloads(t *testing.T) {
	backing := memstore.New()
	mgr, err := smartcache.NewManager(backing)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "gm-corrupt", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx := context.Background()
	if err := backing.Set(ctx, "bc:gm-corrupt:a", []byte("not valid json{{{"), time.Minute); err != nil {
		t.Fatalf("backing.Set failed: %v", err)
	}

	var loadCalls int
	loadMissing := func(ctx context.Context, missing []string) (map[string]*sample, error) {
		loadCalls++
		return map[string]*sample{"a": {N: 42}}, nil
	}

	out, err := c.GetManyByKey(ctx, []string{"a"}, loadMissing)
	if err != nil {
		t.Fatalf("GetMany failed: %v", err)
	}
	if loadCalls != 1 {
		t.Errorf("loadMissing calls: got %d, want 1 (corrupt entry should be treated as a miss)", loadCalls)
	}
	if out["a"] == nil || out["a"].N != 42 {
		t.Errorf("a: got %v, want N=42 (reloaded)", out["a"])
	}
}

// TestGetMany_Empty verifies an empty key slice short-circuits without calling
// loadMissing.
func TestGetMany_Empty(t *testing.T) {
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "gm-empty", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx := context.Background()
	loadMissing := func(ctx context.Context, missing []string) (map[string]*sample, error) {
		t.Fatalf("loadMissing should not be called for empty keys, got missing=%v", missing)
		return nil, nil
	}

	out, err := c.GetManyByKey(ctx, nil, loadMissing)
	if err != nil {
		t.Fatalf("GetMany(nil) failed: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("GetMany(nil) result: got %v, want empty map", out)
	}
}
