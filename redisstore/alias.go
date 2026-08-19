package redisstore

import (
	"context"
	_ "embed"
	"errors"

	"github.com/redis/go-redis/v9"

	"github.com/Bytonomics/smartcache"
)

//go:embed scripts/alias_get.lua
var aliasGetScript string

//go:embed scripts/group_put.lua
var groupPutScript string

//go:embed scripts/group_evict_by_primary.lua
var groupEvictByPrimaryScript string

//go:embed scripts/group_evict_by_alias.lua
var groupEvictByAliasScript string

var _ smartcache.AliasCacheStore = (*store)(nil)

// GetByAlias resolves a pointer key to its value key and returns the value bytes, or
// ErrStoreMiss when the pointer or the value it points to is absent.
func (s *store) GetByAlias(ctx context.Context, pointerKey string) ([]byte, error) {
	res, err := s.conn.Eval(ctx, aliasGetScript, []string{pointerKey}).Result()
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

// PutByAlias writes the value and (when a pointer is given) upserts the alias pointer with
// one-alias-per-field replacement + cross-primary steal cleanup, then refreshes every current
// group key to spec.TTL. One atomic Lua on the {ns} slot. TTL is passed as milliseconds.
func (s *store) PutByAlias(ctx context.Context, spec *smartcache.AliasWriteSpec) error {
	keys := []string{spec.ValueKey, spec.MembersKey}
	args := []any{spec.Value, spec.TTL.Milliseconds(), spec.PointerKey, spec.FieldPrefix, spec.MembersKeyPrefix, spec.ValueKeyPrefix}
	return s.conn.Eval(ctx, groupPutScript, keys, args...).Err()
}

// EvictByPrimary deletes the value key, every pointer in the members set, and the set.
func (s *store) EvictByPrimary(ctx context.Context, valueKey, membersKey string) error {
	return s.conn.Eval(ctx, groupEvictByPrimaryScript, []string{valueKey, membersKey}).Err()
}

// EvictByAlias resolves the pointer to its primary, then cascades like EvictByPrimary.
func (s *store) EvictByAlias(ctx context.Context, pointerKey, valueKeyPrefix, membersKeyPrefix string) error {
	return s.conn.Eval(ctx, groupEvictByAliasScript, []string{pointerKey}, valueKeyPrefix, membersKeyPrefix).Err()
}
