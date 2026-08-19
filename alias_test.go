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

func newAliasCache(t *testing.T, mode smartcache.AliasMode) *smartcache.Cache[aliasUser] {
	t.Helper()
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	c, err := smartcache.RegisterAliasGroup[aliasUser](mgr, "user", &smartcache.EntityOptions{TTL: ptr(time.Minute), AliasMode: ptr(mode)})
	if err != nil {
		t.Fatalf("RegisterAliasGroup: %v", err)
	}
	return c
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

// seedAlias caches u under primaryKey with one email alias, failing the test on error.
func seedAlias(t *testing.T, c *smartcache.Cache[aliasUser], primaryKey, name, email string) {
	t.Helper()
	if _, _, err := c.PutAliased(context.Background(), primaryKey, smartcache.AliasRef{Field: "email", Value: email}, writerUser(&aliasUser{ID: primaryKey, Name: name})); err != nil {
		t.Fatalf("PutAliased %s: %v", primaryKey, err)
	}
}

// getMany404Loader is a GetMany loadMissing that supplies only id "404"; it records how many times
// it ran and which keys it was asked for.
func getMany404Loader(calls *int, asked *[]string) func(context.Context, []string) (map[string]*aliasUser, error) {
	return func(_ context.Context, missing []string) (map[string]*aliasUser, error) {
		*calls++
		*asked = append(*asked, missing...)
		out := make(map[string]*aliasUser, len(missing))
		for _, id := range missing {
			if id == "404" {
				out[id] = &aliasUser{ID: "404", Name: "Zoe"}
			}
		}
		return out, nil
	}
}

// failGetMany is a loadMissing that fails the test if invoked (used to prove a cache hit).
func failGetMany(t *testing.T) func(context.Context, []string) (map[string]*aliasUser, error) {
	return func(_ context.Context, missing []string) (map[string]*aliasUser, error) {
		t.Helper()
		t.Errorf("loadMissing must not be called: missing=%v", missing)
		return nil, nil
	}
}

// wantName asserts got[key] is present with the given Name.
func wantName(t *testing.T, got map[string]*aliasUser, key, name string) {
	t.Helper()
	if got[key] == nil || got[key].Name != name {
		t.Errorf("GetMany[%s]: got %+v, want Name=%q", key, got[key], name)
	}
}

// bothModes runs fn for AliasColocated and AliasSharded, proving identical visible results in each
// (memstore is mode-agnostic in one process).
func bothModes(t *testing.T, fn func(t *testing.T, c *smartcache.Cache[aliasUser])) {
	t.Helper()
	for _, m := range []struct {
		name string
		mode smartcache.AliasMode
	}{{"Colocated", smartcache.AliasColocated}, {"Sharded", smartcache.AliasSharded}} {
		t.Run(m.name, func(t *testing.T) { fn(t, newAliasCache(t, m.mode)) })
	}
}

func TestAlias_PutAndReadByPrimaryAndAlias(t *testing.T) {
	bothModes(t, func(t *testing.T, c *smartcache.Cache[aliasUser]) {
		ctx := context.Background()
		if _, _, err := c.PutAliased(ctx, "5", smartcache.AliasRef{Field: "email", Value: "ada@x.com"}, writerUser(&aliasUser{ID: "5", Name: "Ada"})); err != nil {
			t.Fatalf("PutAliased: %v", err)
		}
		if got, oc, err := c.Get(ctx, "5", failLoaderUser(t)); err != nil || oc != smartcache.Hit || got.Name != "Ada" {
			t.Fatalf("Get primary: %+v oc=%v err=%v", got, oc, err)
		}
		if got, oc, err := c.GetByAlias(ctx, smartcache.AliasRef{Field: "email", Value: "ada@x.com"}, failLoaderUser(t)); err != nil || oc != smartcache.Hit || got.Name != "Ada" {
			t.Fatalf("GetByAlias: %+v oc=%v err=%v", got, oc, err)
		}
	})
}

func TestAlias_EvictByAlias_Cascades(t *testing.T) {
	bothModes(t, func(t *testing.T, c *smartcache.Cache[aliasUser]) {
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
			t.Error("primary must miss")
		}
		if _, _, err := c.GetByAlias(ctx, smartcache.AliasRef{Field: "slug", Value: "ada"}, notFoundLoader); !errors.Is(err, smartcache.ErrNotFound) {
			t.Error("slug alias must miss")
		}
	})
}

func TestAlias_EvictPrimary_Cascades(t *testing.T) {
	bothModes(t, func(t *testing.T, c *smartcache.Cache[aliasUser]) {
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
	})
}

func TestAlias_OnePerFieldReplace(t *testing.T) {
	bothModes(t, func(t *testing.T, c *smartcache.Cache[aliasUser]) {
		ctx := context.Background()
		if _, _, err := c.PutAliased(ctx, "5", smartcache.AliasRef{Field: "slug", Value: "ada"}, writerUser(&aliasUser{ID: "5", Name: "Ada"})); err != nil {
			t.Fatalf("PutAliased ada: %v", err)
		}
		if _, _, err := c.PutAliased(ctx, "5", smartcache.AliasRef{Field: "slug", Value: "ada2"}, writerUser(&aliasUser{ID: "5", Name: "Ada"})); err != nil {
			t.Fatalf("PutAliased ada2: %v", err)
		}
		if _, _, err := c.GetByAlias(ctx, smartcache.AliasRef{Field: "slug", Value: "ada"}, notFoundLoader); !errors.Is(err, smartcache.ErrNotFound) {
			t.Error("old slug must be dropped")
		}
		if got, _, err := c.GetByAlias(ctx, smartcache.AliasRef{Field: "slug", Value: "ada2"}, failLoaderUser(t)); err != nil || got.ID != "5" {
			t.Errorf("new slug must resolve: %+v err=%v", got, err)
		}
	})
}

func TestAlias_CrossPrimarySteal(t *testing.T) {
	bothModes(t, func(t *testing.T, c *smartcache.Cache[aliasUser]) {
		ctx := context.Background()
		if _, _, err := c.PutAliased(ctx, "5", smartcache.AliasRef{Field: "email", Value: "shared@x.com"}, writerUser(&aliasUser{ID: "5", Name: "Ada"})); err != nil {
			t.Fatalf("PutAliased 5: %v", err)
		}
		if _, _, err := c.PutAliased(ctx, "9", smartcache.AliasRef{Field: "email", Value: "shared@x.com"}, writerUser(&aliasUser{ID: "9", Name: "Bob"})); err != nil {
			t.Fatalf("PutAliased 9: %v", err)
		}
		if got, _, err := c.GetByAlias(ctx, smartcache.AliasRef{Field: "email", Value: "shared@x.com"}, failLoaderUser(t)); err != nil || got.ID != "9" {
			t.Fatalf("email must resolve to 9: %+v err=%v", got, err)
		}
		if err := c.Evict(ctx, "5"); err != nil {
			t.Fatalf("Evict 5: %v", err)
		}
		if got, _, err := c.GetByAlias(ctx, smartcache.AliasRef{Field: "email", Value: "shared@x.com"}, failLoaderUser(t)); err != nil || got.ID != "9" {
			t.Errorf("email pointer must survive evicting old owner 5: %+v err=%v", got, err)
		}
	})
}

func TestAlias_GetByAlias_ReadThroughRebuild(t *testing.T) {
	bothModes(t, func(t *testing.T, c *smartcache.Cache[aliasUser]) {
		ctx := context.Background()
		calls := 0
		loader := func(context.Context) (*aliasUser, error) {
			calls++
			return &aliasUser{ID: "7", Name: "Grace", Email: "grace@x.com"}, nil
		}
		if got, oc, err := c.GetByAlias(ctx, smartcache.AliasRef{Field: "email", Value: "grace@x.com"}, loader); err != nil || got.ID != "7" || oc != smartcache.Loaded {
			t.Fatalf("cold GetByAlias: %+v oc=%v err=%v", got, oc, err)
		}
		if got, oc, err := c.GetByAlias(ctx, smartcache.AliasRef{Field: "email", Value: "grace@x.com"}, failLoaderUser(t)); err != nil || oc != smartcache.Hit || got.ID != "7" {
			t.Fatalf("warm GetByAlias: %+v oc=%v err=%v", got, oc, err)
		}
		if _, oc, err := c.Get(ctx, "7", failLoaderUser(t)); err != nil || oc != smartcache.Hit {
			t.Fatalf("primary populated: oc=%v err=%v", oc, err)
		}
		if calls != 1 {
			t.Errorf("loader called %d times, want 1", calls)
		}
	})
}

func TestAlias_PutAliasedValue(t *testing.T) {
	bothModes(t, func(t *testing.T, c *smartcache.Cache[aliasUser]) {
		ctx := context.Background()
		if err := c.PutAliasedValue(ctx, "5", smartcache.AliasRef{Field: "email", Value: "ada@x.com"}, &aliasUser{ID: "5", Name: "Ada"}); err != nil {
			t.Fatalf("PutAliasedValue: %v", err)
		}
		if got, _, err := c.GetByAlias(ctx, smartcache.AliasRef{Field: "email", Value: "ada@x.com"}, failLoaderUser(t)); err != nil || got.Name != "Ada" {
			t.Errorf("PutAliasedValue then GetByAlias: %+v err=%v", got, err)
		}
	})
}

// TestAlias_GetMany_PrimaryKeys exercises GetMany on an alias-group cache. GetMany is primary-only:
// every requested key is a primary key, read via a per-key aliasOps.GetValue (no alias resolution,
// no MGET); the misses are batched into one loadMissing call, and each loaded value is populated
// back through the alias store (setValue -> PutValue). It runs in both modes.
func TestAlias_GetMany_PrimaryKeys(t *testing.T) {
	bothModes(t, func(t *testing.T, c *smartcache.Cache[aliasUser]) {
		ctx := context.Background()
		seedAlias(t, c, "5", "Ada", "ada@x.com")
		seedAlias(t, c, "9", "Bob", "bob@x.com")

		var loadCalls int
		var loadedMissing []string
		got, err := c.GetMany(ctx, []string{"5", "9", "404"}, getMany404Loader(&loadCalls, &loadedMissing))
		if err != nil {
			t.Fatalf("GetMany: %v", err)
		}
		// 5 and 9 are cache hits; only 404 falls through to loadMissing, exactly once.
		if loadCalls != 1 || len(loadedMissing) != 1 || loadedMissing[0] != "404" {
			t.Errorf("loadMissing: calls=%d keys=%v, want 1 call for [404]", loadCalls, loadedMissing)
		}
		wantName(t, got, "5", "Ada")
		wantName(t, got, "9", "Bob")
		wantName(t, got, "404", "Zoe")

		// The loaded 404 was populated back through the alias store, so a second GetMany serves it
		// from cache without loading.
		got2, err := c.GetMany(ctx, []string{"404"}, failGetMany(t))
		if err != nil {
			t.Fatalf("second GetMany: %v", err)
		}
		wantName(t, got2, "404", "Zoe")
	})
}

func TestAlias_NotAliasGroup_Errors(t *testing.T) {
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
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
	mgr, err := smartcache.NewManager(&countingStore{CacheStore: memstore.New()})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic when store lacks AliasCacheStore")
		}
	}()
	if _, err := smartcache.RegisterAliasGroup[aliasUser](mgr, "user", nil); err != nil {
		t.Fatalf("RegisterAliasGroup: %v", err)
	}
}

func TestAlias_RegisterAliasGroup_PanicsWhenTNotPrimaryKeyed(t *testing.T) {
	mgr, err := smartcache.NewManager(memstore.New())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic when T is not PrimaryKeyed")
		}
	}()
	if _, err := smartcache.RegisterAliasGroup[sample](mgr, "sample", &smartcache.EntityOptions{TTL: ptr(time.Minute)}); err != nil {
		t.Fatalf("RegisterAliasGroup: %v", err)
	}
}
