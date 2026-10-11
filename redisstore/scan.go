package redisstore

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/redis/go-redis/v9"

	"github.com/Bytonomics/smartcache/internal/keyspace"
)

// KeyScanner is the optional RedisConn extension that prefix eviction needs. ScanKeys calls fn
// with batches of every key that matches the Redis glob pattern match, visiting every node of a
// cluster. The keys passed to fn are in the same key space as the other RedisConn methods accept
// (a connection that adds an environment key prefix to every key must apply it to match and remove
// it from the returned keys). fn is never called concurrently. ScanKeys stops and returns the first
// error fn returns.
type KeyScanner interface {
	ScanKeys(ctx context.Context, match string, fn func(keys []string) error) error
}

// scanBatchSize is the COUNT hint sent with every SCAN call. It bounds how many keys one fn call sees.
const scanBatchSize = 100

// nodeScanner is the subset of a go-redis client that scanNode needs. *redis.Client,
// *redis.ClusterClient and redis.UniversalClient all satisfy it.
type nodeScanner interface {
	Scan(ctx context.Context, cursor uint64, match string, count int64) *redis.ScanCmd
}

// scanNode runs the SCAN cursor loop against one node. It starts at cursor 0, passes every
// non-empty page to fn, and stops when the server returns cursor 0. A fn error is returned unchanged.
func scanNode(ctx context.Context, s nodeScanner, match string, fn func(keys []string) error) error {
	var cursor uint64
	for {
		keys, next, err := s.Scan(ctx, cursor, match, scanBatchSize).Result()
		if err != nil {
			return fmt.Errorf("redisstore: scan: %w", err)
		}
		if len(keys) > 0 {
			if fnErr := fn(keys); fnErr != nil {
				return fnErr
			}
		}
		if next == 0 {
			return nil
		}
		cursor = next
	}
}

// ScanAll is the cluster-aware SCAN loop behind KeyScanner for a go-redis client. A
// *redis.ClusterClient is scanned on every master node; any other client is scanned once.
// fn is never called concurrently, and the first error it returns stops the scan on every node.
// Wrappers call ScanAll with their own prefixed pattern and strip their prefix inside fn.
func ScanAll(ctx context.Context, client redis.UniversalClient, match string, fn func(keys []string) error) error {
	var (
		mu       sync.Mutex
		firstErr error
	)
	// ForEachMaster scans nodes concurrently, so batches are serialized here. Once fn has failed,
	// later batches on other nodes return that error without calling fn again.
	serialized := func(keys []string) error {
		mu.Lock()
		defer mu.Unlock()
		if firstErr == nil {
			firstErr = fn(keys)
		}
		return firstErr
	}
	if cc, ok := client.(*redis.ClusterClient); ok {
		return cc.ForEachMaster(ctx, func(nodeCtx context.Context, node *redis.Client) error {
			return scanNode(nodeCtx, node, match, serialized)
		})
	}
	return scanNode(ctx, client, match, serialized)
}

// globLiteral escapes the Redis glob metacharacters \ * ? [ ] so that s matches itself literally
// inside a SCAN MATCH pattern. Every other character, including { and }, is copied unchanged.
func globLiteral(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\\', '*', '?', '[', ']':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// matchPattern returns the SCAN MATCH glob that selects every key inside m. The prefix and suffix
// are matched literally, so only the gap between them is a wildcard.
func matchPattern(m keyspace.Match) string {
	return globLiteral(m.Prefix) + "*" + globLiteral(m.Suffix)
}
