package smartcache

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"time"

	"go.opentelemetry.io/otel/metric"
	"golang.org/x/sync/singleflight"
)

// managerDefaults holds the global defaults every registered cache inherits
// unless its EntityOptions override them.
type managerDefaults struct {
	ttl                 time.Duration
	jitterFraction      float64
	negativeTTL         time.Duration
	disableSingleflight bool
	codec               Codec
}

// shutdownFlusher is the subset of *sdkmetric.MeterProvider that Manager.Shutdown
// needs. It exists so the meter provider can be swapped in tests.
type shutdownFlusher interface {
	ForceFlush(ctx context.Context) error
	Shutdown(ctx context.Context) error
}

// Manager owns the shared backend store, global defaults, and (optionally) the
// OTLP meter provider. Caches are created against it with Register.
type Manager struct {
	store    CacheStore
	defaults managerDefaults

	mu       sync.Mutex
	registry map[string]struct{}

	otlp          *resolvedOTLP
	meter         metric.Meter
	meterProvider shutdownFlusher
}

// ManagerOption configures a Manager in NewManager.
type ManagerOption func(*Manager) error

// NewManager builds a Manager over store. It returns ErrNilStore if store is nil.
// When WithOTLP was supplied (and no meter was injected for tests), it stands up
// the OTLP exporter + periodic reader + meter provider.
func NewManager(store CacheStore, opts ...ManagerOption) (*Manager, error) {
	if store == nil {
		return nil, ErrNilStore
	}
	m := &Manager{
		store:    store,
		defaults: managerDefaults{jitterFraction: defaultJitterFraction},
		registry: make(map[string]struct{}),
	}
	for _, opt := range opts {
		if err := opt(m); err != nil {
			return nil, err
		}
	}
	if m.otlp != nil && m.meter == nil {
		mp, err := newMeterProvider(context.Background(), m.otlp)
		if err != nil {
			return nil, err
		}
		m.meterProvider = mp
		m.meter = mp.Meter("github.com/Bytonomics/smartcache")
	}
	return m, nil
}

// WithDefaultTTL sets the default positive TTL inherited by caches that do not
// override EntityOptions.TTL.
func WithDefaultTTL(d time.Duration) ManagerOption {
	return func(m *Manager) error { m.defaults.ttl = d; return nil }
}

// WithDefaultJitterFraction sets the default downward-jitter fraction (0 disables).
func WithDefaultJitterFraction(f float64) ManagerOption {
	return func(m *Manager) error { m.defaults.jitterFraction = f; return nil }
}

// WithDefaultNegativeTTL sets the default negative-cache TTL (0 disables negative caching).
func WithDefaultNegativeTTL(d time.Duration) ManagerOption {
	return func(m *Manager) error { m.defaults.negativeTTL = d; return nil }
}

// WithDefaultDisableSingleflight sets the default singleflight toggle.
func WithDefaultDisableSingleflight(b bool) ManagerOption {
	return func(m *Manager) error { m.defaults.disableSingleflight = b; return nil }
}

// WithDefaultCodec sets the default codec (caches fall back to JSON when nil).
func WithDefaultCodec(c Codec) ManagerOption {
	return func(m *Manager) error { m.defaults.codec = c; return nil }
}

// WithOTLP enables OpenTelemetry OTLP metric export. It returns an error if the
// config's URL is nil.
func WithOTLP(cfg OTLPConfig) ManagerOption {
	return func(m *Manager) error {
		r, err := resolveOTLP(cfg)
		if err != nil {
			return err
		}
		m.otlp = r
		return nil
	}
}

// EntityOptions overrides manager defaults for one registered cache. Every field
// is an optional pointer: nil inherits the manager default, non-nil overrides it.
type EntityOptions struct {
	Prefix              *string
	TTL                 *time.Duration
	AllowInfinite       *bool
	JitterFraction      *float64
	NegativeTTL         *time.Duration
	DisableSingleflight *bool
	Codec               Codec
	AliasMode           *AliasMode // nil => AliasColocated; honored only by RegisterAliasGroup.
}

// Register creates a Cache[T] on m under name (required, unique; it doubles as
// the metric name and the default key prefix). It panics with ErrPointerType if
// T is a pointer type. It returns ErrEmptyName, ErrEmptyPrefix, ErrDuplicateName,
// ErrInvalidTTL, or ErrInvalidJitterFraction on invalid input. A failed Register
// never consumes the name.
func Register[T any](m *Manager, name string, opts *EntityOptions) (*Cache[T], error) {
	if t := reflect.TypeFor[T](); t.Kind() == reflect.Pointer {
		panic(fmt.Errorf("smartcache.Register[%s]: %w", t, ErrPointerType))
	}
	if name == "" {
		return nil, ErrEmptyName
	}
	if opts == nil {
		opts = &EntityOptions{}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, dup := m.registry[name]; dup {
		return nil, fmt.Errorf("%q: %w", name, ErrDuplicateName)
	}

	prefix := name
	if opts.Prefix != nil {
		prefix = *opts.Prefix
	}
	if prefix == "" {
		return nil, ErrEmptyPrefix
	}
	ttl := m.defaults.ttl
	if opts.TTL != nil {
		ttl = *opts.TTL
	}
	allowInfinite := false
	if opts.AllowInfinite != nil {
		allowInfinite = *opts.AllowInfinite
	}
	negativeTTL := m.defaults.negativeTTL
	if opts.NegativeTTL != nil {
		negativeTTL = *opts.NegativeTTL
	}
	disableSF := m.defaults.disableSingleflight
	if opts.DisableSingleflight != nil {
		disableSF = *opts.DisableSingleflight
	}
	jitter := m.defaults.jitterFraction
	if opts.JitterFraction != nil {
		jitter = *opts.JitterFraction
	}
	codec := opts.Codec
	if codec == nil {
		codec = m.defaults.codec
	}
	if codec == nil {
		codec = jsonCodec{}
	}

	if ttl <= 0 && !allowInfinite {
		return nil, ErrInvalidTTL
	}
	if jitter < 0 || jitter >= 1 {
		return nil, ErrInvalidJitterFraction
	}

	// newCacheMetrics must never be called with a nil meter (it no longer
	// accepts one) — when telemetry is disabled, metrics simply stays nil,
	// and every *cacheMetrics method is nil-receiver-safe.
	var cm *cacheMetrics
	if m.meter != nil {
		var err error
		cm, err = newCacheMetrics(m.meter, name)
		if err != nil {
			return nil, fmt.Errorf("smartcache: register %q metrics: %w", name, err)
		}
	}

	var batch BatchCacheStore
	if bs, ok := m.store.(BatchCacheStore); ok {
		batch = bs
	}
	var group *singleflight.Group
	if !disableSF {
		group = &singleflight.Group{}
	}

	c := &Cache[T]{
		store: m.store,
		batch: batch,
		opts: Options{
			Prefix:              prefix,
			TTL:                 ttl,
			AllowInfinite:       allowInfinite,
			NegativeTTL:         negativeTTL,
			Codec:               codec,
			DisableSingleflight: disableSF,
		},
		codec:          codec,
		group:          group,
		jitterFraction: jitter,
		metrics:        cm,
	}
	m.registry[name] = struct{}{}
	return c, nil
}

// RegisterAliasGroup registers an alias-group cache: one cached value reachable by several alias
// keys. It behaves like Register but additionally (a) requires the manager's store to implement
// AliasCacheStore, (b) requires T to implement PrimaryKeyed, and (c) resolves the slot mode from
// opts.AliasMode (default AliasColocated) and binds an AliasOps strategy handle via the store's
// AliasGroup factory. Like Register's pointer-type check, it panics on a misconfiguration that
// must fail at init: a pointer T, a store without AliasCacheStore, or a T that is not PrimaryKeyed.
func RegisterAliasGroup[T any](m *Manager, name string, opts *EntityOptions) (*Cache[T], error) {
	if t := reflect.TypeFor[T](); t.Kind() == reflect.Pointer {
		panic(fmt.Errorf("smartcache.RegisterAliasGroup[%s]: %w", t, ErrPointerType))
	}
	aliasStore, ok := m.store.(AliasCacheStore)
	if !ok {
		panic(fmt.Errorf("smartcache.RegisterAliasGroup[%q]: %w", name, ErrAliasingNotSupported))
	}
	if _, ok := any((*T)(nil)).(PrimaryKeyed); !ok {
		panic(fmt.Errorf("smartcache.RegisterAliasGroup[%q]: T must implement PrimaryKeyed (CachePrimaryKey() string)", name))
	}
	mode := AliasColocated
	if opts != nil && opts.AliasMode != nil {
		mode = *opts.AliasMode
	}
	c, err := Register[T](m, name, opts)
	if err != nil {
		return nil, err
	}
	c.aliasOps = aliasStore.AliasGroup(c.opts.Prefix, mode)
	c.isAliasGroup = true
	return c, nil
}

// Shutdown flushes and stops the OTLP meter provider. It is a no-op when WithOTLP
// was not configured.
func (m *Manager) Shutdown(ctx context.Context) error {
	if m.meterProvider == nil {
		return nil
	}
	if err := m.meterProvider.Shutdown(ctx); err != nil {
		return fmt.Errorf("smartcache: manager shutdown: %w", err)
	}
	return nil
}
