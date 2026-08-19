package smartcache

import (
	"bytes"
	"context"
	"errors"
	"time"
)

// aliasSpec builds an AliasWriteSpec for a grouped write. When alias is nil the spec is a
// primary-only value write (no pointer created), which still refreshes existing group TTLs.
func (c *Cache[T]) aliasSpec(primary string, alias *AliasRef, value []byte, ttl time.Duration) *AliasWriteSpec {
	ns := c.opts.Prefix
	spec := &AliasWriteSpec{
		ValueKey:         valueKey(ns, primary, true),
		MembersKey:       membersKey(ns, primary),
		ValueKeyPrefix:   valueKeyPrefix(ns),
		MembersKeyPrefix: membersKeyPrefix(ns),
		Value:            value,
		TTL:              ttl,
	}
	if alias != nil {
		spec.PointerKey = pointerKey(ns, alias.Field, alias.Value)
		spec.FieldPrefix = fieldPrefix(ns, alias.Field)
	}
	return spec
}

// setValue writes a value for a primary key. On an alias-group cache it goes through the store's
// atomic PutByAlias (primary-only spec), refreshing every existing group key to ttl; on a normal
// cache it is a plain store Set.
func (c *Cache[T]) setValue(ctx context.Context, key string, b []byte, ttl time.Duration) error {
	if c.isAliasGroup {
		return c.aliasStore.PutByAlias(ctx, c.aliasSpec(key, nil, b, ttl))
	}
	return c.store.Set(ctx, c.fullKey(key), b, ttl)
}

// deleteValue deletes a value for a primary key. On an alias-group cache it cascades via
// EvictByPrimary (value + all pointers + members set); on a normal cache it is a plain Delete.
func (c *Cache[T]) deleteValue(ctx context.Context, key string) error {
	if c.isAliasGroup {
		ns := c.opts.Prefix
		return c.aliasStore.EvictByPrimary(ctx, valueKey(ns, key, true), membersKey(ns, key))
	}
	return c.store.Delete(ctx, c.fullKey(key))
}

// GetByAlias reads a value by one of its alias keys. It is read-through: on a miss it runs loader,
// learns the loaded value's primary key via PrimaryKeyed, and rebuilds the group (value + this
// alias pointer + members) under one TTL. Only valid on an alias-group cache.
func (c *Cache[T]) GetByAlias(ctx context.Context, alias AliasRef, loader Loader[T]) (*T, Outcome, error) {
	if !c.isAliasGroup {
		return nil, Hit, ErrNotAliasGroup
	}
	pk := pointerKey(c.opts.Prefix, alias.Field, alias.Value)

	raw, getErr := c.aliasStore.GetByAlias(ctx, pk)
	if getErr == nil {
		if bytes.Equal(raw, negativeMarker) {
			c.metrics.recordOutcome(ctx, NegativeHit)
			return nil, NegativeHit, ErrNotFound
		}
		var v T
		if uErr := c.codec.Unmarshal(raw, &v); uErr == nil {
			c.metrics.recordOutcome(ctx, Hit)
			return &v, Hit, nil
		}
		// corrupt cached entry: fall through and reload
	}

	type loadResult struct {
		val       *T
		populated bool
	}
	doLoad := func() (any, error) {
		start := time.Now()
		val, lerr := loader(ctx)
		c.metrics.recordLoadLatencySeconds(ctx, time.Since(start).Seconds())
		switch {
		case errors.Is(lerr, ErrNotFound):
			return nil, ErrNotFound
		case lerr != nil:
			c.metrics.recordLoadError(ctx)
			return nil, lerr
		case val == nil:
			return nil, ErrNotFound
		}
		pkd, ok := any(val).(PrimaryKeyed)
		if !ok {
			return &loadResult{val: val, populated: false}, nil
		}
		primary := pkd.CachePrimaryKey()
		if primary == "" {
			return &loadResult{val: val, populated: false}, nil
		}
		b, mErr := c.codec.Marshal(val)
		if mErr != nil {
			return &loadResult{val: val, populated: false}, nil
		}
		aliasCopy := alias
		if sErr := c.aliasStore.PutByAlias(ctx, c.aliasSpec(primary, &aliasCopy, b, c.positiveTTL())); sErr != nil {
			return &loadResult{val: val, populated: false}, nil
		}
		return &loadResult{val: val, populated: true}, nil
	}

	var res any
	var lerr error
	if c.group != nil {
		res, lerr, _ = c.group.Do("alias:"+pk, doLoad) //nolint:not-an-error -- discards singleflight's shared bool
	} else {
		res, lerr = doLoad()
	}
	if errors.Is(lerr, ErrNotFound) {
		c.metrics.recordOutcome(ctx, Loaded)
		return nil, Loaded, ErrNotFound
	}
	if lerr != nil {
		return nil, Loaded, lerr
	}
	out, _ := res.(*loadResult) //nolint:not-an-error -- doLoad returns *loadResult on nil error
	if out.populated {
		c.metrics.recordOutcome(ctx, Loaded)
		return out.val, Loaded, nil
	}
	c.metrics.recordOutcome(ctx, LoadedNotCached)
	return out.val, LoadedNotCached, nil
}

// PutAliased write-throughs writer's value under primaryKey and registers alias as one of its
// lookup keys (one-alias-per-field: re-registering a field replaces its old value). Only valid
// on an alias-group cache.
func (c *Cache[T]) PutAliased(ctx context.Context, primaryKey string, alias AliasRef, writer Writer[T]) (*T, Outcome, error) {
	if !c.isAliasGroup {
		return nil, Written, ErrNotAliasGroup
	}
	val, err := writer(ctx)
	if err != nil {
		return nil, Written, err
	}
	if val == nil {
		return nil, Written, ErrNilWrite
	}
	b, mErr := c.codec.Marshal(val)
	if mErr != nil {
		c.metrics.recordOutcome(ctx, WrittenNotCached)
		return val, WrittenNotCached, nil
	}
	if sErr := c.aliasStore.PutByAlias(ctx, c.aliasSpec(primaryKey, &alias, b, c.positiveTTL())); sErr != nil {
		c.metrics.recordOutcome(ctx, WrittenNotCached)
		return val, WrittenNotCached, nil
	}
	c.metrics.recordOutcome(ctx, Written)
	return val, Written, nil
}

// PutAliasedValue caches a value you already hold under primaryKey and registers alias for it,
// without running a writer. Only valid on an alias-group cache.
func (c *Cache[T]) PutAliasedValue(ctx context.Context, primaryKey string, alias AliasRef, val *T) error {
	if !c.isAliasGroup {
		return ErrNotAliasGroup
	}
	if val == nil {
		return ErrNilWrite
	}
	b, err := c.codec.Marshal(val)
	if err != nil {
		return err
	}
	return c.aliasStore.PutByAlias(ctx, c.aliasSpec(primaryKey, &alias, b, c.positiveTTL()))
}

// EvictByAlias deletes the whole group reachable through alias (value + every pointer + members
// set). Only valid on an alias-group cache.
func (c *Cache[T]) EvictByAlias(ctx context.Context, alias AliasRef) error {
	if !c.isAliasGroup {
		return ErrNotAliasGroup
	}
	c.metrics.recordEvict(ctx, 1)
	ns := c.opts.Prefix
	return c.aliasStore.EvictByAlias(ctx, pointerKey(ns, alias.Field, alias.Value), valueKeyPrefix(ns), membersKeyPrefix(ns))
}
