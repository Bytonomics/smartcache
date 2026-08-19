package redisstore

import (
	"context"
	_ "embed"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Bytonomics/smartcache"
	"github.com/Bytonomics/smartcache/internal/keyspace"
)

//go:embed scripts/colocated/get.lua
var colocatedGetScript string

//go:embed scripts/colocated/put.lua
var colocatedPutScript string

//go:embed scripts/colocated/evict_by_primary.lua
var colocatedEvictByPrimaryScript string

//go:embed scripts/colocated/evict_by_alias.lua
var colocatedEvictByAliasScript string

// colocatedOps implements smartcache.AliasOps with every key under one {ns} slot, so each op is a
// single atomic Lua. Members are a HASH field->aliasValue; reverse pointers store the primary key.
type colocatedOps struct {
	conn RedisConn
	ns   string
}

var _ smartcache.AliasOps = (*colocatedOps)(nil)

func (o *colocatedOps) GetValue(ctx context.Context, primary string) ([]byte, error) {
	b, err := o.conn.Get(ctx, keyspace.ValueKey(o.ns, primary, false)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, smartcache.ErrStoreMiss
	}
	return b, err
}

func (o *colocatedOps) PutValue(ctx context.Context, primary string, val []byte, ttl time.Duration) error {
	return o.put(ctx, primary, smartcache.AliasRef{}, val, ttl)
}

func (o *colocatedOps) PutByAlias(ctx context.Context, primary string, ref smartcache.AliasRef, val []byte, ttl time.Duration) error {
	return o.put(ctx, primary, ref, val, ttl)
}

func (o *colocatedOps) put(ctx context.Context, primary string, ref smartcache.AliasRef, val []byte, ttl time.Duration) error {
	keys := []string{keyspace.ValueKey(o.ns, primary, false), keyspace.MembersKey(o.ns, primary, false)}
	args := []any{
		val, ttl.Milliseconds(), ref.Field, ref.Value, primary,
		keyspace.ColocatedGrpPrefix(o.ns), keyspace.ColocatedMembersPrefix(o.ns),
	}
	return o.conn.Eval(ctx, colocatedPutScript, keys, args...).Err()
}

func (o *colocatedOps) GetByAlias(ctx context.Context, ref smartcache.AliasRef) ([]byte, error) {
	res, err := o.conn.Eval(ctx, colocatedGetScript,
		[]string{keyspace.PointerKey(o.ns, ref.Field, ref.Value, false)},
		keyspace.ColocatedValuePrefix(o.ns)).Result()
	if errors.Is(err, redis.Nil) {
		return nil, smartcache.ErrStoreMiss
	}
	if err != nil {
		return nil, err
	}
	switch v := res.(type) {
	case string:
		return []byte(v), nil
	case []byte:
		return v, nil
	default:
		return nil, smartcache.ErrStoreMiss
	}
}

func (o *colocatedOps) EvictByPrimary(ctx context.Context, primary string) error {
	keys := []string{keyspace.ValueKey(o.ns, primary, false), keyspace.MembersKey(o.ns, primary, false)}
	return o.conn.Eval(ctx, colocatedEvictByPrimaryScript, keys, keyspace.ColocatedGrpPrefix(o.ns)).Err()
}

func (o *colocatedOps) EvictByAlias(ctx context.Context, ref smartcache.AliasRef) error {
	return o.conn.Eval(ctx, colocatedEvictByAliasScript,
		[]string{keyspace.PointerKey(o.ns, ref.Field, ref.Value, false)},
		keyspace.ColocatedValuePrefix(o.ns), keyspace.ColocatedMembersPrefix(o.ns), keyspace.ColocatedGrpPrefix(o.ns)).Err()
}
