package smartcache_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Bytonomics/smartcache"
	"github.com/Bytonomics/smartcache/memstore"
)

// TestMetrics_Get_HitAndLoaded verifies a cold miss increments "loaded" (plus
// one load_latency sample) and a subsequent hit increments "hit", with no
// double counting.
func TestMetrics_Get_HitAndLoaded(t *testing.T) {
	reader, metricsOpt := newTestMeter()
	mgr, err := smartcache.NewManager(memstore.New(), metricsOpt)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "m-hitloaded", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx := context.Background()
	loader := func(ctx context.Context) (*sample, error) { return &sample{N: 1}, nil }

	if _, _, err := c.Get(ctx, "k", loader); err != nil {
		t.Fatalf("first Get failed: %v", err)
	}
	if _, _, err := c.Get(ctx, "k", loader); err != nil {
		t.Fatalf("second Get failed: %v", err)
	}

	if got := counterValue(t, reader, "smartcache.m-hitloaded.loaded"); got != 1 {
		t.Errorf("loaded counter: got %d, want 1", got)
	}
	if got := counterValue(t, reader, "smartcache.m-hitloaded.hit"); got != 1 {
		t.Errorf("hit counter: got %d, want 1", got)
	}
	if got := histogramCount(t, reader, "smartcache.m-hitloaded.load_latency"); got != 1 {
		t.Errorf("load_latency count: got %d, want 1 (one loader run)", got)
	}
}

// TestMetrics_Get_ColdNotFound_ThenWarmNegativeHit verifies a cold not-found
// increments "loaded" (not "negative_hit"), and the warm repeat increments
// "negative_hit" (not "loaded").
func TestMetrics_Get_ColdNotFound_ThenWarmNegativeHit(t *testing.T) {
	reader, metricsOpt := newTestMeter()
	mgr, err := smartcache.NewManager(memstore.New(), metricsOpt)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "m-negative", &smartcache.EntityOptions{TTL: ptr(time.Minute), NegativeTTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx := context.Background()
	loader := func(ctx context.Context) (*sample, error) { return nil, smartcache.ErrNotFound }

	if _, _, err := c.Get(ctx, "k", loader); !errors.Is(err, smartcache.ErrNotFound) {
		t.Fatalf("first Get: got %v, want smartcache.ErrNotFound", err)
	}
	if _, _, err := c.Get(ctx, "k", loader); !errors.Is(err, smartcache.ErrNotFound) {
		t.Fatalf("second Get: got %v, want smartcache.ErrNotFound", err)
	}

	if got := counterValue(t, reader, "smartcache.m-negative.loaded"); got != 1 {
		t.Errorf("loaded counter (cold not-found): got %d, want 1", got)
	}
	if got := counterValue(t, reader, "smartcache.m-negative.negative_hit"); got != 1 {
		t.Errorf("negative_hit counter (warm repeat): got %d, want 1", got)
	}
}

// TestMetrics_Get_LoadError verifies a transient loader error increments ONLY
// load_error, never loaded.
func TestMetrics_Get_LoadError(t *testing.T) {
	reader, metricsOpt := newTestMeter()
	mgr, err := smartcache.NewManager(memstore.New(), metricsOpt)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "m-loaderr", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx := context.Background()
	loader := func(ctx context.Context) (*sample, error) { return nil, errors.New("db down") }

	if _, _, err := c.Get(ctx, "k", loader); err == nil {
		t.Fatal("Get: expected error, got nil")
	}

	if got := counterValue(t, reader, "smartcache.m-loaderr.load_error"); got != 1 {
		t.Errorf("load_error counter: got %d, want 1", got)
	}
	if got := counterValue(t, reader, "smartcache.m-loaderr.loaded"); got != 0 {
		t.Errorf("loaded counter on transient error: got %d, want 0 (must not double-count)", got)
	}
}

// TestMetrics_Put_WrittenAndNotCached verifies Put's success and populate-
// failure paths each increment their own counter.
func TestMetrics_Put_WrittenAndNotCached(t *testing.T) {
	reader, metricsOpt := newTestMeter()
	mgr, err := smartcache.NewManager(memstore.New(), metricsOpt)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "m-put", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx := context.Background()
	writer := func(ctx context.Context) (*sample, error) { return &sample{N: 1}, nil }
	if _, _, err := c.Put(ctx, "k", writer); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	if got := counterValue(t, reader, "smartcache.m-put.written"); got != 1 {
		t.Errorf("written counter: got %d, want 1", got)
	}

	writerErr := errors.New("boom")
	failWriter := func(ctx context.Context) (*sample, error) { return nil, writerErr }
	if _, _, err := c.Put(ctx, "k2", failWriter); !errors.Is(err, writerErr) {
		t.Fatalf("Put with failing writer: got %v, want %v", err, writerErr)
	}
	if got := counterValue(t, reader, "smartcache.m-put.written"); got != 1 {
		t.Errorf("written counter after failed writer: got %d, want unchanged 1", got)
	}
}

// TestMetrics_Evict_And_EvictMany verifies Evict increments by 1 and
// EvictMany increments by the number of keys.
func TestMetrics_Evict_And_EvictMany(t *testing.T) {
	reader, metricsOpt := newTestMeter()
	mgr, err := smartcache.NewManager(memstore.New(), metricsOpt)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "m-evict", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx := context.Background()
	if err := c.Evict(ctx, "a"); err != nil {
		t.Fatalf("Evict failed: %v", err)
	}
	if err := c.EvictMany(ctx, "b", "c", "d"); err != nil {
		t.Fatalf("EvictMany failed: %v", err)
	}

	if got := counterValue(t, reader, "smartcache.m-evict.evict"); got != 4 {
		t.Errorf("evict counter: got %d, want 4 (1 + 3)", got)
	}
}

// TestManager_Shutdown_WithMeterProvider_Succeeds verifies Shutdown succeeds
// (flushes and stops) when a meter provider IS configured.
func TestManager_Shutdown_WithMeterProvider_Succeeds(t *testing.T) {
	_, metricsOpt := newTestMeter()
	mgr, err := smartcache.NewManager(memstore.New(), metricsOpt)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	if err := mgr.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown with a configured meter provider: got error %v, want nil", err)
	}
}

// TestWithOTLP_NilURL_Error verifies WithOTLP rejects a config with no URL.
func TestWithOTLP_NilURL_Error(t *testing.T) {
	_, err := smartcache.NewManager(memstore.New(), smartcache.WithOTLP(smartcache.OTLPConfig{}))
	if err == nil {
		t.Fatal("NewManager with WithOTLP(no URL): expected error, got nil")
	}
}

// TestWithOTLP_RealExporter_Constructs verifies the real (non-test) OTLP
// exporter/meter-provider path — resolveOTLP's defaulting and
// newMeterProvider's construction — succeeds without needing a reachable
// collector: the gRPC exporter dials lazily, so NewManager/Register/Get never
// block on network connectivity. Shutdown DOES attempt a real network flush,
// so with no collector listening it legitimately fails (connection refused) —
// this test only asserts that Shutdown returns within a short bound rather
// than hanging, not that the flush itself succeeds.
func TestWithOTLP_RealExporter_Constructs(t *testing.T) {
	url := "localhost:4317"
	insecure := true
	mgr, err := smartcache.NewManager(memstore.New(), smartcache.WithOTLP(smartcache.OTLPConfig{
		URL:      &url,
		Insecure: &insecure,
	}))
	if err != nil {
		t.Fatalf("NewManager with WithOTLP failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "m-real-otlp", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	loader := func(ctx context.Context) (*sample, error) { return &sample{N: 1}, nil }
	if _, _, err := c.Get(context.Background(), "k", loader); err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- mgr.Shutdown(shutdownCtx) }()
	select {
	case <-done:
		// Shutdown returned (nil or a network error, either is acceptable —
		// there is no collector listening at this address).
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown did not return within 2s")
	}
}

// TestMetrics_Get_ConcurrentColdLoads_CountsRequestsNotLoads verifies the
// singleflight metric model: N concurrent cold Gets on one key record "loaded"
// once PER SERVED REQUEST (20), while the single deduped loader run records
// exactly one load_latency sample. Before the fix, load-side counters were
// recorded inside the singleflight closure, so 20 concurrent misses recorded
// loaded=1 and distorted the hit-rate signal.
func TestMetrics_Get_ConcurrentColdLoads_CountsRequestsNotLoads(t *testing.T) {
	reader, metricsOpt := newTestMeter()
	mgr, err := smartcache.NewManager(memstore.New(), metricsOpt)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "m-concurrent", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx := context.Background()
	loader := func(ctx context.Context) (*sample, error) {
		time.Sleep(20 * time.Millisecond)
		return &sample{N: 1}, nil
	}

	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, _, getErr := c.Get(ctx, "same", loader); getErr != nil {
				t.Errorf("concurrent Get failed: %v", getErr)
			}
		})
	}
	wg.Wait()

	if got := counterValue(t, reader, "smartcache.m-concurrent.loaded"); got != 20 {
		t.Errorf("loaded counter: got %d, want 20 (one per served request, not one per load)", got)
	}
	if got := histogramCount(t, reader, "smartcache.m-concurrent.load_latency"); got != 1 {
		t.Errorf("load_latency count: got %d, want 1 (single deduped loader run)", got)
	}
}
