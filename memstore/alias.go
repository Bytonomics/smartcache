package memstore

import (
	"context"
	"strings"
	"time"

	"github.com/Bytonomics/smartcache"
)

// GetByAlias resolves a pointer key to its value key and returns a copy of the value bytes, or
// ErrStoreMiss when the pointer or the value is absent/expired.
func (s *store) GetByAlias(ctx context.Context, pointerKey string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pe, ok := s.data[pointerKey]
	if !ok || aliasExpired(pe.expiresAt) {
		return nil, smartcache.ErrStoreMiss
	}
	ve, ok := s.data[string(pe.val)]
	if !ok || aliasExpired(ve.expiresAt) {
		return nil, smartcache.ErrStoreMiss
	}
	cp := make([]byte, len(ve.val))
	copy(cp, ve.val)
	return cp, nil
}

// PutByAlias writes the value, upserts the pointer (one-alias-per-field replace + cross-primary
// steal cleanup), adds it to the members set, and refreshes every group key's expiry to spec.TTL.
// Atomic within the process.
func (s *store) PutByAlias(ctx context.Context, spec *smartcache.AliasWriteSpec) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var exp time.Time
	if spec.TTL > 0 {
		exp = time.Now().Add(spec.TTL)
	}

	valCopy := make([]byte, len(spec.Value))
	copy(valCopy, spec.Value)
	s.data[spec.ValueKey] = entry{val: valCopy, expiresAt: exp}

	ms := s.groups[spec.MembersKey]

	if spec.PointerKey != "" {
		if ms != nil {
			for m := range ms.members {
				if m != spec.PointerKey && strings.HasPrefix(m, spec.FieldPrefix) {
					delete(s.data, m)
					delete(ms.members, m)
				}
			}
		}
		if pe, ok := s.data[spec.PointerKey]; ok {
			oldVk := string(pe.val)
			if oldVk != spec.ValueKey {
				oldMkey := spec.MembersKeyPrefix + strings.TrimPrefix(oldVk, spec.ValueKeyPrefix)
				if oldMs := s.groups[oldMkey]; oldMs != nil {
					delete(oldMs.members, spec.PointerKey)
				}
			}
		}
		s.data[spec.PointerKey] = entry{val: []byte(spec.ValueKey), expiresAt: exp}
		if ms == nil {
			ms = &memberSet{members: make(map[string]struct{})}
			s.groups[spec.MembersKey] = ms
		}
		ms.members[spec.PointerKey] = struct{}{}
	}

	if ms != nil {
		ms.expiresAt = exp
		for m := range ms.members {
			if e, ok := s.data[m]; ok {
				e.expiresAt = exp
				s.data[m] = e
			}
		}
	}
	return nil
}

// EvictByPrimary deletes the value key, every pointer in the members set, and the set.
func (s *store) EvictByPrimary(ctx context.Context, valueKey, membersKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, valueKey)
	if ms := s.groups[membersKey]; ms != nil {
		for m := range ms.members {
			delete(s.data, m)
		}
	}
	delete(s.groups, membersKey)
	return nil
}

// EvictByAlias resolves the pointer to its primary, then cascades like EvictByPrimary.
func (s *store) EvictByAlias(ctx context.Context, pointerKey, valueKeyPrefix, membersKeyPrefix string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	pe, ok := s.data[pointerKey]
	if !ok {
		return nil
	}
	valueKey := string(pe.val)
	membersKey := membersKeyPrefix + strings.TrimPrefix(valueKey, valueKeyPrefix)
	delete(s.data, valueKey)
	if ms := s.groups[membersKey]; ms != nil {
		for m := range ms.members {
			delete(s.data, m)
		}
	}
	delete(s.groups, membersKey)
	return nil
}

// aliasExpired reports whether a non-zero expiry is in the past (mirrors memstore.Get's inline check).
func aliasExpired(t time.Time) bool {
	return !t.IsZero() && time.Now().After(t)
}
