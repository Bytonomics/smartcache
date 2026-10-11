package memstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Bytonomics/smartcache"
	"github.com/Bytonomics/smartcache/internal/keyspace"
	"github.com/Bytonomics/smartcache/memstore"
)

// newAliasStore returns a fresh memstore viewed as a smartcache.AliasCacheStore.
func newAliasStore(t *testing.T) smartcache.AliasCacheStore {
	t.Helper()
	acs, ok := memstore.New().(smartcache.AliasCacheStore)
	if !ok {
		t.Fatal("memstore must implement smartcache.AliasCacheStore")
	}
	return acs
}

func aliasOpsFor(t *testing.T, mode smartcache.AliasMode) smartcache.AliasOps {
	t.Helper()
	return newAliasStore(t).AliasGroup("user", mode)
}

// aliasModes lists both AliasMode values so every alias test runs against each.
var aliasModes = []struct {
	name string
	mode smartcache.AliasMode
}{{"Colocated", smartcache.AliasColocated}, {"Sharded", smartcache.AliasSharded}}

func bothModes(t *testing.T, fn func(t *testing.T, o smartcache.AliasOps)) {
	t.Helper()
	for _, m := range aliasModes {
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

// emailRef returns the email alias that putAliasRecord stores for primary.
func emailRef(primary string) smartcache.AliasRef {
	return smartcache.AliasRef{Field: "email", Value: "email:" + primary}
}

// prefixOpsFor returns o viewed as a smartcache.PrefixAliasOps, failing the test if it is not one.
func prefixOpsFor(t *testing.T, o smartcache.AliasOps) smartcache.PrefixAliasOps {
	t.Helper()
	po, ok := o.(smartcache.PrefixAliasOps)
	if !ok {
		t.Fatal("memstore alias ops must implement smartcache.PrefixAliasOps")
	}
	return po
}

// putAliasRecord stores primary's value under its email alias, failing the test on error.
func putAliasRecord(t *testing.T, o smartcache.AliasOps, primary string, ttl time.Duration) {
	t.Helper()
	if err := o.PutByAlias(context.Background(), primary, emailRef(primary), []byte("V:"+primary), ttl); err != nil {
		t.Fatalf("PutByAlias %q: %v", primary, err)
	}
}

// assertAliasMiss fails unless both the value and the alias of primary miss.
func assertAliasMiss(t *testing.T, o smartcache.AliasOps, primary string) {
	t.Helper()
	ctx := context.Background()
	if _, err := o.GetValue(ctx, primary); !errors.Is(err, smartcache.ErrStoreMiss) {
		t.Errorf("GetValue(%q) must miss, got %v", primary, err)
	}
	if _, err := o.GetByAlias(ctx, emailRef(primary)); !errors.Is(err, smartcache.ErrStoreMiss) {
		t.Errorf("GetByAlias(%q) must miss, got %v", primary, err)
	}
}

// assertAliasHit fails unless both the value and the alias of primary resolve to its stored value.
func assertAliasHit(t *testing.T, o smartcache.AliasOps, primary string) {
	t.Helper()
	ctx := context.Background()
	want := "V:" + primary
	if b, err := o.GetValue(ctx, primary); err != nil || string(b) != want {
		t.Errorf("GetValue(%q): want %q, got %q err=%v", primary, want, b, err)
	}
	if b, err := o.GetByAlias(ctx, emailRef(primary)); err != nil || string(b) != want {
		t.Errorf("GetByAlias(%q): want %q, got %q err=%v", primary, want, b, err)
	}
}

// TestMemAlias_EvictByPrimaryPrefix_Table verifies prefix eviction in both modes: matched records
// (value, members hash and alias all removed) are counted, and every other record keeps resolving.
func TestMemAlias_EvictByPrimaryPrefix_Table(t *testing.T) {
	cases := []struct {
		name      string
		primaries []string
		prefix    string
		wantCount int64
		wantGone  []string
		wantKept  []string
	}{
		{
			name:      "matching subset is evicted and counted",
			primaries: []string{"app-1::u1", "app-1::u2", "app-2::u1"},
			prefix:    "app-1::",
			wantCount: 2,
			wantGone:  []string{"app-1::u1", "app-1::u2"},
			wantKept:  []string{"app-2::u1"},
		},
		{
			name:      "no match returns zero",
			primaries: []string{"app-1::u1"},
			prefix:    "app-9::",
			wantCount: 0,
			wantKept:  []string{"app-1::u1"},
		},
		{
			name:      "empty store returns zero",
			prefix:    "app-1::",
			wantCount: 0,
		},
		{
			name:      "primary containing :: is matched by its full text",
			primaries: []string{"app-1::team::u9", "app-1::u1"},
			prefix:    "app-1::team::",
			wantCount: 1,
			wantGone:  []string{"app-1::team::u9"},
			wantKept:  []string{"app-1::u1"},
		},
		{
			name:      "prefix is literal, not a glob",
			primaries: []string{"a*x", "axx"},
			prefix:    "a*",
			wantCount: 1,
			wantGone:  []string{"a*x"},
			wantKept:  []string{"axx"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bothModes(t, func(t *testing.T, o smartcache.AliasOps) {
				ctx := context.Background()
				for _, pk := range tc.primaries {
					putAliasRecord(t, o, pk, time.Minute)
				}

				got, err := prefixOpsFor(t, o).EvictByPrimaryPrefix(ctx, tc.prefix)
				if err != nil {
					t.Fatalf("EvictByPrimaryPrefix: %v", err)
				}
				if got != tc.wantCount {
					t.Errorf("count: want %d, got %d", tc.wantCount, got)
				}
				for _, pk := range tc.wantGone {
					assertAliasMiss(t, o, pk)
				}
				for _, pk := range tc.wantKept {
					assertAliasHit(t, o, pk)
				}
			})
		})
	}
}

// TestMemAlias_EvictByPrimaryPrefix_CountsMembersOnlyRecord verifies that a record whose value was
// dropped through the store's own Delete (leaving its members hash and alias pointer) is still
// counted and fully cleaned, including its alias pointer.
func TestMemAlias_EvictByPrimaryPrefix_CountsMembersOnlyRecord(t *testing.T) {
	for _, m := range aliasModes {
		t.Run(m.name, func(t *testing.T) {
			ctx := context.Background()
			sharded := m.mode == smartcache.AliasSharded
			s := newAliasStore(t)
			o := s.AliasGroup("user", m.mode)
			po := prefixOpsFor(t, o)
			putAliasRecord(t, o, "app-1::u1", time.Minute)

			ptr := keyspace.PointerKey("user", "email", "email:app-1::u1", sharded)
			if ok, err := s.Exists(ctx, ptr); err != nil || !ok {
				t.Fatalf("alias pointer must exist before eviction: ok=%v err=%v", ok, err)
			}
			if err := s.Delete(ctx, keyspace.ValueKey("user", "app-1::u1", sharded)); err != nil {
				t.Fatalf("Delete value key: %v", err)
			}

			got, err := po.EvictByPrimaryPrefix(ctx, "app-1::")
			if err != nil {
				t.Fatalf("EvictByPrimaryPrefix: %v", err)
			}
			if got != 1 {
				t.Fatalf("members-only record must be counted: want 1, got %d", got)
			}
			if ok, err := s.Exists(ctx, ptr); err != nil || ok {
				t.Errorf("alias pointer must be cascaded away: ok=%v err=%v", ok, err)
			}
			if got, err := po.EvictByPrimaryPrefix(ctx, "app-1::"); err != nil || got != 0 {
				t.Errorf("second call: want 0 records, got %d err=%v", got, err)
			}
		})
	}
}

// TestMemAlias_EvictByPrimaryPrefix_CountsExpiredRecord verifies that an expired record under the
// prefix is counted and removed, because eviction does not require an unexpired value. The second
// call returning zero shows the expired record was physically removed.
func TestMemAlias_EvictByPrimaryPrefix_CountsExpiredRecord(t *testing.T) {
	bothModes(t, func(t *testing.T, o smartcache.AliasOps) {
		ctx := context.Background()
		po := prefixOpsFor(t, o)
		putAliasRecord(t, o, "app-1::u1", 30*time.Millisecond)
		time.Sleep(80 * time.Millisecond)

		got, err := po.EvictByPrimaryPrefix(ctx, "app-1::")
		if err != nil {
			t.Fatalf("EvictByPrimaryPrefix: %v", err)
		}
		if got != 1 {
			t.Fatalf("expired record must be counted: want 1, got %d", got)
		}
		if got, err := po.EvictByPrimaryPrefix(ctx, "app-1::"); err != nil || got != 0 {
			t.Errorf("second call: want 0 records, got %d err=%v", got, err)
		}
	})
}

// TestMemAlias_EvictByPrimaryPrefix_IgnoresOtherNamespaces verifies that eviction in namespace "user"
// neither counts nor touches records of namespace "users", whose name starts with the same text.
func TestMemAlias_EvictByPrimaryPrefix_IgnoresOtherNamespaces(t *testing.T) {
	for _, m := range aliasModes {
		t.Run(m.name, func(t *testing.T) {
			s := newAliasStore(t)
			user := s.AliasGroup("user", m.mode)
			users := s.AliasGroup("users", m.mode)
			putAliasRecord(t, user, "app-1::u1", time.Minute)
			putAliasRecord(t, users, "app-1::u1", time.Minute)
			putAliasRecord(t, users, "app-1::u2", time.Minute)

			got, err := prefixOpsFor(t, user).EvictByPrimaryPrefix(context.Background(), "app-1::")
			if err != nil {
				t.Fatalf("EvictByPrimaryPrefix: %v", err)
			}
			if got != 1 {
				t.Fatalf("want 1 record in namespace user, got %d", got)
			}
			assertAliasMiss(t, user, "app-1::u1")
			assertAliasHit(t, users, "app-1::u1")
			assertAliasHit(t, users, "app-1::u2")
		})
	}
}
