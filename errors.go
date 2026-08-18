package smartcache

import "errors"

var (
	// ErrStoreMiss is returned by CacheStore.Get when the key is absent. It is an
	// internal miss signal that Cache[T] handles; it is never surfaced to callers.
	ErrStoreMiss = errors.New("smartcache: store miss")

	// ErrNotFound is the sentinel a Loader returns (or wraps) to signal a cacheable
	// "does not exist". When negative caching is enabled Cache[T] remembers it
	// briefly and returns it from Get.
	ErrNotFound = errors.New("smartcache: not found")

	// ErrInvalidTTL is returned by New when Options.TTL <= 0 and Options.AllowInfinite
	// is false.
	ErrInvalidTTL = errors.New("smartcache: TTL must be > 0 (set AllowInfinite for no expiry)")

	// ErrPointerType is the value New panics with when T is itself a pointer
	// type. Cache[T] already returns *T from every method; T being a pointer
	// too makes that a double pointer (e.g. **User), which opens a hole where
	// a non-nil outer pointer can wrap a nil inner one — silently violating
	// the "a successful Get/Put never returns nil" guarantee the rest of this
	// package relies on. This is a programming error at the call site
	// (New[*User] instead of New[User]), not a runtime condition, so New
	// panics with it instead of returning it.
	ErrPointerType = errors.New("smartcache: T must not itself be a pointer type")

	// ErrNilWrite is returned by Put when writer succeeds (nil error) but
	// returns a nil value. The cache is never set to nil, so this is treated
	// as a contract violation, not a cacheable state.
	ErrNilWrite = errors.New("smartcache: writer returned a nil value with no error")
)
