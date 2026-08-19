package smartcache

import (
	"bytes"
	"context"
	"errors"
	"time"
)

// setValue writes a value for a primary key. Alias-group caches route through the AliasOps handle
// (PutValue refreshes the record's group TTL); normal caches do a plain store Set.
func (c *Cache[T]) setValue(ctx context.Context, key string, b []byte, ttl time.Duration) error {
	if c.isAliasGroup {
		return c.aliasOps.PutValue(ctx, key, b, ttl)
	}
	return c.store.Set(ctx, c.fullKey(key), b, ttl)
}

// deleteValue deletes a value for a primary key. Alias-group caches cascade via EvictByPrimary;
// normal caches do a plain store Delete.
func (c *Cache[T]) deleteValue(ctx context.Context, key string) error {
	if c.isAliasGroup {
		return c.aliasOps.EvictByPrimary(ctx, key)
	}
	return c.store.Delete(ctx, c.fullKey(key))
}

// GetByAlias reads a value by one of its alias keys (read-through: on a miss it runs loader, reads
// the value's CacheUniqueKey, and rebuilds the group). Only valid on an alias-group cache.
func (c *Cache[T]) GetByAlias(ctx context.Context, alias AliasRef, loader Loader[T]) (*T, Outcome, error) {
	if !c.isAliasGroup {
		return nil, Hit, ErrNotAliasGroup
	}
	raw, getErr := c.aliasOps.GetByAlias(ctx, alias)
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
		pkd, ok := any(val).(UniqueKeyed)
		if !ok {
			return &loadResult{val: val}, nil
		}
		primary := pkd.CacheUniqueKey()
		if primary == "" {
			return &loadResult{val: val}, nil
		}
		b, mErr := c.codec.Marshal(val)
		if mErr != nil {
			return &loadResult{val: val}, nil
		}
		if sErr := c.aliasOps.PutByAlias(ctx, primary, alias, b, c.positiveTTL()); sErr != nil {
			return &loadResult{val: val}, nil
		}
		return &loadResult{val: val, populated: true}, nil
	}
	var res any
	var lerr error
	if c.group != nil {
		res, lerr, _ = c.group.Do("alias:"+alias.Field+":"+alias.Value, doLoad) //nolint:not-an-error -- discards singleflight's shared bool
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

// PutAliased write-throughs writer's value under primaryKey and registers alias (one-alias-per-
// field: re-registering a field replaces its old value). Only valid on an alias-group cache.
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
	if sErr := c.aliasOps.PutByAlias(ctx, primaryKey, alias, b, c.positiveTTL()); sErr != nil {
		c.metrics.recordOutcome(ctx, WrittenNotCached)
		return val, WrittenNotCached, nil
	}
	c.metrics.recordOutcome(ctx, Written)
	return val, Written, nil
}

// PutAliasedValue caches a value you already hold under primaryKey and registers alias, with no
// writer. Only valid on an alias-group cache.
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
	return c.aliasOps.PutByAlias(ctx, primaryKey, alias, b, c.positiveTTL())
}

// EvictByAlias deletes the whole group reachable through alias. Only valid on an alias-group cache.
func (c *Cache[T]) EvictByAlias(ctx context.Context, alias AliasRef) error {
	if !c.isAliasGroup {
		return ErrNotAliasGroup
	}
	c.metrics.recordEvict(ctx, 1)
	return c.aliasOps.EvictByAlias(ctx, alias)
}
