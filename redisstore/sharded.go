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

//go:embed scripts/sharded/record_get_validate.lua
var shardedGetValidateScript string

//go:embed scripts/sharded/record_put.lua
var shardedPutScript string

//go:embed scripts/sharded/record_evict.lua
var shardedEvictScript string

//go:embed scripts/sharded/pointer_cdel.lua
var shardedPointerCDelScript string

// shardedOps implements smartcache.AliasOps with value+members per record ({ns:pk}) and reverse
// pointers per alias ({ns:field:value}). The record (value+members HASH) mutates atomically in one
// Lua; reverse pointers are best-effort. GetByAlias validates the alias against the record's members
// HASH (read-repair), so a stale pointer resolves to a miss. Cleanup is compare-and-delete.
type shardedOps struct {
	conn RedisConn
	ns   string
}

var _ smartcache.AliasOps = (*shardedOps)(nil)

func (o *shardedOps) GetValue(ctx context.Context, primary string) ([]byte, error) {
	b, err := o.conn.Get(ctx, keyspace.ValueKey(o.ns, primary, true)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, smartcache.ErrStoreMiss
	}
	return b, err
}

func (o *shardedOps) PutValue(ctx context.Context, primary string, val []byte, ttl time.Duration) error {
	keys := []string{keyspace.ValueKey(o.ns, primary, true), keyspace.MembersKey(o.ns, primary, true)}
	return o.conn.Eval(ctx, shardedPutScript, keys, val, ttl.Milliseconds(), "", "").Err()
}

func (o *shardedOps) PutByAlias(ctx context.Context, primary string, ref smartcache.AliasRef, val []byte, ttl time.Duration) error {
	keys := []string{keyspace.ValueKey(o.ns, primary, true), keyspace.MembersKey(o.ns, primary, true)}
	if err := o.conn.Eval(ctx, shardedPutScript, keys, val, ttl.Milliseconds(), ref.Field, ref.Value).Err(); err != nil {
		return err
	}
	return o.conn.Set(ctx, keyspace.PointerKey(o.ns, ref.Field, ref.Value, true), primary, ttl).Err()
}

func (o *shardedOps) GetByAlias(ctx context.Context, ref smartcache.AliasRef) ([]byte, error) {
	pk, err := o.conn.Get(ctx, keyspace.PointerKey(o.ns, ref.Field, ref.Value, true)).Result()
	if errors.Is(err, redis.Nil) {
		return nil, smartcache.ErrStoreMiss
	}
	if err != nil {
		return nil, err
	}
	keys := []string{keyspace.ValueKey(o.ns, pk, true), keyspace.MembersKey(o.ns, pk, true)}
	res, err := o.conn.Eval(ctx, shardedGetValidateScript, keys, ref.Field, ref.Value).Result()
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

func (o *shardedOps) EvictByPrimary(ctx context.Context, primary string) error {
	keys := []string{keyspace.ValueKey(o.ns, primary, true), keyspace.MembersKey(o.ns, primary, true)}
	res, err := o.conn.Eval(ctx, shardedEvictScript, keys).Result()
	if err != nil {
		return err
	}
	arr, ok := res.([]any)
	if !ok {
		return nil
	}
	for i := 0; i+1 < len(arr); i += 2 {
		field, _ := arr[i].(string)
		value, _ := arr[i+1].(string)
		_ = o.conn.Eval(ctx, shardedPointerCDelScript, []string{keyspace.PointerKey(o.ns, field, value, true)}, primary).Err()
	}
	return nil
}

func (o *shardedOps) EvictByAlias(ctx context.Context, ref smartcache.AliasRef) error {
	pk, err := o.conn.Get(ctx, keyspace.PointerKey(o.ns, ref.Field, ref.Value, true)).Result()
	if errors.Is(err, redis.Nil) {
		return nil
	}
	if err != nil {
		return err
	}
	return o.EvictByPrimary(ctx, pk)
}
