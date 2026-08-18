package smartcache

import "errors"

var (
	// ErrStoreMiss is returned by Store.Get when the key is absent. It is an
	// internal miss signal that Cache[T] handles; it is never surfaced to callers.
	ErrStoreMiss = errors.New("smartcache: store miss")

	// ErrNotFound is the sentinel a Loader returns (or wraps) to signal a cacheable
	// "does not exist". When negative caching is enabled Cache[T] remembers it
	// briefly and returns it from Get.
	ErrNotFound = errors.New("smartcache: not found")

	// ErrInvalidTTL is returned by New when Options.TTL <= 0 and Options.AllowInfinite
	// is false.
	ErrInvalidTTL = errors.New("smartcache: TTL must be > 0 (set AllowInfinite for no expiry)")
)
