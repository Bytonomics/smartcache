package smartcache

import (
	"context"
	"time"
)

// AliasRef names one secondary lookup key for an alias-group cache: a field ("email") and a
// value ("foo@bar.com").
type AliasRef struct {
	Field string
	Value string
}

// UniqueKeyed is implemented by a value type T cached with a unique key. The library uses
// CacheUniqueKey() to (a) learn a value's primary key on a GetByAlias read-through miss (alias
// groups), and (b) derive the storage/evict key in the value-derived methods Put/PutValue/Evict
// on any cache. The unique key is a SYMBOLIC identity for the cached structure — it need not equal
// the DB primary key and MAY be a composite (e.g. "provider:subscriptionID").
type UniqueKeyed interface {
	CacheUniqueKey() string
}

// AliasMode selects the Redis-Cluster slot-placement strategy for an alias-group cache.
type AliasMode int

const (
	// AliasColocated tags every key of an entity with {ns}: all of the entity's keys share one
	// Cluster slot and every op is a single atomic Lua cascade. One slot per entity (hotspot risk
	// on Cluster with a hot entity). Default.
	AliasColocated AliasMode = iota
	// AliasSharded tags value+members with {ns:pk} (one slot per record) and reverse pointers with
	// {ns:field:value} (one slot per alias), distributing load. GetByAlias is a two-hop resolve with
	// validate-on-read; writes/evicts are a record-slot atomic Lua plus best-effort reverse-pointer
	// ops. No hotspot, no stale reads, weaker cross-key atomicity (self-healing).
	AliasSharded
)

// AliasOps is the per-(namespace, mode) strategy handle for an alias-group cache. It OWNS all
// key-math and operation sequencing; Cache[T] delegates to it with logical identifiers (never a
// pre-built key) and keeps codec, the negative-marker convention, metrics, singleflight, and
// UniqueKeyed. All methods are byte-level: the value bytes may be the internal negative marker,
// which Cache[T] (not the store) interprets. A miss (absent, or a Sharded validate-on-read
// mismatch) is signalled by returning ErrStoreMiss.
type AliasOps interface {
	GetValue(ctx context.Context, primary string) ([]byte, error)
	PutValue(ctx context.Context, primary string, val []byte, ttl time.Duration) error
	EvictByPrimary(ctx context.Context, primary string) error
	GetByAlias(ctx context.Context, ref AliasRef) ([]byte, error)
	PutByAlias(ctx context.Context, primary string, ref AliasRef, val []byte, ttl time.Duration) error
	EvictByAlias(ctx context.Context, ref AliasRef) error
}

// AliasCacheStore is the optional CacheStore extension for backends that support alias groups. It
// is a FACTORY: given an entity namespace and slot mode it returns an AliasOps bound to them.
// RegisterAliasGroup calls AliasGroup once per cache. redisstore returns a Colocated or Sharded
// implementation; memstore returns its single mode-agnostic implementation (one process has no
// slots, so both modes are behaviourally identical there). Detected via a comma-ok type assertion,
// mirroring BatchCacheStore.
type AliasCacheStore interface {
	CacheStore
	AliasGroup(ns string, mode AliasMode) AliasOps
}
