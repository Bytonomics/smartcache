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

// PrimaryKeyed is implemented by the value type T (or *T) cached in an alias-group cache. It
// lets the library learn a value's primary key when rebuilding the group on a GetByAlias
// read-through miss. It returns the primary key VALUE (e.g. "5"); the value key is bc:{ns}:<value>.
type PrimaryKeyed interface {
	CachePrimaryKey() string
}

// AliasWriteSpec carries the pre-built key strings (produced by keyspace.go on the Cache[T]
// side) for a single grouped write. The alias-related fields are empty for a primary-only
// value write, which still refreshes the TTL of every existing group key.
type AliasWriteSpec struct {
	ValueKey         string // bc:{ns}:<primary>
	MembersKey       string // bc:memb:{ns}:<primary>
	PointerKey       string // bc:grp:{ns}:<field>:<value>   ("" => primary-only write)
	FieldPrefix      string // bc:grp:{ns}:<field>:          ("" => primary-only write)
	ValueKeyPrefix   string // bc:{ns}:      (steal-cleanup: parse old primary from old value key)
	MembersKeyPrefix string // bc:memb:{ns}: (steal-cleanup: rebuild old primary's members key)
	Value            []byte
	TTL              time.Duration // the single jittered TTL, computed once by Cache[T]
}

// AliasCacheStore is an optional CacheStore extension for backends that can maintain atomic key
// groups (a value key, its alias pointers, and a members set) in one operation. It is detected
// once at RegisterAliasGroup via a comma-ok type assertion, mirroring BatchCacheStore. The store
// is a dumb executor: Cache[T] builds every key string via keyspace.go and passes them in, so the
// keyspace stays single-source and the store never constructs keys.
type AliasCacheStore interface {
	CacheStore

	// GetByAlias resolves pointerKey -> value key -> value bytes. It returns ErrStoreMiss when
	// the pointer or the value it points to is absent.
	GetByAlias(ctx context.Context, pointerKey string) ([]byte, error)

	// PutByAlias writes spec.Value at spec.ValueKey and, when spec.PointerKey is non-empty,
	// upserts the alias pointer (one-alias-per-field replacement plus cross-primary steal
	// cleanup) and adds it to the members set — then refreshes every current group key to
	// spec.TTL. It is one atomic operation on the {ns} slot.
	PutByAlias(ctx context.Context, spec *AliasWriteSpec) error

	// EvictByPrimary deletes the value key, every pointer listed in the members set, and the
	// members set itself.
	EvictByPrimary(ctx context.Context, valueKey, membersKey string) error

	// EvictByAlias resolves pointerKey to its primary, then cascades exactly like EvictByPrimary.
	// valueKeyPrefix and membersKeyPrefix let the store derive the primary and members key.
	EvictByAlias(ctx context.Context, pointerKey, valueKeyPrefix, membersKeyPrefix string) error
}
