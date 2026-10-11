// Package keyspace builds every smartcache Redis key string, so the key layout has a single
// source of truth shared by the smartcache core and its backends. It deliberately has NO
// dependency on the smartcache package (which would be an import cycle); the slot-placement
// strategy is passed as a plain bool `sharded`.
package keyspace

import "strings"

const (
	globalPrefix = "bc"   // Bytonomics Cache — top-level namespace on every key
	grpSeg       = "grp"  // reverse alias-pointer segment
	membSeg      = "memb" // members-hash segment
)

// NonAliasKey builds a plain (non-alias) cache value key: bc::<ns>::<key>.
func NonAliasKey(ns, key string) string {
	return globalPrefix + "::" + ns + "::" + key
}

// ValueKey builds the value key for one alias-group record.
// Colocated (sharded=false): bc::{ns}::<pk>  — the whole entity shares one slot.
// Sharded   (sharded=true):  bc::{ns::<pk>}     — one slot per record.
func ValueKey(ns, pk string, sharded bool) string {
	if sharded {
		return globalPrefix + "::{" + ns + "::" + pk + "}"
	}
	return globalPrefix + "::{" + ns + "}::" + pk
}

// MembersKey builds the members-HASH key (field -> aliasValue) for one record.
// Colocated: bc::memb::{ns}::<pk> ; Sharded: bc::memb::{ns::<pk>}.
func MembersKey(ns, pk string, sharded bool) string {
	if sharded {
		return globalPrefix + "::" + membSeg + "::{" + ns + "::" + pk + "}"
	}
	return globalPrefix + "::" + membSeg + "::{" + ns + "}::" + pk
}

// PointerKey builds a reverse alias pointer key (its value is the primary key).
// Colocated: bc::grp::{ns}::<field>::<value> ; Sharded: bc::grp::{ns::<field>::<value>}.
func PointerKey(ns, field, value string, sharded bool) string {
	if sharded {
		return globalPrefix + "::" + grpSeg + "::{" + ns + "::" + field + "::" + value + "}"
	}
	return globalPrefix + "::" + grpSeg + "::{" + ns + "}::" + field + "::" + value
}

// The three Colocated-only prefixes let the single-slot Colocated Lua rebuild sibling keys
// (all under the same {ns} tag): the value key from a pk, the members key from a pk, and a
// pointer key from a field+value. Sharded never needs these (its reverse pointer stores the pk
// directly and its record ops rebuild keys from that pk via ValueKey/MembersKey).

// ColocatedValuePrefix returns bc::{ns}::  (value key = prefix + pk).
func ColocatedValuePrefix(ns string) string {
	return globalPrefix + "::{" + ns + "}::"
}

// ColocatedMembersPrefix returns bc::memb::{ns}::  (members key = prefix + pk).
func ColocatedMembersPrefix(ns string) string {
	return globalPrefix + "::" + membSeg + "::{" + ns + "}::"
}

// ColocatedGrpPrefix returns bc::grp::{ns}::  (pointer key = prefix + field + "::" + value).
func ColocatedGrpPrefix(ns string) string {
	return globalPrefix + "::" + grpSeg + "::{" + ns + "}::"
}

// Match describes a family of keys by a literal Prefix and a literal Suffix: a key is in the family
// when it has both. Neither field contains wildcards. Stores turn a Match into a backend pattern
// (redisstore: a glob; memstore: string comparison).
type Match struct {
	Prefix string
	Suffix string
}

// Contains reports whether key belongs to the family.
func (m Match) Contains(key string) bool {
	return len(key) >= len(m.Prefix)+len(m.Suffix) &&
		strings.HasPrefix(key, m.Prefix) && strings.HasSuffix(key, m.Suffix)
}

// NonAliasKeyPrefix is the literal storage prefix of every plain-cache key of ns whose logical key
// starts with keyPrefix: bc::<ns>::<keyPrefix>.
func NonAliasKeyPrefix(ns, keyPrefix string) string {
	return globalPrefix + "::" + ns + "::" + keyPrefix
}

// ValueMatch matches every alias-group value key of ns whose primary key starts with pkPrefix.
// Colocated: {Prefix: "bc::{"+ns+"}::"+pkPrefix, Suffix: ""}.
// Sharded:   {Prefix: "bc::{"+ns+"::"+pkPrefix, Suffix: "}"}.
func ValueMatch(ns, pkPrefix string, sharded bool) Match {
	if sharded {
		return Match{Prefix: globalPrefix + "::{" + ns + "::" + pkPrefix, Suffix: "}"}
	}
	return Match{Prefix: globalPrefix + "::{" + ns + "}::" + pkPrefix}
}

// MembersMatch matches every alias-group members-hash key of ns whose primary key starts with pkPrefix.
// Colocated: {Prefix: "bc::memb::{"+ns+"}::"+pkPrefix, Suffix: ""}.
// Sharded:   {Prefix: "bc::memb::{"+ns+"::"+pkPrefix, Suffix: "}"}.
func MembersMatch(ns, pkPrefix string, sharded bool) Match {
	if sharded {
		return Match{Prefix: globalPrefix + "::" + membSeg + "::{" + ns + "::" + pkPrefix, Suffix: "}"}
	}
	return Match{Prefix: globalPrefix + "::" + membSeg + "::{" + ns + "}::" + pkPrefix}
}

// PrimaryFromValueKey is the inverse of ValueKey: it returns the primary key inside a value key of
// ns and true, or "" and false when key is not a value key of ns for that mode.
func PrimaryFromValueKey(ns, key string, sharded bool) (string, bool) {
	return primaryOf(ValueMatch(ns, "", sharded), key)
}

// PrimaryFromMembersKey is the inverse of MembersKey: it returns the primary key inside a members
// key of ns and true, or "" and false when key is not a members key of ns for that mode.
func PrimaryFromMembersKey(ns, key string, sharded bool) (string, bool) {
	return primaryOf(MembersMatch(ns, "", sharded), key)
}

// primaryOf returns the text that m's Prefix and Suffix wrap around key, and true. It returns "" and
// false when key is outside m's family.
func primaryOf(m Match, key string) (string, bool) {
	if !m.Contains(key) {
		return "", false
	}
	return key[len(m.Prefix) : len(key)-len(m.Suffix)], true
}
