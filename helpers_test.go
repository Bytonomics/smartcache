package smartcache_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Bytonomics/smartcache"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// ptr returns a pointer to v — for building EntityOptions pointer fields.
func ptr[T any](v T) *T { return &v }

// sample is the test value type.
type sample struct{ N int }

// failSetStore wraps a CacheStore but forces Set to fail.
type failSetStore struct{ smartcache.CacheStore }

func (f failSetStore) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	return errors.New("forced set failure")
}

// failDeleteStore wraps a CacheStore but forces Delete to fail.
type failDeleteStore struct{ smartcache.CacheStore }

func (f failDeleteStore) Delete(ctx context.Context, key string) error {
	return errors.New("forced delete failure")
}

// countingStore wraps a CacheStore and counts Get calls. It embeds the CacheStore
// INTERFACE, so it does NOT implement BatchCacheStore even when the wrapped store
// does — used to exercise GetMany's per-key fallback.
type countingStore struct {
	smartcache.CacheStore
	getCalls int64
}

func (s *countingStore) Get(ctx context.Context, key string) ([]byte, error) {
	atomic.AddInt64(&s.getCalls, 1)
	return s.CacheStore.Get(ctx, key)
}

// captureStore wraps a CacheStore and records the TTL + key of every Set (used
// for jitter assertions). It embeds the CacheStore interface, so it is not a
// BatchCacheStore.
type captureStore struct {
	smartcache.CacheStore
	mu      sync.Mutex
	setTTLs []time.Duration
	setKeys []string
}

func (s *captureStore) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	s.mu.Lock()
	s.setTTLs = append(s.setTTLs, ttl)
	s.setKeys = append(s.setKeys, key)
	s.mu.Unlock()
	return s.CacheStore.Set(ctx, key, val, ttl)
}

func (s *captureStore) lastTTL() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.setTTLs) == 0 {
		return 0
	}
	return s.setTTLs[len(s.setTTLs)-1]
}

// newTestMeter returns a ManualReader and a ManagerOption that wires a
// MeterProvider backed by it, so metric emission can be asserted without a live
// collector.
func newTestMeter() (*sdkmetric.ManualReader, smartcache.ManagerOption) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	return reader, smartcache.WithMeterProviderForTest(mp)
}

// counterValue collects and sums the int64 counter named metricName (0 if absent).
func counterValue(t *testing.T, reader *sdkmetric.ManualReader, metricName string) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != metricName {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("%s: not an int64 sum", metricName)
			}
			var total int64
			for _, dp := range sum.DataPoints {
				total += dp.Value
			}
			return total
		}
	}
	return 0
}

// histogramCount collects and sums the count of the float64 histogram named metricName.
func histogramCount(t *testing.T, reader *sdkmetric.ManualReader, metricName string) uint64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != metricName {
				continue
			}
			h, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("%s: not a float64 histogram", metricName)
			}
			var total uint64
			for _, dp := range h.DataPoints {
				total += dp.Count
			}
			return total
		}
	}
	return 0
}
