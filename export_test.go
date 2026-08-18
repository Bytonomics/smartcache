package smartcache

import (
	"go.opentelemetry.io/otel/metric"
)

// SetJitterRandForTest overrides the package-level jitter randomness seam for
// deterministic expiry assertions and returns a restore func. Tests that use it
// must NOT run in parallel (it mutates package-global state).
func SetJitterRandForTest(f func() float64) (restore func()) {
	old := jitterRand
	jitterRand = f
	return func() { jitterRand = old }
}

// WithMeterProviderForTest injects a MeterProvider (e.g. one backed by an
// sdkmetric.ManualReader) so metric-emission tests can Collect and assert without
// a live OTLP collector. The argument must be both a metric.MeterProvider and a
// shutdownFlusher — *sdkmetric.MeterProvider is.
func WithMeterProviderForTest(mp interface {
	metric.MeterProvider
	shutdownFlusher
}) ManagerOption {
	return func(m *Manager) error {
		m.meter = mp.Meter("github.com/Bytonomics/smartcache")
		m.meterProvider = mp
		return nil
	}
}
