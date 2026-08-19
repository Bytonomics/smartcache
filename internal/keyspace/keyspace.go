// Package keyspace builds every smartcache Redis key string, so the key layout has a single
// source of truth shared by the smartcache core and its backends. It deliberately has NO
// dependency on the smartcache package (which would be an import cycle); the slot-placement
// strategy is passed as a plain bool `sharded`.
package keyspace

const (
	globalPrefix = "bc"   // Bytonomics Cache — top-level namespace on every key
	grpSeg       = "grp"  // reverse alias-pointer segment
	membSeg      = "memb" // members-hash segment
)

// NonAliasKey builds a plain (non-alias) cache value key: bc:<ns>:<key>.
func NonAliasKey(ns, key string) string {
	return globalPrefix + ":" + ns + ":" + key
}

// ValueKey builds the value key for one alias-group record.
// Colocated (sharded=false): bc:{ns}:<pk>  — the whole entity shares one slot.
// Sharded   (sharded=true):  bc:{ns:pk}     — one slot per record.
func ValueKey(ns, pk string, sharded bool) string {
	if sharded {
		return globalPrefix + ":{" + ns + ":" + pk + "}"
	}
	return globalPrefix + ":{" + ns + "}:" + pk
}

// MembersKey builds the members-HASH key (field -> aliasValue) for one record.
// Colocated: bc:memb:{ns}:<pk> ; Sharded: bc:memb:{ns:pk}.
func MembersKey(ns, pk string, sharded bool) string {
	if sharded {
		return globalPrefix + ":" + membSeg + ":{" + ns + ":" + pk + "}"
	}
	return globalPrefix + ":" + membSeg + ":{" + ns + "}:" + pk
}

// PointerKey builds a reverse alias pointer key (its value is the primary key).
// Colocated: bc:grp:{ns}:<field>:<value> ; Sharded: bc:grp:{ns:field:value}.
func PointerKey(ns, field, value string, sharded bool) string {
	if sharded {
		return globalPrefix + ":" + grpSeg + ":{" + ns + ":" + field + ":" + value + "}"
	}
	return globalPrefix + ":" + grpSeg + ":{" + ns + "}:" + field + ":" + value
}

// The three Colocated-only prefixes let the single-slot Colocated Lua rebuild sibling keys
// (all under the same {ns} tag): the value key from a pk, the members key from a pk, and a
// pointer key from a field+value. Sharded never needs these (its reverse pointer stores the pk
// directly and its record ops rebuild keys from that pk via ValueKey/MembersKey).

// ColocatedValuePrefix returns bc:{ns}:  (value key = prefix + pk).
func ColocatedValuePrefix(ns string) string {
	return globalPrefix + ":{" + ns + "}:"
}

// ColocatedMembersPrefix returns bc:memb:{ns}:  (members key = prefix + pk).
func ColocatedMembersPrefix(ns string) string {
	return globalPrefix + ":" + membSeg + ":{" + ns + "}:"
}

// ColocatedGrpPrefix returns bc:grp:{ns}:  (pointer key = prefix + field + ":" + value).
func ColocatedGrpPrefix(ns string) string {
	return globalPrefix + ":" + grpSeg + ":{" + ns + "}:"
}
