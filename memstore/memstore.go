// Package memstore provides an in-memory smartcache.CacheStore for unit tests and
// light use.
package memstore

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Bytonomics/smartcache"
)

// entry holds a stored value and its optional expiry. A zero expiresAt means
// the entry never expires.
type entry struct {
	val       []byte
	expiresAt time.Time
}

// store is an in-memory smartcache.CacheStore.
type store struct {
	mu     sync.RWMutex
	data   map[string]entry
	groups map[string]*memberSet // membersKey -> its alias-pointer keys (alias-group bookkeeping)
}

// memberSet is an in-memory members set (the pointer keys for one primary) with optional expiry.
type memberSet struct {
	members   map[string]struct{}
	expiresAt time.Time
}

var (
	_ smartcache.CacheStore      = (*store)(nil)
	_ smartcache.BatchCacheStore = (*store)(nil)
	_ smartcache.AliasCacheStore = (*store)(nil)
)

// New returns a new in-memory smartcache.CacheStore.
func New() smartcache.CacheStore {
	return &store{data: make(map[string]entry), groups: make(map[string]*memberSet)}
}

func (s *store) Get(ctx context.Context, key string) ([]byte, error) {
	s.mu.RLock()
	e, ok := s.data[key]
	s.mu.RUnlock()
	if !ok {
		return nil, smartcache.ErrStoreMiss
	}
	if !e.expiresAt.IsZero() && time.Now().After(e.expiresAt) {
		s.mu.Lock()
		if cur, stillThere := s.data[key]; stillThere && cur.expiresAt.Equal(e.expiresAt) {
			delete(s.data, key)
		}
		s.mu.Unlock()
		return nil, smartcache.ErrStoreMiss
	}
	cp := make([]byte, len(e.val))
	copy(cp, e.val)
	return cp, nil
}

func (s *store) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	cp := make([]byte, len(val))
	copy(cp, val)

	var expiresAt time.Time
	if ttl > 0 {
		expiresAt = time.Now().Add(ttl)
	}

	s.mu.Lock()
	s.data[key] = entry{val: cp, expiresAt: expiresAt}
	s.mu.Unlock()
	return nil
}

func (s *store) Delete(ctx context.Context, key string) error {
	s.mu.Lock()
	delete(s.data, key)
	s.mu.Unlock()
	return nil
}

func (s *store) Exists(ctx context.Context, key string) (bool, error) {
	s.mu.RLock()
	e, ok := s.data[key]
	s.mu.RUnlock()
	if !ok {
		return false, nil
	}
	if !e.expiresAt.IsZero() && time.Now().After(e.expiresAt) {
		return false, nil
	}
	return true, nil
}

// GetMany reads several keys, reusing Get so per-key expiry and the defensive
// copy apply. Absent/expired keys (ErrStoreMiss) are omitted; any other error
// aborts.
func (s *store) GetMany(ctx context.Context, keys []string) (map[string][]byte, error) {
	out := make(map[string][]byte, len(keys))
	for _, k := range keys {
		b, err := s.Get(ctx, k)
		switch {
		case err == nil:
			out[k] = b
		case errors.Is(err, smartcache.ErrStoreMiss):
			// absent or expired: omit from the result
		default:
			return nil, err
		}
	}
	return out, nil
}
