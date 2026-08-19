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

	// ErrInvalidTTL is returned by Register when the resolved TTL <= 0 and
	// AllowInfinite is false.
	ErrInvalidTTL = errors.New("smartcache: TTL must be > 0 (set AllowInfinite for no expiry)")

	// ErrPointerType is the value Register panics with when T is itself a pointer
	// type. Cache[T] already returns *T from every method; T being a pointer
	// too makes that a double pointer (e.g. **User), which opens a hole where
	// a non-nil outer pointer can wrap a nil inner one — silently violating
	// the "a successful Get/Put never returns nil" guarantee the rest of this
	// package relies on. This is a programming error at the call site
	// (Register[*User] instead of Register[User]), not a runtime condition, so
	// Register panics with it instead of returning it.
	ErrPointerType = errors.New("smartcache: T must not itself be a pointer type")

	// ErrNilWrite is returned by Put when writer succeeds (nil error) but
	// returns a nil value. The cache is never set to nil, so this is treated
	// as a contract violation, not a cacheable state.
	ErrNilWrite = errors.New("smartcache: writer returned a nil value with no error")

	// ErrNilStore is returned by NewManager when the CacheStore is nil.
	ErrNilStore = errors.New("smartcache: store must not be nil")

	// ErrEmptyName is returned by Register when the cache name is empty. The name
	// is required: it is both the metric name and the default key prefix.
	ErrEmptyName = errors.New("smartcache: cache name must not be empty")

	// ErrDuplicateName is returned by Register when a cache name is already
	// registered on the manager. Each registered cache must have a unique name.
	ErrDuplicateName = errors.New("smartcache: cache name already registered")

	// ErrEmptyPrefix is returned by Register when EntityOptions.Prefix is explicitly
	// set to an empty string. Prefix namespaces every key this cache stores; an empty
	// prefix would collide with any other cache that also opts out of namespacing.
	ErrEmptyPrefix = errors.New("smartcache: prefix must not be empty")

	// ErrInvalidJitterFraction is returned by Register when the resolved jitter
	// fraction is outside [0, 1). Zero disables jitter.
	ErrInvalidJitterFraction = errors.New("smartcache: jitter fraction must be in [0, 1)")

	// ErrNotAliasGroup is returned when an alias-only method (GetByAlias, PutAliased,
	// PutAliasedValue, EvictByAlias) is called on a cache that was not created with
	// RegisterAliasGroup.
	ErrNotAliasGroup = errors.New("smartcache: cache is not an alias group")

	// ErrAliasingNotSupported is used in the RegisterAliasGroup panic when the manager's
	// store does not implement AliasCacheStore.
	ErrAliasingNotSupported = errors.New("smartcache: store does not implement AliasCacheStore")

	// ErrNotUniqueKeyed is returned by the value-derived methods (Put/PutValue/Evict that take a
	// value instead of a key) when the cached type T does not implement UniqueKeyed. The *ByKey
	// variants (GetByKey/PutByKey/PutValueByKey/EvictByKey/EvictManyByKey/GetManyByKey) work for any T.
	ErrNotUniqueKeyed = errors.New("smartcache: value type does not implement UniqueKeyed (CacheUniqueKey() string)")
)
