package memstore

import (
	"context"
	"time"

	"github.com/Bytonomics/smartcache"
	"github.com/Bytonomics/smartcache/internal/keyspace"
)

// memAliasOps is memstore's single, mode-agnostic AliasOps. Members are a field->aliasValue HASH;
// reads validate the alias against that HASH (parity with the redisstore Sharded read-repair).
type memAliasOps struct {
	s       *store
	ns      string
	sharded bool
}

var _ smartcache.AliasOps = (*memAliasOps)(nil)

func (o *memAliasOps) valueKey(pk string) string   { return keyspace.ValueKey(o.ns, pk, o.sharded) }
func (o *memAliasOps) membersKey(pk string) string { return keyspace.MembersKey(o.ns, pk, o.sharded) }
func (o *memAliasOps) pointerKey(field, value string) string {
	return keyspace.PointerKey(o.ns, field, value, o.sharded)
}

func (o *memAliasOps) GetValue(ctx context.Context, primary string) ([]byte, error) {
	o.s.mu.RLock()
	defer o.s.mu.RUnlock()
	e, ok := o.s.data[o.valueKey(primary)]
	if !ok || aliasExpired(e.expiresAt) {
		return nil, smartcache.ErrStoreMiss
	}
	return cloneBytes(e.val), nil
}

func (o *memAliasOps) PutValue(ctx context.Context, primary string, val []byte, ttl time.Duration) error {
	o.s.mu.Lock()
	defer o.s.mu.Unlock()
	o.writeValueLocked(primary, val, ttl)
	o.refreshGroupLocked(primary, ttl)
	return nil
}

func (o *memAliasOps) PutByAlias(ctx context.Context, primary string, ref smartcache.AliasRef, val []byte, ttl time.Duration) error {
	o.s.mu.Lock()
	defer o.s.mu.Unlock()
	o.writeValueLocked(primary, val, ttl)

	mkey := o.membersKey(primary)
	mh := o.s.members[mkey]
	if mh != nil {
		if oldVal, ok := mh.fields[ref.Field]; ok && oldVal != ref.Value {
			delete(o.s.data, o.pointerKey(ref.Field, oldVal)) // one-per-field: drop replaced value's pointer
		}
	}
	pkey := o.pointerKey(ref.Field, ref.Value)
	if pe, ok := o.s.data[pkey]; ok { // steal: pointer already targets a different primary
		if oldPk := string(pe.val); oldPk != primary {
			if oldMh := o.s.members[o.membersKey(oldPk)]; oldMh != nil {
				delete(oldMh.fields, ref.Field)
			}
		}
	}
	o.s.data[pkey] = entry{val: []byte(primary), expiresAt: expAt(ttl)}
	if mh == nil {
		mh = &membersHash{fields: make(map[string]string)}
		o.s.members[mkey] = mh
	}
	mh.fields[ref.Field] = ref.Value
	o.refreshGroupLocked(primary, ttl)
	return nil
}

func (o *memAliasOps) GetByAlias(ctx context.Context, ref smartcache.AliasRef) ([]byte, error) {
	o.s.mu.RLock()
	defer o.s.mu.RUnlock()
	pe, ok := o.s.data[o.pointerKey(ref.Field, ref.Value)]
	if !ok || aliasExpired(pe.expiresAt) {
		return nil, smartcache.ErrStoreMiss
	}
	pk := string(pe.val)
	mh := o.s.members[o.membersKey(pk)]
	if mh == nil || aliasExpired(mh.expiresAt) || mh.fields[ref.Field] != ref.Value { // validate-on-read
		return nil, smartcache.ErrStoreMiss
	}
	e, ok := o.s.data[o.valueKey(pk)]
	if !ok || aliasExpired(e.expiresAt) {
		return nil, smartcache.ErrStoreMiss
	}
	return cloneBytes(e.val), nil
}

func (o *memAliasOps) EvictByPrimary(ctx context.Context, primary string) error {
	o.s.mu.Lock()
	defer o.s.mu.Unlock()
	o.evictLocked(primary)
	return nil
}

func (o *memAliasOps) EvictByAlias(ctx context.Context, ref smartcache.AliasRef) error {
	o.s.mu.Lock()
	defer o.s.mu.Unlock()
	pe, ok := o.s.data[o.pointerKey(ref.Field, ref.Value)]
	if !ok {
		return nil
	}
	o.evictLocked(string(pe.val))
	return nil
}

func (o *memAliasOps) writeValueLocked(primary string, val []byte, ttl time.Duration) {
	o.s.data[o.valueKey(primary)] = entry{val: cloneBytes(val), expiresAt: expAt(ttl)}
}

func (o *memAliasOps) refreshGroupLocked(primary string, ttl time.Duration) {
	mh := o.s.members[o.membersKey(primary)]
	if mh == nil {
		return
	}
	exp := expAt(ttl)
	mh.expiresAt = exp
	for field, value := range mh.fields {
		pkey := o.pointerKey(field, value)
		if e, ok := o.s.data[pkey]; ok {
			e.expiresAt = exp
			o.s.data[pkey] = e
		}
	}
}

func (o *memAliasOps) evictLocked(primary string) {
	delete(o.s.data, o.valueKey(primary))
	mkey := o.membersKey(primary)
	if mh := o.s.members[mkey]; mh != nil {
		for field, value := range mh.fields {
			delete(o.s.data, o.pointerKey(field, value))
		}
	}
	delete(o.s.members, mkey)
}

func expAt(ttl time.Duration) time.Time {
	if ttl > 0 {
		return time.Now().Add(ttl)
	}
	return time.Time{}
}

func cloneBytes(b []byte) []byte {
	cp := make([]byte, len(b))
	copy(cp, b)
	return cp
}

// aliasExpired reports whether a non-zero expiry is in the past (mirrors memstore.Get's inline check).
func aliasExpired(t time.Time) bool {
	return !t.IsZero() && time.Now().After(t)
}
