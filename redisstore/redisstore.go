// Package redisstore provides a go-redis-backed smartcache.CacheStore.
package redisstore

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Bytonomics/smartcache"
)

// RedisConn is the minimal slice of the go-redis client API that redisstore uses.
// *redis.Client satisfies it, so callers pass their existing client without the
// store depending on the full client surface. It is also trivially fakeable in tests.
type RedisConn interface {
	Get(ctx context.Context, key string) *redis.StringCmd
	Set(ctx context.Context, key string, value any, ttl time.Duration) *redis.StatusCmd
	Del(ctx context.Context, keys ...string) *redis.IntCmd
	Exists(ctx context.Context, keys ...string) *redis.IntCmd
	MGet(ctx context.Context, keys ...string) *redis.SliceCmd
}

// store is a go-redis-backed smartcache.CacheStore.
type store struct {
	conn RedisConn
}

var (
	_ smartcache.CacheStore      = (*store)(nil)
	_ smartcache.BatchCacheStore = (*store)(nil)
)

// New returns a smartcache.CacheStore backed by conn.
func New(conn RedisConn) smartcache.CacheStore {
	return &store{conn: conn}
}

func (s *store) Get(ctx context.Context, key string) ([]byte, error) {
	b, err := s.conn.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, smartcache.ErrStoreMiss
	}
	return b, err
}

func (s *store) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	return s.conn.Set(ctx, key, val, ttl).Err()
}

func (s *store) Delete(ctx context.Context, key string) error {
	return s.conn.Del(ctx, key).Err()
}

func (s *store) Exists(ctx context.Context, key string) (bool, error) {
	n, err := s.conn.Exists(ctx, key).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// GetMany reads several keys with a single Redis MGET. Present values are
// returned as bytes keyed by the requested key; absent keys (nil in the MGET
// reply) are omitted. A transport error aborts.
func (s *store) GetMany(ctx context.Context, keys []string) (map[string][]byte, error) {
	if len(keys) == 0 {
		return map[string][]byte{}, nil
	}
	vals, err := s.conn.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	out := make(map[string][]byte, len(keys))
	for i, v := range vals {
		if i >= len(keys) {
			break
		}
		switch t := v.(type) {
		case string:
			out[keys[i]] = []byte(t)
		case []byte:
			out[keys[i]] = t
		default:
			// nil (absent) or an unexpected type: omit
		}
	}
	return out, nil
}
