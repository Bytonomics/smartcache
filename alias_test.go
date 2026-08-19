package smartcache_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Bytonomics/smartcache"
	"github.com/Bytonomics/smartcache/memstore"
)

// aliasUser is a PrimaryKeyed test value; its primary key is ID.
type aliasUser struct {
	ID    string
	Name  string
	Email string
}

func (u aliasUser) CachePrimaryKey() string { return u.ID }

func newAliasCache(t *testing.T) *smartcache.Cache[aliasUser] {
	t.Helper()
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	c, err := smartcache.RegisterAliasGroup[aliasUser](mgr, "user", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("RegisterAliasGroup: %v", err)
	}
	return c
}

// recordingAliasStore wraps a real AliasCacheStore and counts PutByAlias calls, so a test
// can prove a write path routes through the alias-aware store rather than bypassing it via
// a plain CacheStore.Set (which would silently skip refreshing sibling pointer/members TTLs).
type recordingAliasStore struct {
	smartcache.AliasCacheStore
	putByAliasCalls int
}

func (s *recordingAliasStore) PutByAlias(ctx context.Context, spec *smartcache.AliasWriteSpec) error {
	s.putByAliasCalls++
	return s.AliasCacheStore.PutByAlias(ctx, spec)
}

func failLoaderUser(t *testing.T) smartcache.Loader[aliasUser] {
	return func(context.Context) (*aliasUser, error) {
		t.Helper()
		t.Fatal("loader must not be called on a cache hit")
		return nil, nil
	}
}

func writerUser(u *aliasUser) smartcache.Writer[aliasUser] {
	return func(context.Context) (*aliasUser, error) { return u, nil }
}

func notFoundLoader(context.Context) (*aliasUser, error) { return nil, smartcache.ErrNotFound }

func TestAlias_PutAndReadByPrimaryAndAlias(t *testing.T) {
	c := newAliasCache(t)
	ctx := context.Background()
	if _, _, err := c.PutAliased(ctx, "5", smartcache.AliasRef{Field: "email", Value: "ada@x.com"}, writerUser(&aliasUser{ID: "5", Name: "Ada"})); err != nil {
		t.Fatalf("PutAliased: %v", err)
	}
	got, oc, err := c.Get(ctx, "5", failLoaderUser(t))
	if err != nil || oc != smartcache.Hit || got.Name != "Ada" {
		t.Fatalf("Get primary: %+v oc=%v err=%v", got, oc, err)
	}
	got, oc, err = c.GetByAlias(ctx, smartcache.AliasRef{Field: "email", Value: "ada@x.com"}, failLoaderUser(t))
	if err != nil || oc != smartcache.Hit || got.Name != "Ada" {
		t.Fatalf("GetByAlias: %+v oc=%v err=%v", got, oc, err)
	}
}

func TestAlias_EvictByAlias_CascadesAllViews(t *testing.T) {
	c := newAliasCache(t)
	ctx := context.Background()
	if _, _, err := c.PutAliased(ctx, "5", smartcache.AliasRef{Field: "email", Value: "ada@x.com"}, writerUser(&aliasUser{ID: "5", Name: "Ada"})); err != nil {
		t.Fatalf("PutAliased email: %v", err)
	}
	if _, _, err := c.PutAliased(ctx, "5", smartcache.AliasRef{Field: "slug", Value: "ada"}, writerUser(&aliasUser{ID: "5", Name: "Ada"})); err != nil {
		t.Fatalf("PutAliased slug: %v", err)
	}
	if err := c.EvictByAlias(ctx, smartcache.AliasRef{Field: "email", Value: "ada@x.com"}); err != nil {
		t.Fatalf("EvictByAlias: %v", err)
	}
	if _, _, err := c.Get(ctx, "5", notFoundLoader); !errors.Is(err, smartcache.ErrNotFound) {
		t.Error("primary must miss after cascade evict")
	}
	if _, _, err := c.GetByAlias(ctx, smartcache.AliasRef{Field: "slug", Value: "ada"}, notFoundLoader); !errors.Is(err, smartcache.ErrNotFound) {
		t.Error("slug alias must miss after cascade evict")
	}
}

func TestAlias_EvictPrimary_CascadesAliases(t *testing.T) {
	c := newAliasCache(t)
	ctx := context.Background()
	if _, _, err := c.PutAliased(ctx, "5", smartcache.AliasRef{Field: "email", Value: "ada@x.com"}, writerUser(&aliasUser{ID: "5", Name: "Ada"})); err != nil {
		t.Fatalf("PutAliased: %v", err)
	}
	if err := c.Evict(ctx, "5"); err != nil {
		t.Fatalf("Evict: %v", err)
	}
	if _, _, err := c.GetByAlias(ctx, smartcache.AliasRef{Field: "email", Value: "ada@x.com"}, notFoundLoader); !errors.Is(err, smartcache.ErrNotFound) {
		t.Error("alias must miss after primary evict")
	}
}

func TestAlias_OnePerFieldReplace(t *testing.T) {
	c := newAliasCache(t)
	ctx := context.Background()
	if _, _, err := c.PutAliased(ctx, "5", smartcache.AliasRef{Field: "slug", Value: "ada"}, writerUser(&aliasUser{ID: "5", Name: "Ada"})); err != nil {
		t.Fatalf("PutAliased ada: %v", err)
	}
	if _, _, err := c.PutAliased(ctx, "5", smartcache.AliasRef{Field: "slug", Value: "ada2"}, writerUser(&aliasUser{ID: "5", Name: "Ada"})); err != nil {
		t.Fatalf("PutAliased ada2: %v", err)
	}
	if _, _, err := c.GetByAlias(ctx, smartcache.AliasRef{Field: "slug", Value: "ada"}, notFoundLoader); !errors.Is(err, smartcache.ErrNotFound) {
		t.Error("old slug value must be dropped by one-per-field replace")
	}
	got, _, err := c.GetByAlias(ctx, smartcache.AliasRef{Field: "slug", Value: "ada2"}, failLoaderUser(t))
	if err != nil || got.ID != "5" {
		t.Errorf("new slug must resolve: %+v err=%v", got, err)
	}
}

func TestAlias_CrossPrimarySteal(t *testing.T) {
	c := newAliasCache(t)
	ctx := context.Background()
	if _, _, err := c.PutAliased(ctx, "5", smartcache.AliasRef{Field: "email", Value: "shared@x.com"}, writerUser(&aliasUser{ID: "5", Name: "Ada"})); err != nil {
		t.Fatalf("PutAliased 5: %v", err)
	}
	if _, _, err := c.PutAliased(ctx, "9", smartcache.AliasRef{Field: "email", Value: "shared@x.com"}, writerUser(&aliasUser{ID: "9", Name: "Bob"})); err != nil {
		t.Fatalf("PutAliased 9: %v", err)
	}
	got, _, err := c.GetByAlias(ctx, smartcache.AliasRef{Field: "email", Value: "shared@x.com"}, failLoaderUser(t))
	if err != nil || got.ID != "9" {
		t.Fatalf("email must resolve to new owner 9: %+v err=%v", got, err)
	}
	if err := c.Evict(ctx, "5"); err != nil {
		t.Fatalf("Evict 5: %v", err)
	}
	got, _, err = c.GetByAlias(ctx, smartcache.AliasRef{Field: "email", Value: "shared@x.com"}, failLoaderUser(t))
	if err != nil || got.ID != "9" {
		t.Errorf("email pointer must survive eviction of old owner 5 (steal cleanup): %+v err=%v", got, err)
	}
}

func TestAlias_GetByAlias_ReadThroughRebuild(t *testing.T) {
	c := newAliasCache(t)
	ctx := context.Background()
	calls := 0
	loader := func(context.Context) (*aliasUser, error) {
		calls++
		return &aliasUser{ID: "7", Name: "Grace", Email: "grace@x.com"}, nil
	}
	got, oc, err := c.GetByAlias(ctx, smartcache.AliasRef{Field: "email", Value: "grace@x.com"}, loader)
	if err != nil || got.ID != "7" || oc != smartcache.Loaded {
		t.Fatalf("cold GetByAlias: %+v oc=%v err=%v", got, oc, err)
	}
	got, oc, err = c.GetByAlias(ctx, smartcache.AliasRef{Field: "email", Value: "grace@x.com"}, failLoaderUser(t))
	if err != nil || oc != smartcache.Hit || got.ID != "7" {
		t.Fatalf("warm GetByAlias: %+v oc=%v err=%v", got, oc, err)
	}
	if _, oc, err := c.Get(ctx, "7", failLoaderUser(t)); err != nil || oc != smartcache.Hit {
		t.Fatalf("primary populated by alias rebuild: oc=%v err=%v", oc, err)
	}
	if calls != 1 {
		t.Errorf("loader called %d times, want 1", calls)
	}
}

func TestAlias_PutAliasedValue(t *testing.T) {
	c := newAliasCache(t)
	ctx := context.Background()
	if err := c.PutAliasedValue(ctx, "5", smartcache.AliasRef{Field: "email", Value: "ada@x.com"}, &aliasUser{ID: "5", Name: "Ada"}); err != nil {
		t.Fatalf("PutAliasedValue: %v", err)
	}
	got, _, err := c.GetByAlias(ctx, smartcache.AliasRef{Field: "email", Value: "ada@x.com"}, failLoaderUser(t))
	if err != nil || got.Name != "Ada" {
		t.Errorf("PutAliasedValue then GetByAlias: %+v err=%v", got, err)
	}
}

// TestAlias_GetMany_PopulatesViaAliasStore guards against populateLoaded bypassing the
// alias-aware write path: on an alias-group cache, GetMany's cold-miss populate must go
// through AliasCacheStore.PutByAlias (which refreshes every existing sibling pointer and
// members-set TTL), not a plain CacheStore.Set that only writes the value key. A regression
// here would leave a pointer free to expire before its value, so GetByAlias could miss while
// Get still hits.
func TestAlias_GetMany_PopulatesViaAliasStore(t *testing.T) {
	backing, ok := memstore.New().(smartcache.AliasCacheStore)
	if !ok {
		t.Fatal("memstore must implement smartcache.AliasCacheStore")
	}
	rec := &recordingAliasStore{AliasCacheStore: backing}
	mgr, err := smartcache.NewManager(rec)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	c, err := smartcache.RegisterAliasGroup[aliasUser](mgr, "user", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("RegisterAliasGroup: %v", err)
	}

	ctx := context.Background()
	got, err := c.GetMany(ctx, []string{"5"}, func(context.Context, []string) (map[string]*aliasUser, error) {
		return map[string]*aliasUser{"5": {ID: "5", Name: "Ada"}}, nil
	})
	if err != nil {
		t.Fatalf("GetMany: %v", err)
	}
	if got["5"] == nil || got["5"].Name != "Ada" {
		t.Fatalf("GetMany result: %+v", got)
	}
	if rec.putByAliasCalls != 1 {
		t.Errorf("populateLoaded must write via PutByAlias on an alias-group cache: got %d calls, want 1", rec.putByAliasCalls)
	}
}

func TestAlias_NotAliasGroup_Errors(t *testing.T) {
	mgr, _ := smartcache.NewManager(memstore.New())
	c, err := smartcache.Register[aliasUser](mgr, "plain", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	ctx := context.Background()
	if _, _, err := c.GetByAlias(ctx, smartcache.AliasRef{Field: "email", Value: "x"}, failLoaderUser(t)); !errors.Is(err, smartcache.ErrNotAliasGroup) {
		t.Errorf("GetByAlias: want ErrNotAliasGroup, got %v", err)
	}
	if _, _, err := c.PutAliased(ctx, "5", smartcache.AliasRef{Field: "email", Value: "x"}, writerUser(&aliasUser{ID: "5"})); !errors.Is(err, smartcache.ErrNotAliasGroup) {
		t.Errorf("PutAliased: want ErrNotAliasGroup, got %v", err)
	}
	if err := c.PutAliasedValue(ctx, "5", smartcache.AliasRef{Field: "email", Value: "x"}, &aliasUser{ID: "5"}); !errors.Is(err, smartcache.ErrNotAliasGroup) {
		t.Errorf("PutAliasedValue: want ErrNotAliasGroup, got %v", err)
	}
	if err := c.EvictByAlias(ctx, smartcache.AliasRef{Field: "email", Value: "x"}); !errors.Is(err, smartcache.ErrNotAliasGroup) {
		t.Errorf("EvictByAlias: want ErrNotAliasGroup, got %v", err)
	}
}

func TestAlias_RegisterAliasGroup_PanicsWhenStoreNotAlias(t *testing.T) {
	mgr, _ := smartcache.NewManager(&countingStore{CacheStore: memstore.New()})
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic when store lacks AliasCacheStore")
		}
	}()
	_, _ = smartcache.RegisterAliasGroup[aliasUser](mgr, "user", nil)
}

func TestAlias_RegisterAliasGroup_PanicsWhenTNotPrimaryKeyed(t *testing.T) {
	mgr, _ := smartcache.NewManager(memstore.New())
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic when T is not PrimaryKeyed")
		}
	}()
	_, _ = smartcache.RegisterAliasGroup[sample](mgr, "sample", &smartcache.EntityOptions{TTL: ptr(time.Minute)})
}
