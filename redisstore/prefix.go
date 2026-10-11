package redisstore

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"

	"github.com/Bytonomics/smartcache"
	"github.com/Bytonomics/smartcache/internal/keyspace"
)

// evictByPrimaryPrefix evicts every record of ns whose primary key starts with pkPrefix. It finds the
// records by scanning both the value keys and the members keys, so a record whose value has already
// expired but whose members hash remains is still cleaned up. Each record is then evicted through
// evict, which is the strategy's own EvictByPrimary (the single-record cascade). Primaries are
// evicted in sorted order. It returns the number of records evicted without error, and the joined
// eviction errors. A scan error aborts before any record is evicted.
func evictByPrimaryPrefix(
	ctx context.Context,
	conn RedisConn,
	ns string,
	sharded bool,
	pkPrefix string,
	evict func(ctx context.Context, primary string) error,
) (int64, error) {
	sc, ok := conn.(KeyScanner)
	if !ok {
		return 0, smartcache.ErrPrefixEvictNotSupported
	}
	pks := make(map[string]struct{})
	collect := func(m keyspace.Match, primaryOf func(string, string, bool) (string, bool)) error {
		return sc.ScanKeys(ctx, matchPattern(m), func(keys []string) error {
			for _, key := range keys {
				pk, found := primaryOf(ns, key, sharded)
				if found && strings.HasPrefix(pk, pkPrefix) {
					pks[pk] = struct{}{}
				}
			}
			return nil
		})
	}
	if err := collect(keyspace.ValueMatch(ns, pkPrefix, sharded), keyspace.PrimaryFromValueKey); err != nil {
		return 0, err
	}
	if err := collect(keyspace.MembersMatch(ns, pkPrefix, sharded), keyspace.PrimaryFromMembersKey); err != nil {
		return 0, err
	}
	var (
		count int64
		errs  []error
	)
	for _, pk := range slices.Sorted(maps.Keys(pks)) {
		if err := evict(ctx, pk); err != nil {
			errs = append(errs, err)
			continue
		}
		count++
	}
	return count, errors.Join(errs...)
}
