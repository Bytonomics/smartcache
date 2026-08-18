// Package memstore provides an in-memory smartcache.Store for unit tests and
// light use.
package memstore

import (
	"context"
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

// store is an in-memory smartcache.Store.
type store struct {
	mu   sync.RWMutex
	data map[string]entry
}

var _ smartcache.Store = (*store)(nil)

// New returns a new in-memory smartcache.Store.
func New() smartcache.Store {
	return &store{data: make(map[string]entry)}
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
