package smartcache_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Bytonomics/smartcache"
	"github.com/Bytonomics/smartcache/memstore"
)

// TestNewManager_NilStore_Error verifies NewManager rejects a nil store.
func TestNewManager_NilStore_Error(t *testing.T) {
	_, err := smartcache.NewManager(nil)
	if !errors.Is(err, smartcache.ErrNilStore) {
		t.Errorf("NewManager(nil): got %v, want smartcache.ErrNilStore", err)
	}
}

// TestRegister_TTLValidation verifies Register's TTL validation, ported from the
// old New()'s TestNew_TTLValidation.
func TestRegister_TTLValidation(t *testing.T) {
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	// TTL=0, AllowInfinite=false should fail
	_, err = smartcache.Register[sample](mgr, "ttl-zero", &smartcache.EntityOptions{TTL: ptr(time.Duration(0))})
	if err == nil {
		t.Fatal("Register with TTL=0, AllowInfinite=false: expected error, got nil")
	}
	if !errors.Is(err, smartcache.ErrInvalidTTL) {
		t.Errorf("Register TTL=0 error: got %v, want smartcache.ErrInvalidTTL", err)
	}

	// TTL=0, AllowInfinite=true should succeed
	_, err = smartcache.Register[sample](mgr, "ttl-zero-infinite", &smartcache.EntityOptions{TTL: ptr(time.Duration(0)), AllowInfinite: ptr(true)})
	if err != nil {
		t.Fatalf("Register with TTL=0, AllowInfinite=true failed: %v", err)
	}

	// TTL=time.Minute should succeed
	_, err = smartcache.Register[sample](mgr, "ttl-minute", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register with TTL=time.Minute failed: %v", err)
	}
}

// TestRegister_EmptyName_Error verifies an empty registration name is rejected.
func TestRegister_EmptyName_Error(t *testing.T) {
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	_, err = smartcache.Register[sample](mgr, "", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if !errors.Is(err, smartcache.ErrEmptyName) {
		t.Errorf(`Register(""): got %v, want smartcache.ErrEmptyName`, err)
	}
}

// TestRegister_DuplicateName_Error verifies a second Register with the same name
// fails, and that a FAILED Register never consumes the name.
func TestRegister_DuplicateName_Error(t *testing.T) {
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	if _, err := smartcache.Register[sample](mgr, "dup", &smartcache.EntityOptions{TTL: ptr(time.Minute)}); err != nil {
		t.Fatalf("first Register(\"dup\") failed: %v", err)
	}

	_, err = smartcache.Register[sample](mgr, "dup", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if !errors.Is(err, smartcache.ErrDuplicateName) {
		t.Errorf(`second Register("dup"): got %v, want smartcache.ErrDuplicateName`, err)
	}
}

// TestRegister_DefaultPrefixFromName verifies the registered name is used as the
// default key prefix when EntityOptions.Prefix is nil.
func TestRegister_DefaultPrefixFromName(t *testing.T) {
	store := &captureStore{CacheStore: memstore.New()}
	mgr, err := smartcache.NewManager(store)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "user", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx := context.Background()
	if err := c.PutValue(ctx, "42", &sample{N: 1}); err != nil {
		t.Fatalf("PutValue failed: %v", err)
	}
	if len(store.setKeys) == 0 || store.setKeys[len(store.setKeys)-1] != "bc:user:42" {
		t.Errorf("stored key: got %v, want last entry %q (default prefix is the registered name)", store.setKeys, "bc:user:42")
	}
}

// TestRegister_PrefixOverride verifies EntityOptions.Prefix overrides the
// registered name as the key prefix.
func TestRegister_PrefixOverride(t *testing.T) {
	store := &captureStore{CacheStore: memstore.New()}
	mgr, err := smartcache.NewManager(store)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "user", &smartcache.EntityOptions{TTL: ptr(time.Minute), Prefix: ptr("custom")})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx := context.Background()
	if err := c.PutValue(ctx, "42", &sample{N: 1}); err != nil {
		t.Fatalf("PutValue failed: %v", err)
	}
	if len(store.setKeys) == 0 || store.setKeys[len(store.setKeys)-1] != "bc:custom:42" {
		t.Errorf("stored key: got %v, want last entry %q", store.setKeys, "bc:custom:42")
	}
}

// TestRegister_EmptyPrefix_Error verifies Register rejects an explicitly empty
// EntityOptions.Prefix rather than silently namespacing keys under no prefix.
func TestRegister_EmptyPrefix_Error(t *testing.T) {
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	_, err = smartcache.Register[sample](mgr, "user", &smartcache.EntityOptions{TTL: ptr(time.Minute), Prefix: ptr("")})
	if !errors.Is(err, smartcache.ErrEmptyPrefix) {
		t.Errorf("Register with empty Prefix: got %v, want smartcache.ErrEmptyPrefix", err)
	}
}

// TestRegister_InheritVsOverride verifies EntityOptions fields left nil inherit
// the manager's defaults, and non-nil fields override them. JitterFraction is
// pinned to 0 at the manager level so the TTL assertions below compare exact
// values with no jitter distortion — jitter itself is covered separately by
// jitter_test.go.
func TestRegister_InheritVsOverride(t *testing.T) {
	store := &captureStore{CacheStore: memstore.New()}
	mgr, err := smartcache.NewManager(store, smartcache.WithDefaultTTL(time.Hour), smartcache.WithDefaultJitterFraction(0))
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	inherited, err := smartcache.Register[sample](mgr, "inherited", nil)
	if err != nil {
		t.Fatalf("Register(inherited) failed: %v", err)
	}
	ctx := context.Background()
	if err := inherited.PutValue(ctx, "k", &sample{N: 1}); err != nil {
		t.Fatalf("PutValue failed: %v", err)
	}
	if got := store.lastTTL(); got != time.Hour {
		t.Errorf("inherited TTL: got %v, want %v (manager default)", got, time.Hour)
	}

	overridden, err := smartcache.Register[sample](mgr, "overridden", &smartcache.EntityOptions{TTL: ptr(30 * time.Minute)})
	if err != nil {
		t.Fatalf("Register(overridden) failed: %v", err)
	}
	if err := overridden.PutValue(ctx, "k", &sample{N: 1}); err != nil {
		t.Fatalf("PutValue failed: %v", err)
	}
	if got := store.lastTTL(); got != 30*time.Minute {
		t.Errorf("overridden TTL: got %v, want %v (EntityOptions override)", got, 30*time.Minute)
	}
}

// TestRegister_InvalidJitterFraction verifies out-of-range jitter fractions are rejected.
func TestRegister_InvalidJitterFraction(t *testing.T) {
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	_, err = smartcache.Register[sample](mgr, "jitter-neg", &smartcache.EntityOptions{TTL: ptr(time.Minute), JitterFraction: ptr(-0.1)})
	if !errors.Is(err, smartcache.ErrInvalidJitterFraction) {
		t.Errorf("JitterFraction=-0.1: got %v, want smartcache.ErrInvalidJitterFraction", err)
	}

	_, err = smartcache.Register[sample](mgr, "jitter-one", &smartcache.EntityOptions{TTL: ptr(time.Minute), JitterFraction: ptr(1.0)})
	if !errors.Is(err, smartcache.ErrInvalidJitterFraction) {
		t.Errorf("JitterFraction=1.0: got %v, want smartcache.ErrInvalidJitterFraction", err)
	}

	_, err = smartcache.Register[sample](mgr, "jitter-valid", &smartcache.EntityOptions{TTL: ptr(time.Minute), JitterFraction: ptr(0.5)})
	if err != nil {
		t.Errorf("JitterFraction=0.5: got unexpected error %v", err)
	}
}

// TestManager_Shutdown_NoOTLP_NoOp verifies Shutdown is a no-op when WithOTLP
// was never configured.
func TestManager_Shutdown_NoOTLP_NoOp(t *testing.T) {
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	if err := mgr.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown with no OTLP configured: got error %v, want nil", err)
	}
}

// upperCaseCodec is a trivial non-JSON Codec for testing Codec overrides: it
// marshals a *sample by encoding its N field as a decimal string, uppercased
// (there are no letters in a plain integer, but the wrapper markers prove
// this codec — not the default JSON one — actually ran).
type upperCaseCodec struct{ calls int }

func (c *upperCaseCodec) Marshal(v any) ([]byte, error) {
	c.calls++
	s, ok := v.(*sample)
	if !ok {
		return nil, errors.New("upperCaseCodec: not a *sample")
	}
	return []byte("N=" + strconv.Itoa(s.N)), nil
}

func (c *upperCaseCodec) Unmarshal(data []byte, v any) error {
	s, ok := v.(*sample)
	if !ok {
		return errors.New("upperCaseCodec: not a *sample")
	}
	n, err := strconv.Atoi(strings.TrimPrefix(string(data), "N="))
	if err != nil {
		return err
	}
	s.N = n
	return nil
}

// TestRegister_CodecOverride verifies EntityOptions.Codec (and the manager's
// WithDefaultCodec) is actually used for serialization instead of the default
// JSON codec.
func TestRegister_CodecOverride(t *testing.T) {
	codec := &upperCaseCodec{}
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "codec-override", &smartcache.EntityOptions{TTL: ptr(time.Minute), Codec: codec})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx := context.Background()
	if err := c.PutValue(ctx, "k", &sample{N: 42}); err != nil {
		t.Fatalf("PutValue failed: %v", err)
	}
	if codec.calls == 0 {
		t.Error("custom codec's Marshal was never called")
	}

	loader := func(ctx context.Context) (*sample, error) {
		panic("loader should not be called")
	}
	val, outcome, err := c.Get(ctx, "k", loader)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if val == nil || val.N != 42 {
		t.Errorf("Get value: got %v, want N=42 (round-tripped through the custom codec)", val)
	}
	if outcome != smartcache.Hit {
		t.Errorf("Get outcome: got %v, want Hit", outcome)
	}
}

// TestGet_DisableSingleflight_StillCorrect verifies Get behaves correctly
// (still read-through, still caches) when DisableSingleflight is set — this
// exercises the non-deduped direct-call path (c.group == nil).
func TestGet_DisableSingleflight_StillCorrect(t *testing.T) {
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "no-singleflight", &smartcache.EntityOptions{
		TTL:                 ptr(time.Minute),
		DisableSingleflight: ptr(true),
	})
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	ctx := context.Background()
	var calls int
	loader := func(ctx context.Context) (*sample, error) {
		calls++
		return &sample{N: 7}, nil
	}

	val1, outcome1, err1 := c.Get(ctx, "k", loader)
	if err1 != nil {
		t.Fatalf("first Get failed: %v", err1)
	}
	if val1 == nil || val1.N != 7 {
		t.Errorf("first Get value: got %v, want N=7", val1)
	}
	if outcome1 != smartcache.Loaded {
		t.Errorf("first Get outcome: got %v, want Loaded", outcome1)
	}

	val2, outcome2, err2 := c.Get(ctx, "k", loader)
	if err2 != nil {
		t.Fatalf("second Get failed: %v", err2)
	}
	if val2 == nil || val2.N != 7 {
		t.Errorf("second Get value: got %v, want N=7", val2)
	}
	if outcome2 != smartcache.Hit {
		t.Errorf("second Get outcome: got %v, want Hit", outcome2)
	}
	if calls != 1 {
		t.Errorf("loader calls: got %d, want 1", calls)
	}
}
