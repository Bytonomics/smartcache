package memstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Bytonomics/smartcache"
	"github.com/Bytonomics/smartcache/memstore"
)

func newAliasStore(t *testing.T) smartcache.AliasCacheStore {
	t.Helper()
	as, ok := memstore.New().(smartcache.AliasCacheStore)
	if !ok {
		t.Fatal("memstore must implement smartcache.AliasCacheStore")
	}
	return as
}

func userSpec(primary, field, value string, val []byte, ttl time.Duration) *smartcache.AliasWriteSpec {
	s := &smartcache.AliasWriteSpec{
		ValueKey:         "bc:{user}:" + primary,
		MembersKey:       "bc:memb:{user}:" + primary,
		ValueKeyPrefix:   "bc:{user}:",
		MembersKeyPrefix: "bc:memb:{user}:",
		Value:            val,
		TTL:              ttl,
	}
	if field != "" {
		s.PointerKey = "bc:grp:{user}:" + field + ":" + value
		s.FieldPrefix = "bc:grp:{user}:" + field + ":"
	}
	return s
}

func TestMemAlias_PutResolveGet(t *testing.T) {
	as := newAliasStore(t)
	ctx := context.Background()
	if err := as.PutByAlias(ctx, userSpec("5", "email", "ada", []byte("VAL5"), time.Minute)); err != nil {
		t.Fatalf("PutByAlias: %v", err)
	}
	got, err := as.GetByAlias(ctx, "bc:grp:{user}:email:ada")
	if err != nil || string(got) != "VAL5" {
		t.Fatalf("GetByAlias: got %q err=%v", got, err)
	}
}

func TestMemAlias_GetByAlias_Miss(t *testing.T) {
	as := newAliasStore(t)
	if _, err := as.GetByAlias(context.Background(), "bc:grp:{user}:email:none"); !errors.Is(err, smartcache.ErrStoreMiss) {
		t.Errorf("want ErrStoreMiss, got %v", err)
	}
}

func TestMemAlias_EvictByPrimary(t *testing.T) {
	as := newAliasStore(t)
	ctx := context.Background()
	if err := as.PutByAlias(ctx, userSpec("5", "email", "ada", []byte("VAL5"), time.Minute)); err != nil {
		t.Fatalf("PutByAlias: %v", err)
	}
	if err := as.EvictByPrimary(ctx, "bc:{user}:5", "bc:memb:{user}:5"); err != nil {
		t.Fatalf("EvictByPrimary: %v", err)
	}
	if _, err := as.GetByAlias(ctx, "bc:grp:{user}:email:ada"); !errors.Is(err, smartcache.ErrStoreMiss) {
		t.Errorf("alias must miss after EvictByPrimary, got %v", err)
	}
}

func TestMemAlias_EvictByAlias(t *testing.T) {
	as := newAliasStore(t)
	ctx := context.Background()
	if err := as.PutByAlias(ctx, userSpec("5", "email", "ada", []byte("VAL5"), time.Minute)); err != nil {
		t.Fatalf("PutByAlias: %v", err)
	}
	if err := as.EvictByAlias(ctx, "bc:grp:{user}:email:ada", "bc:{user}:", "bc:memb:{user}:"); err != nil {
		t.Fatalf("EvictByAlias: %v", err)
	}
	if _, err := as.GetByAlias(ctx, "bc:grp:{user}:email:ada"); !errors.Is(err, smartcache.ErrStoreMiss) {
		t.Errorf("alias must miss after EvictByAlias, got %v", err)
	}
}

func TestMemAlias_OnePerFieldReplace(t *testing.T) {
	as := newAliasStore(t)
	ctx := context.Background()
	if err := as.PutByAlias(ctx, userSpec("5", "slug", "ada", []byte("VAL5"), time.Minute)); err != nil {
		t.Fatalf("PutByAlias ada: %v", err)
	}
	if err := as.PutByAlias(ctx, userSpec("5", "slug", "ada2", []byte("VAL5"), time.Minute)); err != nil {
		t.Fatalf("PutByAlias ada2: %v", err)
	}
	if _, err := as.GetByAlias(ctx, "bc:grp:{user}:slug:ada"); !errors.Is(err, smartcache.ErrStoreMiss) {
		t.Errorf("old slug pointer must be dropped, got %v", err)
	}
	if got, err := as.GetByAlias(ctx, "bc:grp:{user}:slug:ada2"); err != nil || string(got) != "VAL5" {
		t.Errorf("new slug must resolve: %q err=%v", got, err)
	}
}

func TestMemAlias_TTLExpiry(t *testing.T) {
	as := newAliasStore(t)
	ctx := context.Background()
	if err := as.PutByAlias(ctx, userSpec("5", "email", "ada", []byte("VAL5"), 30*time.Millisecond)); err != nil {
		t.Fatalf("PutByAlias: %v", err)
	}
	if _, err := as.GetByAlias(ctx, "bc:grp:{user}:email:ada"); err != nil {
		t.Fatalf("immediate GetByAlias: %v", err)
	}
	time.Sleep(80 * time.Millisecond)
	if _, err := as.GetByAlias(ctx, "bc:grp:{user}:email:ada"); !errors.Is(err, smartcache.ErrStoreMiss) {
		t.Errorf("alias must expire, got %v", err)
	}
}
