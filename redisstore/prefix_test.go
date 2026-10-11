package redisstore_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/Bytonomics/smartcache"
	"github.com/Bytonomics/smartcache/internal/keyspace"
	"github.com/Bytonomics/smartcache/redisstore"
)

// scanFake embeds *fakeConn and implements redisstore.KeyScanner. For each match pattern it replays
// the configured batches to fn, then returns the configured error for that pattern. It records every
// pattern requested and every key deleted. Eval calls are recorded by the embedded fakeConn.
type scanFake struct {
	*fakeConn
	batches  map[string][][]string
	scanErrs map[string]error
	delErrs  map[string]error
	evalErrs map[string]error
	matches  []string
	dels     []string
}

var _ redisstore.KeyScanner = (*scanFake)(nil)

func (s *scanFake) ScanKeys(_ context.Context, match string, fn func(keys []string) error) error {
	s.matches = append(s.matches, match)
	for _, batch := range s.batches[match] {
		if err := fn(batch); err != nil {
			return err
		}
	}
	return s.scanErrs[match]
}

func (s *scanFake) Del(ctx context.Context, keys ...string) *redis.IntCmd {
	s.dels = append(s.dels, keys...)
	cmd := redis.NewIntCmd(ctx)
	if err := s.delErrs[keys[0]]; err != nil {
		cmd.SetErr(err)
		return cmd
	}
	cmd.SetVal(1)
	return cmd
}

// Eval records the call through the embedded fakeConn, then fails it when evalErrs is set for keys[0].
func (s *scanFake) Eval(ctx context.Context, script string, keys []string, args ...any) *redis.Cmd {
	cmd := s.fakeConn.Eval(ctx, script, keys, args...)
	if err := s.evalErrs[keys[0]]; err != nil {
		failed := redis.NewCmd(ctx)
		failed.SetErr(err)
		return failed
	}
	return cmd
}

// delRecorder embeds *fakeConn and records every Del key. It does not implement
// redisstore.KeyScanner, so it stands in for a connection that cannot scan keys.
type delRecorder struct {
	*fakeConn
	dels []string
}

func (d *delRecorder) Del(ctx context.Context, keys ...string) *redis.IntCmd {
	d.dels = append(d.dels, keys...)
	cmd := redis.NewIntCmd(ctx)
	cmd.SetVal(int64(len(keys)))
	return cmd
}

// scanPage is one SCAN reply: its keys, the cursor it returns, and an optional error.
type scanPage struct {
	keys []string
	next uint64
	err  error
}

// scanNodeFake answers Scan from pages keyed by the cursor it receives, and records every cursor
// and match pattern it sees.
type scanNodeFake struct {
	pages   map[uint64]scanPage
	cursors []uint64
	matches []string
}

func (f *scanNodeFake) Scan(ctx context.Context, cursor uint64, match string, _ int64) *redis.ScanCmd {
	f.cursors = append(f.cursors, cursor)
	f.matches = append(f.matches, match)
	page := f.pages[cursor]
	cmd := redis.NewScanCmd(ctx, func(context.Context, redis.Cmder) error { return nil })
	cmd.SetVal(page.keys, page.next)
	cmd.SetErr(page.err)
	return cmd
}

// scanUniversal embeds a nil redis.UniversalClient, so only the methods defined here exist. ScanAll
// on a non-cluster client calls nothing but Scan, so overriding Scan is enough.
type scanUniversal struct {
	redis.UniversalClient
	node *scanNodeFake
}

func (u *scanUniversal) Scan(ctx context.Context, cursor uint64, match string, count int64) *redis.ScanCmd {
	return u.node.Scan(ctx, cursor, match, count)
}

// equalBatches reports whether got and want hold the same batches in the same order.
func equalBatches(got, want [][]string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if !slices.Equal(got[i], want[i]) {
			return false
		}
	}
	return true
}

// flatten concatenates batches into one slice of keys, in order.
func flatten(batches [][]string) []string {
	var out []string
	for _, b := range batches {
		out = append(out, b...)
	}
	return out
}

// prefixOps returns the alias strategy for mode on conn, asserted to smartcache.PrefixAliasOps.
func prefixOps(t *testing.T, conn redisstore.RedisConn, mode smartcache.AliasMode) smartcache.PrefixAliasOps {
	t.Helper()
	ops := redisstore.New(conn).(smartcache.AliasCacheStore).AliasGroup("user", mode)
	po, ok := ops.(smartcache.PrefixAliasOps)
	if !ok {
		t.Fatalf("%T does not implement smartcache.PrefixAliasOps", ops)
	}
	return po
}

func TestGlobLiteral(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain", in: "plain", want: "plain"},
		{name: "star", in: "a*b", want: `a\*b`},
		{name: "question mark", in: "a?b", want: `a\?b`},
		{name: "brackets", in: "a[b]c", want: `a\[b\]c`},
		{name: "backslash", in: `a\b`, want: `a\\b`},
		{name: "braces are not special", in: "{ns}::x", want: "{ns}::x"},
		{name: "empty", in: "", want: ""},
		{name: "mix", in: `bc::{u}*?[x]\`, want: `bc::{u}\*\?\[x\]\\`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := redisstore.GlobLiteralForTest(tc.in); got != tc.want {
				t.Errorf("globLiteral(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestDeleteByPrefix(t *testing.T) {
	cases := []struct {
		name        string
		prefix      string
		wantPattern string
		batches     [][]string
	}{
		{
			name:        "single batch deletes each key",
			prefix:      "bc::user::",
			wantPattern: "bc::user::*",
			batches:     [][]string{{"bc::user::a", "bc::user::b"}},
		},
		{
			name:        "keys across batches are all deleted",
			prefix:      "bc::user::",
			wantPattern: "bc::user::*",
			batches:     [][]string{{"k1"}, {"k2", "k3"}},
		},
		{
			name:        "no matching keys deletes nothing",
			prefix:      "bc::user::",
			wantPattern: "bc::user::*",
		},
		{
			name:        "glob metacharacters in the prefix match literally",
			prefix:      `bc::a*b?c[d]e\f`,
			wantPattern: `bc::a\*b\?c\[d\]e\\f*`,
			batches:     [][]string{{"k1"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := &scanFake{fakeConn: &fakeConn{}, batches: map[string][][]string{tc.wantPattern: tc.batches}}
			st, ok := redisstore.New(fc).(smartcache.PrefixCacheStore)
			if !ok {
				t.Fatal("redisstore must implement smartcache.PrefixCacheStore")
			}
			n, err := st.DeleteByPrefix(context.Background(), tc.prefix)
			if err != nil {
				t.Fatalf("DeleteByPrefix: %v", err)
			}
			want := flatten(tc.batches)
			if !slices.Equal(fc.matches, []string{tc.wantPattern}) {
				t.Errorf("patterns = %q, want %q", fc.matches, tc.wantPattern)
			}
			if !slices.Equal(fc.dels, want) {
				t.Errorf("dels = %v, want %v", fc.dels, want)
			}
			if n != int64(len(want)) {
				t.Errorf("count = %d, want %d", n, len(want))
			}
		})
	}
}

func TestDeleteByPrefix_Errors(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name     string
		batches  [][]string
		scanErr  error
		delErrs  map[string]error
		wantN    int64
		wantDels []string
	}{
		{
			name:     "del error is joined and the sweep continues",
			batches:  [][]string{{"k1", "k2", "k3"}},
			delErrs:  map[string]error{"k2": boom},
			wantN:    2,
			wantDels: []string{"k1", "k2", "k3"},
		},
		{
			name:     "scan error is joined with the count so far",
			batches:  [][]string{{"k1"}},
			scanErr:  boom,
			wantN:    1,
			wantDels: []string{"k1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const pattern = "bc::user::*"
			fc := &scanFake{
				fakeConn: &fakeConn{},
				batches:  map[string][][]string{pattern: tc.batches},
				scanErrs: map[string]error{},
				delErrs:  tc.delErrs,
			}
			if tc.scanErr != nil {
				fc.scanErrs[pattern] = tc.scanErr
			}
			st := redisstore.New(fc).(smartcache.PrefixCacheStore)
			n, err := st.DeleteByPrefix(context.Background(), "bc::user::")
			if !errors.Is(err, boom) {
				t.Errorf("err = %v, want boom joined in", err)
			}
			if n != tc.wantN {
				t.Errorf("count = %d, want %d", n, tc.wantN)
			}
			if !slices.Equal(fc.dels, tc.wantDels) {
				t.Errorf("dels = %v, want %v", fc.dels, tc.wantDels)
			}
		})
	}
}

func TestDeleteByPrefix_ConnWithoutScanKeys(t *testing.T) {
	rc := &delRecorder{fakeConn: &fakeConn{}}
	st, ok := redisstore.New(rc).(smartcache.PrefixCacheStore)
	if !ok {
		t.Fatal("redisstore must implement smartcache.PrefixCacheStore")
	}
	n, err := st.DeleteByPrefix(context.Background(), "bc::user::")
	if !errors.Is(err, smartcache.ErrPrefixEvictNotSupported) {
		t.Errorf("err = %v, want ErrPrefixEvictNotSupported", err)
	}
	if n != 0 {
		t.Errorf("count = %d, want 0", n)
	}
	if len(rc.dels) != 0 {
		t.Errorf("dels = %v, want none", rc.dels)
	}
}

// aliasFixture holds the key shapes of one alias mode for the "user" namespace. The scan patterns are
// written out literally so the tests pin the exact glob that reaches Redis.
type aliasFixture struct {
	name     string
	mode     smartcache.AliasMode
	sharded  bool
	valuePat string
	membPat  string
}

func aliasFixtures() []aliasFixture {
	return []aliasFixture{
		{
			name:     "colocated",
			mode:     smartcache.AliasColocated,
			valuePat: "bc::{user}::app-1::*",
			membPat:  "bc::memb::{user}::app-1::*",
		},
		{
			name:     "sharded",
			mode:     smartcache.AliasSharded,
			sharded:  true,
			valuePat: "bc::{user::app-1::*}",
			membPat:  "bc::memb::{user::app-1::*}",
		},
	}
}

func (f aliasFixture) valueKey(pk string) string { return keyspace.ValueKey("user", pk, f.sharded) }

func (f aliasFixture) membKey(pk string) string { return keyspace.MembersKey("user", pk, f.sharded) }

// evictCase is one EvictByPrimaryPrefix scenario. The value scan returns the keys of app-1::b and
// app-1::a, an app-2 key, and a members key; the members scan returns app-1::a and app-1::c. The
// distinct matching primaries are therefore app-1::a, app-1::b and app-1::c, and the non-matching
// keys must be ignored.
type evictCase struct {
	name         string
	valueScanErr bool
	membScanErr  bool
	failPK       string
	wantErr      bool
	wantN        int64
	wantPKs      []string
}

func evictCases() []evictCase {
	allPKs := []string{"app-1::a", "app-1::b", "app-1::c"}
	return []evictCase{
		{name: "each distinct primary is evicted once in sorted order", wantN: 3, wantPKs: allPKs},
		{name: "value scan error evicts nothing", valueScanErr: true, wantErr: true},
		{name: "members scan error evicts nothing", membScanErr: true, wantErr: true},
		{name: "eval failure is joined and the others still count", failPK: "app-1::b", wantErr: true, wantN: 2, wantPKs: allPKs},
	}
}

// newEvictFake builds the scanFake for one fixture and case: the value and members scans replay the
// keys described on evictCase, and the case decides which scan or eviction fails with boom.
func newEvictFake(f *aliasFixture, tc *evictCase, boom error) *scanFake {
	fc := &scanFake{
		fakeConn: &fakeConn{},
		batches: map[string][][]string{
			f.valuePat: {{f.valueKey("app-1::b"), f.valueKey("app-1::a"), f.valueKey("app-2::x"), f.membKey("app-1::a")}},
			f.membPat:  {{f.membKey("app-1::a"), f.membKey("app-1::c")}},
		},
		scanErrs: map[string]error{},
		evalErrs: map[string]error{},
	}
	if tc.valueScanErr {
		fc.scanErrs[f.valuePat] = boom
	}
	if tc.membScanErr {
		fc.scanErrs[f.membPat] = boom
	}
	if tc.failPK != "" {
		fc.evalErrs[f.valueKey(tc.failPK)] = boom
	}
	return fc
}

// assertEvictScans checks which glob patterns reached the connection: both scans normally, only the
// value scan when it fails.
func assertEvictScans(t *testing.T, f *aliasFixture, tc *evictCase, fc *scanFake) {
	t.Helper()
	wantMatches := []string{f.valuePat, f.membPat}
	if tc.valueScanErr {
		wantMatches = []string{f.valuePat}
	}
	if !slices.Equal(fc.matches, wantMatches) {
		t.Errorf("scan patterns = %q, want %q", fc.matches, wantMatches)
	}
}

// assertEvictOutcome checks the returned count and error of one case.
func assertEvictOutcome(t *testing.T, tc *evictCase, n int64, err, boom error) {
	t.Helper()
	if (err != nil) != tc.wantErr {
		t.Errorf("err = %v, wantErr %v", err, tc.wantErr)
	}
	if tc.wantErr && !errors.Is(err, boom) {
		t.Errorf("err = %v, want boom joined in", err)
	}
	if n != tc.wantN {
		t.Errorf("count = %d, want %d", n, tc.wantN)
	}
}

// assertEvictions checks that each wanted primary was evicted once, in order, with its value and
// members keys.
func assertEvictions(t *testing.T, f *aliasFixture, tc *evictCase, fc *scanFake) {
	t.Helper()
	if len(fc.evalCalls) != len(tc.wantPKs) {
		t.Fatalf("evictions = %d, want %d", len(fc.evalCalls), len(tc.wantPKs))
	}
	for i, pk := range tc.wantPKs {
		e := fc.evalCalls[i]
		if len(e.keys) != 2 || e.keys[0] != f.valueKey(pk) || e.keys[1] != f.membKey(pk) {
			t.Errorf("eviction %d keys = %v, want %q and %q", i, e.keys, f.valueKey(pk), f.membKey(pk))
		}
	}
}

func TestEvictByPrimaryPrefix(t *testing.T) {
	boom := errors.New("boom")
	for _, f := range aliasFixtures() {
		for _, tc := range evictCases() {
			t.Run(f.name+"/"+tc.name, func(t *testing.T) {
				fc := newEvictFake(&f, &tc, boom)

				n, err := prefixOps(t, fc, f.mode).EvictByPrimaryPrefix(context.Background(), "app-1::")

				assertEvictScans(t, &f, &tc, fc)
				assertEvictOutcome(t, &tc, n, err, boom)
				assertEvictions(t, &f, &tc, fc)
			})
		}
	}
}

func TestEvictByPrimaryPrefix_ConnWithoutScanKeys(t *testing.T) {
	for _, f := range aliasFixtures() {
		t.Run(f.name, func(t *testing.T) {
			fc := &fakeConn{}
			n, err := prefixOps(t, fc, f.mode).EvictByPrimaryPrefix(context.Background(), "app-1::")
			if !errors.Is(err, smartcache.ErrPrefixEvictNotSupported) {
				t.Errorf("err = %v, want ErrPrefixEvictNotSupported", err)
			}
			if n != 0 {
				t.Errorf("count = %d, want 0", n)
			}
			if len(fc.evalCalls) != 0 {
				t.Errorf("evictions = %d, want none", len(fc.evalCalls))
			}
		})
	}
}

func TestScanNode(t *testing.T) {
	boom := errors.New("scan boom")
	stop := errors.New("stop")
	cases := []struct {
		name        string
		pages       map[uint64]scanPage
		fnErr       error
		wantBatches [][]string
		wantCursors []uint64
		wantErr     error
	}{
		{
			name:        "multi page follows the cursor until it returns to zero",
			pages:       map[uint64]scanPage{0: {keys: []string{"a", "b"}, next: 7}, 7: {keys: []string{"c"}}},
			wantBatches: [][]string{{"a", "b"}, {"c"}},
			wantCursors: []uint64{0, 7},
		},
		{
			name:        "empty page is not passed to fn",
			pages:       map[uint64]scanPage{0: {next: 7}, 7: {keys: []string{"c"}}},
			wantBatches: [][]string{{"c"}},
			wantCursors: []uint64{0, 7},
		},
		{
			name:        "scan error is returned and stops the loop",
			pages:       map[uint64]scanPage{0: {err: boom}},
			wantCursors: []uint64{0},
			wantErr:     boom,
		},
		{
			name:        "fn error stops the loop and is returned unchanged",
			pages:       map[uint64]scanPage{0: {keys: []string{"a"}, next: 7}, 7: {keys: []string{"b"}}},
			fnErr:       stop,
			wantBatches: [][]string{{"a"}},
			wantCursors: []uint64{0},
			wantErr:     stop,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node := &scanNodeFake{pages: tc.pages}
			var got [][]string
			err := redisstore.ScanNodeForTest(context.Background(), node, "bc::*", func(keys []string) error {
				got = append(got, keys)
				return tc.fnErr
			})
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
			if !equalBatches(got, tc.wantBatches) {
				t.Errorf("batches = %v, want %v", got, tc.wantBatches)
			}
			if !slices.Equal(node.cursors, tc.wantCursors) {
				t.Errorf("cursors = %v, want %v", node.cursors, tc.wantCursors)
			}
			for _, m := range node.matches {
				if m != "bc::*" {
					t.Errorf("match = %q, want %q", m, "bc::*")
				}
			}
		})
	}
}

func TestScanAll_NonClusterForwardsBatches(t *testing.T) {
	boom := errors.New("scan boom")
	stop := errors.New("stop")
	cases := []struct {
		name        string
		pages       map[uint64]scanPage
		fnErr       error
		wantBatches [][]string
		wantErr     error
	}{
		{
			name:        "batches are forwarded",
			pages:       map[uint64]scanPage{0: {keys: []string{"a", "b"}}},
			wantBatches: [][]string{{"a", "b"}},
		},
		{
			name:    "scan error is returned",
			pages:   map[uint64]scanPage{0: {err: boom}},
			wantErr: boom,
		},
		{
			name:        "fn error stops the scan",
			pages:       map[uint64]scanPage{0: {keys: []string{"a"}, next: 7}, 7: {keys: []string{"b"}}},
			fnErr:       stop,
			wantBatches: [][]string{{"a"}},
			wantErr:     stop,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node := &scanNodeFake{pages: tc.pages}
			var got [][]string
			err := redisstore.ScanAll(context.Background(), &scanUniversal{node: node}, "bc::*", func(keys []string) error {
				got = append(got, keys)
				return tc.fnErr
			})
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
			if !equalBatches(got, tc.wantBatches) {
				t.Errorf("batches = %v, want %v", got, tc.wantBatches)
			}
			for _, m := range node.matches {
				if m != "bc::*" {
					t.Errorf("match = %q, want %q", m, "bc::*")
				}
			}
		})
	}
}
