package smartcache

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
)

// OTLPConfig configures the optional OpenTelemetry OTLP metric exporter on a
// Manager (via WithOTLP). Every field is an optional pointer; a nil field falls
// back to its documented default. URL is required — WithOTLP returns an error if
// it is nil.
type OTLPConfig struct {
	// URL is the OTLP gRPC endpoint as host:port (e.g. "localhost:4317"). Required.
	URL *string
	// FlushInterval is how often the background PeriodicReader exports. Default 15s.
	FlushInterval *time.Duration
	// Timeout bounds a single export. Default 10s.
	Timeout *time.Duration
	// Insecure uses plaintext gRPC when true. Default false.
	Insecure *bool
	// ServiceName becomes the resource service.name attribute. Default "smartcache".
	ServiceName *string
}

// resolvedOTLP is OTLPConfig with all defaults applied.
type resolvedOTLP struct {
	endpoint      string
	flushInterval time.Duration
	timeout       time.Duration
	insecure      bool
	serviceName   string
}

// resolveOTLP validates cfg and fills defaults. It returns an error when URL is nil.
func resolveOTLP(cfg OTLPConfig) (*resolvedOTLP, error) {
	if cfg.URL == nil {
		return nil, errors.New("smartcache: WithOTLP requires a non-nil URL")
	}
	r := &resolvedOTLP{
		endpoint:      *cfg.URL,
		flushInterval: 15 * time.Second,
		timeout:       10 * time.Second,
		insecure:      false,
		serviceName:   "smartcache",
	}
	if cfg.FlushInterval != nil {
		r.flushInterval = *cfg.FlushInterval
	}
	if cfg.Timeout != nil {
		r.timeout = *cfg.Timeout
	}
	if cfg.Insecure != nil {
		r.insecure = *cfg.Insecure
	}
	if cfg.ServiceName != nil {
		r.serviceName = *cfg.ServiceName
	}
	return r, nil
}

// newMeterProvider builds an OTLP gRPC exporter + PeriodicReader + MeterProvider
// from r. The PeriodicReader owns the background flush goroutine that exports
// every r.flushInterval (fire-and-forget); MeterProvider.Shutdown flushes the
// final batch and stops it.
func newMeterProvider(ctx context.Context, r *resolvedOTLP) (*sdkmetric.MeterProvider, error) {
	opts := []otlpmetricgrpc.Option{
		otlpmetricgrpc.WithEndpoint(r.endpoint),
		otlpmetricgrpc.WithTimeout(r.timeout),
	}
	if r.insecure {
		opts = append(opts, otlpmetricgrpc.WithInsecure())
	}
	exp, err := otlpmetricgrpc.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("smartcache: otlp metric exporter: %w", err)
	}
	reader := sdkmetric.NewPeriodicReader(exp, sdkmetric.WithInterval(r.flushInterval))
	res := resource.NewSchemaless(attribute.String("service.name", r.serviceName))
	return sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(reader),
		sdkmetric.WithResource(res),
	), nil
}

// cacheMetrics is one registered cache's instrument set. A nil *cacheMetrics is a
// valid "telemetry disabled" value: every record method returns immediately on a
// nil receiver.
type cacheMetrics struct {
	hit              metric.Int64Counter
	loaded           metric.Int64Counter
	loadedNotCached  metric.Int64Counter
	negativeHit      metric.Int64Counter
	loadError        metric.Int64Counter
	written          metric.Int64Counter
	writtenNotCached metric.Int64Counter
	evict            metric.Int64Counter
	loadLatency      metric.Float64Histogram
}

// newCacheMetrics builds the instrument set for a cache named name. Callers
// must not invoke this with a nil meter — checking whether telemetry is
// enabled is the caller's responsibility (see Manager.Register), so this
// function never has a reason to return a nil value with a nil error.
// Instrument names are smartcache.<name>.<event>; there are NEVER any
// key-level attributes.
func newCacheMetrics(meter metric.Meter, name string) (*cacheMetrics, error) {
	m := &cacheMetrics{}
	var err error
	if m.hit, err = meter.Int64Counter("smartcache." + name + ".hit"); err != nil {
		return nil, err
	}
	if m.loaded, err = meter.Int64Counter("smartcache." + name + ".loaded"); err != nil {
		return nil, err
	}
	if m.loadedNotCached, err = meter.Int64Counter("smartcache." + name + ".loaded_not_cached"); err != nil {
		return nil, err
	}
	if m.negativeHit, err = meter.Int64Counter("smartcache." + name + ".negative_hit"); err != nil {
		return nil, err
	}
	if m.loadError, err = meter.Int64Counter("smartcache." + name + ".load_error"); err != nil {
		return nil, err
	}
	if m.written, err = meter.Int64Counter("smartcache." + name + ".written"); err != nil {
		return nil, err
	}
	if m.writtenNotCached, err = meter.Int64Counter("smartcache." + name + ".written_not_cached"); err != nil {
		return nil, err
	}
	if m.evict, err = meter.Int64Counter("smartcache." + name + ".evict"); err != nil {
		return nil, err
	}
	if m.loadLatency, err = meter.Float64Histogram("smartcache."+name+".load_latency", metric.WithUnit("s")); err != nil {
		return nil, err
	}
	return m, nil
}

// recordOutcome increments the counter for o. Written/WrittenNotCached and the
// Get-side outcomes are all handled; it is nil-safe.
func (m *cacheMetrics) recordOutcome(ctx context.Context, o Outcome) {
	if m == nil {
		return
	}
	switch o {
	case Hit:
		m.hit.Add(ctx, 1)
	case Loaded:
		m.loaded.Add(ctx, 1)
	case LoadedNotCached:
		m.loadedNotCached.Add(ctx, 1)
	case NegativeHit:
		m.negativeHit.Add(ctx, 1)
	case Written:
		m.written.Add(ctx, 1)
	case WrittenNotCached:
		m.writtenNotCached.Add(ctx, 1)
	default:
	}
}

func (m *cacheMetrics) recordLoadError(ctx context.Context) {
	if m == nil {
		return
	}
	m.loadError.Add(ctx, 1)
}

func (m *cacheMetrics) recordLoadLatencySeconds(ctx context.Context, s float64) {
	if m == nil {
		return
	}
	m.loadLatency.Record(ctx, s)
}

func (m *cacheMetrics) recordEvict(ctx context.Context, n int64) {
	if m == nil {
		return
	}
	m.evict.Add(ctx, n)
}
