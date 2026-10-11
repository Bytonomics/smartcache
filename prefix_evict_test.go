package smartcache_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Bytonomics/smartcache"
	"github.com/Bytonomics/smartcache/memstore"
)

// evictPrefixNoPrefixStore hides the optional PrefixCacheStore method: embedding the CacheStore
// interface promotes only the four CacheStore methods.
type evictPrefixNoPrefixStore struct {
	smartcache.CacheStore
}

// evictPrefixNoPrefixAliasStore hides the optional PrefixAliasOps method: its AliasGroup returns an
// AliasOps that embeds only the AliasOps interface.
type evictPrefixNoPrefixAliasStore struct {
	smartcache.AliasCacheStore
}

// AliasGroup returns an AliasOps that cannot prefix-evict.
func (s evictPrefixNoPrefixAliasStore) AliasGroup(ns string, mode smartcache.AliasMode) smartcache.AliasOps {
	return evictPrefixNoPrefixAliasOps{AliasOps: s.AliasCacheStore.AliasGroup(ns, mode)}
}

// evictPrefixNoPrefixAliasOps embeds the AliasOps interface only, so it lacks EvictByPrimaryPrefix.
type evictPrefixNoPrefixAliasOps struct {
	smartcache.AliasOps
}

// evictPrefixFailingStore is a PrefixCacheStore whose DeleteByPrefix reports three removed entries
// together with err.
type evictPrefixFailingStore struct {
	smartcache.CacheStore
	err error
}

// DeleteByPrefix reports three removed entries and s.err.
func (s evictPrefixFailingStore) DeleteByPrefix(context.Context, string) (int64, error) {
	return 3, s.err
}

// evictPrefixFailLoader returns a loader that records a test error if it runs. It proves a read is
// served from the cache.
func evictPrefixFailLoader(t *testing.T) smartcache.Loader[sample] {
	t.Helper()
	return func(context.Context) (*sample, error) {
		t.Errorf("loader must not be called on a cache hit")
		return nil, errors.New("unexpected loader call")
	}
}

// evictPrefixCountingLoader returns a loader that counts its calls in *calls and yields a value.
func evictPrefixCountingLoader(calls *int) smartcache.Loader[sample] {
	return func(context.Context) (*sample, error) {
		*calls++
		return &sample{N: 1}, nil
	}
}

// evictPrefixNotFoundLoader returns a loader that counts its calls in *calls and reports not found.
func evictPrefixNotFoundLoader(calls *int) smartcache.Loader[sample] {
	return func(context.Context) (*sample, error) {
		*calls++
		return nil, smartcache.ErrNotFound
	}
}

// newEvictPrefixPlain registers a plain sample cache named name over store, with a one-minute TTL.
func newEvictPrefixPlain(t *testing.T, store smartcache.CacheStore, name string) *smartcache.Cache[sample] {
	t.Helper()
	mgr, err := smartcache.NewManager(store)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, name, &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	return c
}

// evictPrefixSeed writes each key into c with a distinct value.
func evictPrefixSeed(t *testing.T, c *smartcache.Cache[sample], keys ...string) {
	t.Helper()
	for i, key := range keys {
		if err := c.PutValueByKey(context.Background(), key, &sample{N: i}); err != nil {
			t.Fatalf("PutValueByKey(%q): %v", key, err)
		}
	}
}

// evictPrefixPlainWithoutCapability evicts from a plain cache whose store lacks PrefixCacheStore.
func evictPrefixPlainWithoutCapability(t *testing.T) (int64, error) {
	t.Helper()
	c := newEvictPrefixPlain(t, evictPrefixNoPrefixStore{CacheStore: memstore.New()}, "no-prefix-plain")
	evictPrefixSeed(t, c, "app-1::a")
	return c.EvictByKeyPrefix(context.Background(), "app-1::")
}

// evictPrefixAliasWithoutCapability evicts from an alias-group cache whose AliasOps lacks
// PrefixAliasOps.
func evictPrefixAliasWithoutCapability(t *testing.T) (int64, error) {
	t.Helper()
	base, ok := memstore.New().(smartcache.AliasCacheStore)
	if !ok {
		t.Fatal("memstore must implement smartcache.AliasCacheStore")
	}
	mgr, err := smartcache.NewManager(evictPrefixNoPrefixAliasStore{AliasCacheStore: base})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	c, err := smartcache.RegisterAliasGroup[aliasUser](mgr, "no-prefix-alias", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("RegisterAliasGroup: %v", err)
	}
	return c.EvictByKeyPrefix(context.Background(), "app-1::")
}

// TestEvictByKeyPrefix_Plain_RemovesOnlyMatchingKeys verifies that a plain cache evicts exactly the
// keys that start with the prefix, and that the prefix is matched literally.
func TestEvictByKeyPrefix_Plain_RemovesOnlyMatchingKeys(t *testing.T) {
	tests := []struct {
		name        string
		seedKeys    []string
		keyPrefix   string
		wantRemoved int64
		wantMissing []string
		wantHit     []string
	}{
		{
			name:        "tenant prefix removes only that tenant",
			seedKeys:    []string{"app-1::a", "app-1::b", "app-2::a"},
			keyPrefix:   "app-1::",
			wantRemoved: 2,
			wantMissing: []string{"app-1::a", "app-1::b"},
			wantHit:     []string{"app-2::a"},
		},
		{
			name:        "glob characters match themselves",
			seedKeys:    []string{"a?b::1", "axb::1"},
			keyPrefix:   "a?",
			wantRemoved: 1,
			wantMissing: []string{"a?b::1"},
			wantHit:     []string{"axb::1"},
		},
	}
	for i := range tests {
		tc := &tests[i]
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			c := newEvictPrefixPlain(t, memstore.New(), "plain-evict")
			evictPrefixSeed(t, c, tc.seedKeys...)

			n, err := c.EvictByKeyPrefix(ctx, tc.keyPrefix)
			if err != nil {
				t.Fatalf("EvictByKeyPrefix(%q): %v", tc.keyPrefix, err)
			}
			if n != tc.wantRemoved {
				t.Errorf("EvictByKeyPrefix(%q) removed %d, want %d", tc.keyPrefix, n, tc.wantRemoved)
			}
			for _, key := range tc.wantMissing {
				var calls int
				if _, outcome, getErr := c.GetByKey(ctx, key, evictPrefixCountingLoader(&calls)); getErr != nil || outcome != smartcache.Loaded || calls != 1 {
					t.Errorf("GetByKey(%q) after eviction: outcome=%v err=%v loader calls=%d, want Loaded via loader", key, outcome, getErr, calls)
				}
			}
			for _, key := range tc.wantHit {
				if _, outcome, getErr := c.GetByKey(ctx, key, evictPrefixFailLoader(t)); getErr != nil || outcome != smartcache.Hit {
					t.Errorf("GetByKey(%q) for a non-matching key: outcome=%v err=%v, want Hit", key, outcome, getErr)
				}
			}
		})
	}
}

// TestEvictByKeyPrefix_Alias_RemovesOnlyMatchingRecords verifies that an alias-group cache evicts the
// records whose primary key matches, and that their alias pointers go with them, in both modes.
func TestEvictByKeyPrefix_Alias_RemovesOnlyMatchingRecords(t *testing.T) {
	bothModes(t, func(t *testing.T, c *smartcache.Cache[aliasUser]) {
		ctx := context.Background()
		seedAlias(t, c, "app-1::u1", "Ada", "a1@x.com")
		seedAlias(t, c, "app-2::u1", "Bob", "a2@x.com")

		n, err := c.EvictByKeyPrefix(ctx, "app-1::")
		if err != nil {
			t.Fatalf("EvictByKeyPrefix: %v", err)
		}
		if n != 1 {
			t.Errorf("EvictByKeyPrefix removed %d records, want 1", n)
		}

		var calls int
		loader := func(context.Context) (*aliasUser, error) {
			calls++
			return nil, smartcache.ErrNotFound
		}
		if _, _, getErr := c.GetByAlias(ctx, smartcache.AliasRef{Field: "email", Value: "a1@x.com"}, loader); !errors.Is(getErr, smartcache.ErrNotFound) {
			t.Errorf("GetByAlias for evicted record: err = %v, want ErrNotFound", getErr)
		}
		if calls != 1 {
			t.Errorf("loader calls for evicted alias = %d, want 1", calls)
		}
		if got, outcome, getErr := c.GetByAlias(ctx, smartcache.AliasRef{Field: "email", Value: "a2@x.com"}, failLoaderUser(t)); getErr != nil || outcome != smartcache.Hit || got.Name != "Bob" {
			t.Errorf("GetByAlias for non-matching record: outcome=%v err=%v, want Hit", outcome, getErr)
		}
	})
}

// TestEvictByKeyPrefix_EmptyPrefix_RejectsAndDeletesNothing verifies that an empty prefix is refused
// with ErrEmptyKeyPrefix instead of matching, and therefore flushing, every entry.
func TestEvictByKeyPrefix_EmptyPrefix_RejectsAndDeletesNothing(t *testing.T) {
	t.Run("Plain", func(t *testing.T) {
		ctx := context.Background()
		c := newEvictPrefixPlain(t, memstore.New(), "empty-plain")
		evictPrefixSeed(t, c, "app-1::a")

		n, err := c.EvictByKeyPrefix(ctx, "")
		if !errors.Is(err, smartcache.ErrEmptyKeyPrefix) {
			t.Errorf("EvictByKeyPrefix(\"\") error = %v, want ErrEmptyKeyPrefix", err)
		}
		if n != 0 {
			t.Errorf("EvictByKeyPrefix(\"\") removed %d, want 0", n)
		}
		if _, outcome, getErr := c.GetByKey(ctx, "app-1::a", evictPrefixFailLoader(t)); getErr != nil || outcome != smartcache.Hit {
			t.Errorf("entry after rejected empty prefix: outcome=%v err=%v, want Hit", outcome, getErr)
		}
	})

	bothModes(t, func(t *testing.T, c *smartcache.Cache[aliasUser]) {
		ctx := context.Background()
		seedAlias(t, c, "app-1::u1", "Ada", "a1@x.com")

		n, err := c.EvictByKeyPrefix(ctx, "")
		if !errors.Is(err, smartcache.ErrEmptyKeyPrefix) {
			t.Errorf("EvictByKeyPrefix(\"\") error = %v, want ErrEmptyKeyPrefix", err)
		}
		if n != 0 {
			t.Errorf("EvictByKeyPrefix(\"\") removed %d, want 0", n)
		}
		if got, outcome, getErr := c.GetByAlias(ctx, smartcache.AliasRef{Field: "email", Value: "a1@x.com"}, failLoaderUser(t)); getErr != nil || outcome != smartcache.Hit || got.Name != "Ada" {
			t.Errorf("alias after rejected empty prefix: outcome=%v err=%v, want Hit", outcome, getErr)
		}
	})
}

// TestEvictByKeyPrefix_UnsupportedStore_ReturnsErrPrefixEvictNotSupported verifies that a store, or an
// alias AliasOps, without prefix eviction reports ErrPrefixEvictNotSupported and removes nothing.
func TestEvictByKeyPrefix_UnsupportedStore_ReturnsErrPrefixEvictNotSupported(t *testing.T) {
	tests := []struct {
		name  string
		evict func(t *testing.T) (int64, error)
	}{
		{name: "plain store without PrefixCacheStore", evict: evictPrefixPlainWithoutCapability},
		{name: "alias ops without PrefixAliasOps", evict: evictPrefixAliasWithoutCapability},
	}
	for i := range tests {
		tc := &tests[i]
		t.Run(tc.name, func(t *testing.T) {
			n, err := tc.evict(t)
			if !errors.Is(err, smartcache.ErrPrefixEvictNotSupported) {
				t.Errorf("EvictByKeyPrefix error = %v, want ErrPrefixEvictNotSupported", err)
			}
			if n != 0 {
				t.Errorf("EvictByKeyPrefix removed %d, want 0", n)
			}
		})
	}
}

// TestEvictByKeyPrefix_Telemetry_CountsOnlyRemovedEntries verifies that the evict counter advances by
// the number of entries removed and stays unchanged when nothing matched.
func TestEvictByKeyPrefix_Telemetry_CountsOnlyRemovedEntries(t *testing.T) {
	reader, opt := newTestMeter()
	mgr, err := smartcache.NewManager(memstore.New(), opt)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	const name = "evict-telemetry"
	c, err := smartcache.Register[sample](mgr, name, &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	ctx := context.Background()
	evictPrefixSeed(t, c, "app-1::a", "app-1::b", "app-2::a")

	if n, evictErr := c.EvictByKeyPrefix(ctx, "app-1::"); evictErr != nil || n != 2 {
		t.Fatalf("EvictByKeyPrefix(app-1::) = (%d, %v), want (2, nil)", n, evictErr)
	}
	if got := counterValue(t, reader, "smartcache."+name+".evict"); got != 2 {
		t.Errorf("evict counter after removing 2 entries = %d, want 2", got)
	}

	if n, evictErr := c.EvictByKeyPrefix(ctx, "app-9::"); evictErr != nil || n != 0 {
		t.Fatalf("EvictByKeyPrefix(app-9::) = (%d, %v), want (0, nil)", n, evictErr)
	}
	if got := counterValue(t, reader, "smartcache."+name+".evict"); got != 2 {
		t.Errorf("evict counter after a call that removed 0 = %d, want unchanged 2", got)
	}
}

// TestEvictByKeyPrefix_StoreError_ReturnsCountAndError verifies that a store failure is returned
// unchanged together with the count the store reports as removed.
func TestEvictByKeyPrefix_StoreError_ReturnsCountAndError(t *testing.T) {
	errBoom := errors.New("boom")
	c := newEvictPrefixPlain(t, evictPrefixFailingStore{CacheStore: memstore.New(), err: errBoom}, "store-err")

	n, err := c.EvictByKeyPrefix(context.Background(), "app-1::")
	if !errors.Is(err, errBoom) {
		t.Errorf("EvictByKeyPrefix error = %v, want errBoom", err)
	}
	if n != 3 {
		t.Errorf("EvictByKeyPrefix removed %d, want the store's reported 3", n)
	}
}

// TestEvictByKeyPrefix_Plain_RemovesNegativeMarkers verifies that a cached not-found marker under a
// matching key is evicted with the prefix, while a marker outside the prefix survives.
func TestEvictByKeyPrefix_Plain_RemovesNegativeMarkers(t *testing.T) {
	ctx := context.Background()
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	c, err := smartcache.Register[sample](mgr, "neg-evict", &smartcache.EntityOptions{
		TTL:         ptr(time.Minute),
		NegativeTTL: ptr(time.Minute),
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	var calls int
	notFound := evictPrefixNotFoundLoader(&calls)
	for _, key := range []string{"app-1::missing", "app-2::missing"} {
		if _, _, getErr := c.GetByKey(ctx, key, notFound); !errors.Is(getErr, smartcache.ErrNotFound) {
			t.Fatalf("seed GetByKey(%q): err = %v, want ErrNotFound", key, getErr)
		}
	}
	if calls != 2 {
		t.Fatalf("seed loader calls = %d, want 2", calls)
	}

	n, err := c.EvictByKeyPrefix(ctx, "app-1::")
	if err != nil {
		t.Fatalf("EvictByKeyPrefix: %v", err)
	}
	if n != 1 {
		t.Errorf("EvictByKeyPrefix removed %d, want 1 (the app-1 negative marker)", n)
	}

	// The app-1 marker is gone, so the loader runs again and the miss is re-cached.
	if _, outcome, getErr := c.GetByKey(ctx, "app-1::missing", notFound); !errors.Is(getErr, smartcache.ErrNotFound) || outcome != smartcache.Loaded {
		t.Errorf("GetByKey(app-1::missing) after eviction: outcome=%v err=%v, want Loaded + ErrNotFound", outcome, getErr)
	}
	if calls != 3 {
		t.Errorf("loader calls after evicted marker = %d, want 3", calls)
	}

	// The app-2 marker is outside the prefix: served as a NegativeHit without loading.
	if _, outcome, getErr := c.GetByKey(ctx, "app-2::missing", evictPrefixFailLoader(t)); !errors.Is(getErr, smartcache.ErrNotFound) || outcome != smartcache.NegativeHit {
		t.Errorf("GetByKey(app-2::missing): outcome=%v err=%v, want NegativeHit + ErrNotFound", outcome, getErr)
	}
}

// evictPrefixSpyStore embeds a PrefixCacheStore and counts DeleteByPrefix calls before delegating, so a
// test can prove that a rejected prefix never reaches the store.
type evictPrefixSpyStore struct {
	smartcache.PrefixCacheStore
	calls int
}

// DeleteByPrefix counts the call and delegates to the embedded store.
func (s *evictPrefixSpyStore) DeleteByPrefix(ctx context.Context, keyPrefix string) (int64, error) {
	s.calls++
	return s.PrefixCacheStore.DeleteByPrefix(ctx, keyPrefix)
}

// TestEvictByKeyPrefix_WildcardPrefix_RejectsAndDeletesNothing verifies that a keyPrefix containing '*' is
// refused with ErrWildcardKeyPrefix before the store is reached, so no entry is removed on either cache kind.
func TestEvictByKeyPrefix_WildcardPrefix_RejectsAndDeletesNothing(t *testing.T) {
	for _, prefix := range []string{"*", "app-1::*", "*::a", "a*b", "**"} {
		t.Run(prefix, func(t *testing.T) {
			t.Run("Plain", func(t *testing.T) { assertWildcardRejectedPlain(t, prefix) })
			bothModes(t, func(t *testing.T, c *smartcache.Cache[aliasUser]) {
				assertWildcardRejectedAlias(t, c, prefix)
			})
		})
	}
}

// assertWildcardRejectedPlain evicts prefix on a plain cache backed by a spy store. It fails unless the call
// returns ErrWildcardKeyPrefix and 0, the store is never reached, and both seeded entries still hit.
func assertWildcardRejectedPlain(t *testing.T, prefix string) {
	t.Helper()
	ctx := context.Background()
	ps, ok := memstore.New().(smartcache.PrefixCacheStore)
	if !ok {
		t.Fatal("memstore must implement smartcache.PrefixCacheStore")
	}
	spy := &evictPrefixSpyStore{PrefixCacheStore: ps}
	c := newEvictPrefixPlain(t, spy, "wildcard-plain")
	evictPrefixSeed(t, c, "app-1::a", "app-2::a")

	n, err := c.EvictByKeyPrefix(ctx, prefix)
	if !errors.Is(err, smartcache.ErrWildcardKeyPrefix) {
		t.Errorf("EvictByKeyPrefix(%q) error = %v, want ErrWildcardKeyPrefix", prefix, err)
	}
	if n != 0 {
		t.Errorf("EvictByKeyPrefix(%q) removed %d, want 0", prefix, n)
	}
	if spy.calls != 0 {
		t.Errorf("DeleteByPrefix calls = %d, want 0 for rejected prefix %q", spy.calls, prefix)
	}
	for _, key := range []string{"app-1::a", "app-2::a"} {
		if _, outcome, getErr := c.GetByKey(ctx, key, evictPrefixFailLoader(t)); getErr != nil || outcome != smartcache.Hit {
			t.Errorf("GetByKey(%q) after rejected prefix %q: outcome=%v err=%v, want Hit", key, prefix, outcome, getErr)
		}
	}
}

// assertWildcardRejectedAlias evicts prefix on an alias cache. It fails unless the call returns
// ErrWildcardKeyPrefix and 0, and both seeded records still resolve through their alias.
func assertWildcardRejectedAlias(t *testing.T, c *smartcache.Cache[aliasUser], prefix string) {
	t.Helper()
	ctx := context.Background()
	seedAlias(t, c, "app-1::u1", "Ada", "a1@x.com")
	seedAlias(t, c, "app-2::u1", "Bob", "a2@x.com")

	n, err := c.EvictByKeyPrefix(ctx, prefix)
	if !errors.Is(err, smartcache.ErrWildcardKeyPrefix) {
		t.Errorf("EvictByKeyPrefix(%q) error = %v, want ErrWildcardKeyPrefix", prefix, err)
	}
	if n != 0 {
		t.Errorf("EvictByKeyPrefix(%q) removed %d, want 0", prefix, n)
	}
	for _, want := range []struct{ email, name string }{{"a1@x.com", "Ada"}, {"a2@x.com", "Bob"}} {
		got, outcome, getErr := c.GetByAlias(ctx, smartcache.AliasRef{Field: "email", Value: want.email}, failLoaderUser(t))
		if getErr != nil || outcome != smartcache.Hit || got.Name != want.name {
			t.Errorf("GetByAlias(%q) after rejected prefix %q: outcome=%v err=%v, want Hit", want.email, prefix, outcome, getErr)
		}
	}
}
