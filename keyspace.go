package smartcache

// Key namespace + segment constants. Every smartcache key string is built by the helpers in
// this file and nowhere else, so the key layout has a single source of truth.
const (
	globalPrefix = "bc"   // Bytonomics Cache — top-level namespace on every key
	grpSeg       = "grp"  // alias-pointer segment
	membSeg      = "memb" // members-set segment
)

// valueKey builds the key that holds a serialized value. Alias-group caches hash-tag the
// namespace ({ns}) so all of an entity's keys share one Redis Cluster slot; non-alias caches
// leave it un-tagged so values distribute across slots. ns is guaranteed non-empty: Register
// (and RegisterAliasGroup, which calls it) rejects an empty resolved Prefix with ErrEmptyPrefix.
func valueKey(ns, key string, aliasGroup bool) string {
	if aliasGroup {
		return globalPrefix + ":{" + ns + "}:" + key
	}
	return globalPrefix + ":" + ns + ":" + key
}

// pointerKey builds an alias pointer key: bc:grp:{ns}:<field>:<value>.
func pointerKey(ns, field, value string) string {
	return globalPrefix + ":" + grpSeg + ":{" + ns + "}:" + field + ":" + value
}

// membersKey builds the members-set key for a primary: bc:memb:{ns}:<primary>.
func membersKey(ns, primary string) string {
	return globalPrefix + ":" + membSeg + ":{" + ns + "}:" + primary
}

// valueKeyPrefix returns bc:{ns}: — the Lua steal-cleanup parses a primary out of a value key
// by stripping this prefix.
func valueKeyPrefix(ns string) string {
	return globalPrefix + ":{" + ns + "}:"
}

// membersKeyPrefix returns bc:memb:{ns}: — used to rebuild a primary's members key inside Lua.
func membersKeyPrefix(ns string) string {
	return globalPrefix + ":" + membSeg + ":{" + ns + "}:"
}

// fieldPrefix returns bc:grp:{ns}:<field>: — used for one-alias-per-field replacement in Lua.
func fieldPrefix(ns, field string) string {
	return globalPrefix + ":" + grpSeg + ":{" + ns + "}:" + field + ":"
}
