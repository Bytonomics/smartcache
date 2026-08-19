package memstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Bytonomics/smartcache"
	"github.com/Bytonomics/smartcache/memstore"
)

func aliasOpsFor(t *testing.T, mode smartcache.AliasMode) smartcache.AliasOps {
	t.Helper()
	acs, ok := memstore.New().(smartcache.AliasCacheStore)
	if !ok {
		t.Fatal("memstore must implement smartcache.AliasCacheStore")
	}
	return acs.AliasGroup("user", mode)
}

func bothModes(t *testing.T, fn func(t *testing.T, o smartcache.AliasOps)) {
	t.Helper()
	for _, m := range []struct {
		name string
		mode smartcache.AliasMode
	}{{"Colocated", smartcache.AliasColocated}, {"Sharded", smartcache.AliasSharded}} {
		t.Run(m.name, func(t *testing.T) { fn(t, aliasOpsFor(t, m.mode)) })
	}
}

func TestMemAlias_PutResolveGet(t *testing.T) {
	bothModes(t, func(t *testing.T, o smartcache.AliasOps) {
		ctx := context.Background()
		if err := o.PutByAlias(ctx, "5", smartcache.AliasRef{Field: "email", Value: "ada"}, []byte("V5"), time.Minute); err != nil {
			t.Fatalf("PutByAlias: %v", err)
		}
		if b, err := o.GetByAlias(ctx, smartcache.AliasRef{Field: "email", Value: "ada"}); err != nil || string(b) != "V5" {
			t.Fatalf("GetByAlias: %q err=%v", b, err)
		}
		if b, err := o.GetValue(ctx, "5"); err != nil || string(b) != "V5" {
			t.Fatalf("GetValue: %q err=%v", b, err)
		}
	})
}

func TestMemAlias_GetByAlias_Miss(t *testing.T) {
	bothModes(t, func(t *testing.T, o smartcache.AliasOps) {
		if _, err := o.GetByAlias(context.Background(), smartcache.AliasRef{Field: "email", Value: "none"}); !errors.Is(err, smartcache.ErrStoreMiss) {
			t.Errorf("want ErrStoreMiss, got %v", err)
		}
	})
}

func TestMemAlias_EvictByPrimary(t *testing.T) {
	bothModes(t, func(t *testing.T, o smartcache.AliasOps) {
		ctx := context.Background()
		if err := o.PutByAlias(ctx, "5", smartcache.AliasRef{Field: "email", Value: "ada"}, []byte("V5"), time.Minute); err != nil {
			t.Fatalf("PutByAlias: %v", err)
		}
		if err := o.EvictByPrimary(ctx, "5"); err != nil {
			t.Fatalf("EvictByPrimary: %v", err)
		}
		if _, err := o.GetByAlias(ctx, smartcache.AliasRef{Field: "email", Value: "ada"}); !errors.Is(err, smartcache.ErrStoreMiss) {
			t.Errorf("alias must miss, got %v", err)
		}
	})
}

func TestMemAlias_EvictByAlias(t *testing.T) {
	bothModes(t, func(t *testing.T, o smartcache.AliasOps) {
		ctx := context.Background()
		if err := o.PutByAlias(ctx, "5", smartcache.AliasRef{Field: "email", Value: "ada"}, []byte("V5"), time.Minute); err != nil {
			t.Fatalf("PutByAlias: %v", err)
		}
		if err := o.EvictByAlias(ctx, smartcache.AliasRef{Field: "email", Value: "ada"}); err != nil {
			t.Fatalf("EvictByAlias: %v", err)
		}
		if _, err := o.GetValue(ctx, "5"); !errors.Is(err, smartcache.ErrStoreMiss) {
			t.Errorf("value must miss, got %v", err)
		}
	})
}

func TestMemAlias_OnePerFieldReplace(t *testing.T) {
	bothModes(t, func(t *testing.T, o smartcache.AliasOps) {
		ctx := context.Background()
		if err := o.PutByAlias(ctx, "5", smartcache.AliasRef{Field: "slug", Value: "ada"}, []byte("V5"), time.Minute); err != nil {
			t.Fatalf("PutByAlias ada: %v", err)
		}
		if err := o.PutByAlias(ctx, "5", smartcache.AliasRef{Field: "slug", Value: "ada2"}, []byte("V5"), time.Minute); err != nil {
			t.Fatalf("PutByAlias ada2: %v", err)
		}
		if _, err := o.GetByAlias(ctx, smartcache.AliasRef{Field: "slug", Value: "ada"}); !errors.Is(err, smartcache.ErrStoreMiss) {
			t.Errorf("old slug must be dropped, got %v", err)
		}
		if b, err := o.GetByAlias(ctx, smartcache.AliasRef{Field: "slug", Value: "ada2"}); err != nil || string(b) != "V5" {
			t.Errorf("new slug must resolve: %q err=%v", b, err)
		}
	})
}

func TestMemAlias_Steal(t *testing.T) {
	bothModes(t, func(t *testing.T, o smartcache.AliasOps) {
		ctx := context.Background()
		if err := o.PutByAlias(ctx, "5", smartcache.AliasRef{Field: "email", Value: "x"}, []byte("V5"), time.Minute); err != nil {
			t.Fatalf("PutByAlias 5: %v", err)
		}
		if err := o.PutByAlias(ctx, "9", smartcache.AliasRef{Field: "email", Value: "x"}, []byte("V9"), time.Minute); err != nil {
			t.Fatalf("PutByAlias 9: %v", err)
		}
		if b, err := o.GetByAlias(ctx, smartcache.AliasRef{Field: "email", Value: "x"}); err != nil || string(b) != "V9" {
			t.Fatalf("email must resolve to 9's value: %q err=%v", b, err)
		}
		if err := o.EvictByPrimary(ctx, "5"); err != nil {
			t.Fatalf("EvictByPrimary: %v", err)
		}
		if b, err := o.GetByAlias(ctx, smartcache.AliasRef{Field: "email", Value: "x"}); err != nil || string(b) != "V9" {
			t.Errorf("email pointer must survive evicting 5: %q err=%v", b, err)
		}
	})
}

func TestMemAlias_TTLExpiry(t *testing.T) {
	bothModes(t, func(t *testing.T, o smartcache.AliasOps) {
		ctx := context.Background()
		if err := o.PutByAlias(ctx, "5", smartcache.AliasRef{Field: "email", Value: "ada"}, []byte("V5"), 30*time.Millisecond); err != nil {
			t.Fatalf("PutByAlias: %v", err)
		}
		if _, err := o.GetByAlias(ctx, smartcache.AliasRef{Field: "email", Value: "ada"}); err != nil {
			t.Fatalf("immediate: %v", err)
		}
		time.Sleep(80 * time.Millisecond)
		if _, err := o.GetByAlias(ctx, smartcache.AliasRef{Field: "email", Value: "ada"}); !errors.Is(err, smartcache.ErrStoreMiss) {
			t.Errorf("must expire, got %v", err)
		}
	})
}
