package keyspace

import "testing"

func TestNonAliasKey(t *testing.T) {
	if got := NonAliasKey("user", "5"); got != "bc::user::5" {
		t.Errorf("NonAliasKey: got %q, want bc::user::5", got)
	}
}

func TestValueKey(t *testing.T) {
	if got := ValueKey("user", "5", false); got != "bc::{user}::5" {
		t.Errorf("colocated value: got %q, want bc::{user}::5", got)
	}
	if got := ValueKey("user", "5", true); got != "bc::{user::5}" {
		t.Errorf("sharded value: got %q, want bc::{user::5}", got)
	}
}

func TestMembersKey(t *testing.T) {
	if got := MembersKey("user", "5", false); got != "bc::memb::{user}::5" {
		t.Errorf("colocated members: got %q, want bc::memb::{user}::5", got)
	}
	if got := MembersKey("user", "5", true); got != "bc::memb::{user::5}" {
		t.Errorf("sharded members: got %q, want bc::memb::{user::5}", got)
	}
}

func TestPointerKey(t *testing.T) {
	if got := PointerKey("user", "email", "foo@bar.com", false); got != "bc::grp::{user}::email::foo@bar.com" {
		t.Errorf("colocated pointer: got %q", got)
	}
	if got := PointerKey("user", "email", "foo@bar.com", true); got != "bc::grp::{user::email::foo@bar.com}" {
		t.Errorf("sharded pointer: got %q", got)
	}
}

func TestColocatedPrefixes(t *testing.T) {
	if got := ColocatedValuePrefix("user"); got != "bc::{user}::" {
		t.Errorf("value prefix: got %q", got)
	}
	if got := ColocatedMembersPrefix("user"); got != "bc::memb::{user}::" {
		t.Errorf("members prefix: got %q", got)
	}
	if got := ColocatedGrpPrefix("user"); got != "bc::grp::{user}::" {
		t.Errorf("grp prefix: got %q", got)
	}
}

// TestColocatedPrefixConsistency locks the exact relationships the Colocated Lua depends on:
// stripping/appending a prefix must reproduce the corresponding full key.
func TestColocatedPrefixConsistency(t *testing.T) {
	if ColocatedValuePrefix("user")+"5" != ValueKey("user", "5", false) {
		t.Error("valuePrefix + pk must equal colocated value key")
	}
	if ColocatedMembersPrefix("user")+"5" != MembersKey("user", "5", false) {
		t.Error("membersPrefix + pk must equal colocated members key")
	}
	if ColocatedGrpPrefix("user")+"email::foo" != PointerKey("user", "email", "foo", false) {
		t.Error("grpPrefix + field::value must equal colocated pointer key")
	}
}

// TestKeyShapeNonAlias verifies the exact key shape for non-alias keys.
func TestKeyShapeNonAlias(t *testing.T) {
	ns, key := "app:123", "data:456"
	got := NonAliasKey(ns, key)
	expected := "bc::app:123::data:456"
	if got != expected {
		t.Errorf("NonAliasKey with colons: got %q, want %q", got, expected)
	}
	// Verify bc prefix is preserved
	if !startsWith(got, "bc") {
		t.Errorf("NonAliasKey must start with 'bc', got %q", got)
	}
}

// TestKeyShapeValueColocated verifies the exact key shape for colocated value keys.
func TestKeyShapeValueColocated(t *testing.T) {
	ns, pk := "app:123", "pk:456"
	got := ValueKey(ns, pk, false)
	expected := "bc::{app:123}::pk:456"
	if got != expected {
		t.Errorf("ValueKey colocated: got %q, want %q", got, expected)
	}
	// Verify bc prefix is preserved
	if !startsWith(got, "bc") {
		t.Errorf("ValueKey colocated must start with 'bc', got %q", got)
	}
}

// TestKeyShapeValueSharded verifies the exact key shape for sharded value keys.
func TestKeyShapeValueSharded(t *testing.T) {
	ns, pk := "app:123", "pk:456"
	got := ValueKey(ns, pk, true)
	expected := "bc::{app:123::pk:456}"
	if got != expected {
		t.Errorf("ValueKey sharded: got %q, want %q", got, expected)
	}
	// Verify bc prefix is preserved
	if !startsWith(got, "bc") {
		t.Errorf("ValueKey sharded must start with 'bc', got %q", got)
	}
}

// TestKeyShapeMembersColocated verifies the exact key shape for colocated members keys.
func TestKeyShapeMembersColocated(t *testing.T) {
	ns, pk := "app:123", "pk:456"
	got := MembersKey(ns, pk, false)
	expected := "bc::memb::{app:123}::pk:456"
	if got != expected {
		t.Errorf("MembersKey colocated: got %q, want %q", got, expected)
	}
	// Verify bc prefix is preserved
	if !startsWith(got, "bc") {
		t.Errorf("MembersKey colocated must start with 'bc', got %q", got)
	}
}

// TestKeyShapeMembersSharded verifies the exact key shape for sharded members keys.
func TestKeyShapeMembersSharded(t *testing.T) {
	ns, pk := "app:123", "pk:456"
	got := MembersKey(ns, pk, true)
	expected := "bc::memb::{app:123::pk:456}"
	if got != expected {
		t.Errorf("MembersKey sharded: got %q, want %q", got, expected)
	}
	// Verify bc prefix is preserved
	if !startsWith(got, "bc") {
		t.Errorf("MembersKey sharded must start with 'bc', got %q", got)
	}
}

// TestKeyShapePointerColocated verifies the exact key shape for colocated pointer keys.
func TestKeyShapePointerColocated(t *testing.T) {
	ns, field, value := "app:123", "email:field", "user@example.com:alt"
	got := PointerKey(ns, field, value, false)
	expected := "bc::grp::{app:123}::email:field::user@example.com:alt"
	if got != expected {
		t.Errorf("PointerKey colocated: got %q, want %q", got, expected)
	}
	// Verify bc prefix is preserved
	if !startsWith(got, "bc") {
		t.Errorf("PointerKey colocated must start with 'bc', got %q", got)
	}
}

// TestKeyShapePointerSharded verifies the exact key shape for sharded pointer keys.
func TestKeyShapePointerSharded(t *testing.T) {
	ns, field, value := "app:123", "email:field", "user@example.com:alt"
	got := PointerKey(ns, field, value, true)
	expected := "bc::grp::{app:123::email:field::user@example.com:alt}"
	if got != expected {
		t.Errorf("PointerKey sharded: got %q, want %q", got, expected)
	}
	// Verify bc prefix is preserved
	if !startsWith(got, "bc") {
		t.Errorf("PointerKey sharded must start with 'bc', got %q", got)
	}
}

// startsWith is a simple helper for Go versions that don't have strings.HasPrefix
// available in tests.
func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// TestNonAliasKeyPrefix verifies the literal storage prefix that plain-cache prefix eviction scans.
func TestNonAliasKeyPrefix(t *testing.T) {
	tests := []struct {
		name      string
		ns        string
		keyPrefix string
		want      string
	}{
		{name: "tenant prefix", ns: "user", keyPrefix: "app-1::", want: "bc::user::app-1::"},
		{name: "colon namespace", ns: "app:123", keyPrefix: "data:", want: "bc::app:123::data:"},
		{name: "empty key prefix", ns: "user", keyPrefix: "", want: "bc::user::"},
	}
	for i := range tests {
		tc := &tests[i]
		t.Run(tc.name, func(t *testing.T) {
			if got := NonAliasKeyPrefix(tc.ns, tc.keyPrefix); got != tc.want {
				t.Errorf("NonAliasKeyPrefix(%q, %q) = %q, want %q", tc.ns, tc.keyPrefix, got, tc.want)
			}
		})
	}
}

// TestValueMatch verifies the exact Prefix and Suffix of the alias-group value family in both modes.
func TestValueMatch(t *testing.T) {
	tests := []struct {
		name     string
		ns       string
		pkPrefix string
		sharded  bool
		want     Match
	}{
		{name: "colocated empty pk prefix", ns: "user", want: Match{Prefix: "bc::{user}::"}},
		{name: "colocated colon pk prefix", ns: "app:123", pkPrefix: "pk::", want: Match{Prefix: "bc::{app:123}::pk::"}},
		{name: "sharded empty pk prefix", ns: "user", sharded: true, want: Match{Prefix: "bc::{user::", Suffix: "}"}},
		{name: "sharded colon pk prefix", ns: "app:123", pkPrefix: "pk::", sharded: true, want: Match{Prefix: "bc::{app:123::pk::", Suffix: "}"}},
	}
	for i := range tests {
		tc := &tests[i]
		t.Run(tc.name, func(t *testing.T) {
			if got := ValueMatch(tc.ns, tc.pkPrefix, tc.sharded); got != tc.want {
				t.Errorf("ValueMatch(%q, %q, %t) = %+v, want %+v", tc.ns, tc.pkPrefix, tc.sharded, got, tc.want)
			}
		})
	}
}

// TestMembersMatch verifies the exact Prefix and Suffix of the alias-group members family in both modes.
func TestMembersMatch(t *testing.T) {
	tests := []struct {
		name     string
		ns       string
		pkPrefix string
		sharded  bool
		want     Match
	}{
		{name: "colocated empty pk prefix", ns: "user", want: Match{Prefix: "bc::memb::{user}::"}},
		{name: "colocated colon pk prefix", ns: "app:123", pkPrefix: "pk::", want: Match{Prefix: "bc::memb::{app:123}::pk::"}},
		{name: "sharded empty pk prefix", ns: "user", sharded: true, want: Match{Prefix: "bc::memb::{user::", Suffix: "}"}},
		{name: "sharded colon pk prefix", ns: "app:123", pkPrefix: "pk::", sharded: true, want: Match{Prefix: "bc::memb::{app:123::pk::", Suffix: "}"}},
	}
	for i := range tests {
		tc := &tests[i]
		t.Run(tc.name, func(t *testing.T) {
			if got := MembersMatch(tc.ns, tc.pkPrefix, tc.sharded); got != tc.want {
				t.Errorf("MembersMatch(%q, %q, %t) = %+v, want %+v", tc.ns, tc.pkPrefix, tc.sharded, got, tc.want)
			}
		})
	}
}

// TestMatchContains covers the length guard, the literal prefix and suffix checks, and overlapping
// prefix/suffix (a key must be long enough to hold both without sharing bytes).
func TestMatchContains(t *testing.T) {
	family := Match{Prefix: "ab", Suffix: "cd"}
	overlap := Match{Prefix: "aa", Suffix: "aa"}
	tests := []struct {
		name string
		m    Match
		key  string
		want bool
	}{
		{name: "prefix and suffix around middle", m: family, key: "abXcd", want: true},
		{name: "exactly prefix plus suffix", m: family, key: "abcd", want: true},
		{name: "too short for prefix and suffix", m: family, key: "abc", want: false},
		{name: "empty key", m: family, key: "", want: false},
		{name: "wrong prefix", m: family, key: "xbXcd", want: false},
		{name: "wrong suffix", m: family, key: "abXce", want: false},
		{name: "overlap needs enough length", m: overlap, key: "aaa", want: false},
		{name: "overlap at minimum length", m: overlap, key: "aaaa", want: true},
		{name: "empty prefix and suffix match everything", m: Match{}, key: "anything", want: true},
	}
	for i := range tests {
		tc := &tests[i]
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.m.Contains(tc.key); got != tc.want {
				t.Errorf("Match%+v.Contains(%q) = %t, want %t", tc.m, tc.key, got, tc.want)
			}
		})
	}
}

// roundTripMode is one slot-placement strategy exercised by the round-trip tests.
type roundTripMode struct {
	name    string
	sharded bool
}

// roundTripModes returns both slot-placement strategies for the round-trip tests.
func roundTripModes() []roundTripMode {
	return []roundTripMode{{name: "colocated", sharded: false}, {name: "sharded", sharded: true}}
}

// TestPrimaryFromValueKey_RoundTrip verifies that PrimaryFromValueKey inverts ValueKey in both modes.
func TestPrimaryFromValueKey_RoundTrip(t *testing.T) {
	for _, ns := range []string{"user", "app:123"} {
		for _, pk := range []string{"5", "pk:456", "a::b"} {
			for _, mode := range roundTripModes() {
				t.Run(mode.name+"/"+ns+"/"+pk, func(t *testing.T) {
					got, ok := PrimaryFromValueKey(ns, ValueKey(ns, pk, mode.sharded), mode.sharded)
					if !ok || got != pk {
						t.Errorf("PrimaryFromValueKey(ValueKey(%q, %q)) = (%q, %t), want (%q, true)", ns, pk, got, ok, pk)
					}
				})
			}
		}
	}
}

// TestPrimaryFromMembersKey_RoundTrip verifies that PrimaryFromMembersKey inverts MembersKey in both modes.
func TestPrimaryFromMembersKey_RoundTrip(t *testing.T) {
	for _, ns := range []string{"user", "app:123"} {
		for _, pk := range []string{"5", "pk:456", "a::b"} {
			for _, mode := range roundTripModes() {
				t.Run(mode.name+"/"+ns+"/"+pk, func(t *testing.T) {
					got, ok := PrimaryFromMembersKey(ns, MembersKey(ns, pk, mode.sharded), mode.sharded)
					if !ok || got != pk {
						t.Errorf("PrimaryFromMembersKey(MembersKey(%q, %q)) = (%q, %t), want (%q, true)", ns, pk, got, ok, pk)
					}
				})
			}
		}
	}
}

// TestPrimaryFromValueKey_Rejects verifies that keys outside the value family (pointer keys, members
// keys, plain keys, other namespaces, and the wrong mode) are not reported as value keys.
func TestPrimaryFromValueKey_Rejects(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		sharded bool
	}{
		{name: "pointer key colocated", key: PointerKey("user", "email", "x", false), sharded: false},
		{name: "pointer key sharded", key: PointerKey("user", "email", "x", true), sharded: true},
		{name: "members key colocated", key: MembersKey("user", "5", false), sharded: false},
		{name: "members key sharded", key: MembersKey("user", "5", true), sharded: true},
		{name: "other namespace colocated", key: ValueKey("other", "5", false), sharded: false},
		{name: "other namespace sharded", key: ValueKey("other", "5", true), sharded: true},
		{name: "plain cache key", key: NonAliasKey("user", "5"), sharded: false},
		{name: "colocated key read as sharded", key: ValueKey("user", "5", false), sharded: true},
		{name: "sharded key read as colocated", key: ValueKey("user", "5", true), sharded: false},
		{name: "sharded key missing closing brace", key: "bc::{user::5", sharded: true},
		{name: "empty key", key: "", sharded: false},
	}
	for i := range tests {
		tc := &tests[i]
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := PrimaryFromValueKey("user", tc.key, tc.sharded); ok {
				t.Errorf("PrimaryFromValueKey(%q, sharded=%t) = (%q, true), want false", tc.key, tc.sharded, got)
			}
		})
	}
}

// TestPrimaryFromMembersKey_Rejects verifies that keys outside the members family (pointer keys,
// value keys, plain keys, other namespaces, and the wrong mode) are not reported as members keys.
func TestPrimaryFromMembersKey_Rejects(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		sharded bool
	}{
		{name: "pointer key colocated", key: PointerKey("user", "email", "x", false), sharded: false},
		{name: "pointer key sharded", key: PointerKey("user", "email", "x", true), sharded: true},
		{name: "value key colocated", key: ValueKey("user", "5", false), sharded: false},
		{name: "value key sharded", key: ValueKey("user", "5", true), sharded: true},
		{name: "other namespace colocated", key: MembersKey("other", "5", false), sharded: false},
		{name: "other namespace sharded", key: MembersKey("other", "5", true), sharded: true},
		{name: "plain cache key", key: NonAliasKey("user", "5"), sharded: false},
		{name: "colocated key read as sharded", key: MembersKey("user", "5", false), sharded: true},
		{name: "sharded key read as colocated", key: MembersKey("user", "5", true), sharded: false},
		{name: "sharded key missing closing brace", key: "bc::memb::{user::5", sharded: true},
		{name: "empty key", key: "", sharded: false},
	}
	for i := range tests {
		tc := &tests[i]
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := PrimaryFromMembersKey("user", tc.key, tc.sharded); ok {
				t.Errorf("PrimaryFromMembersKey(%q, sharded=%t) = (%q, true), want false", tc.key, tc.sharded, got)
			}
		})
	}
}

// TestMatchPrefixes_ConsistentWithKeyBuilders locks the exact relationship between the match families
// and the key builders the stores and Lua scripts already rely on.
func TestMatchPrefixes_ConsistentWithKeyBuilders(t *testing.T) {
	tests := []struct {
		name string
		ns   string
		pk   string
	}{
		{name: "plain namespace empty pk", ns: "user", pk: ""},
		{name: "colon namespace and pk", ns: "app:123", pk: "pk::"},
	}
	for i := range tests {
		tc := &tests[i]
		t.Run(tc.name, func(t *testing.T) {
			if got := ValueMatch(tc.ns, "", false).Prefix; got != ColocatedValuePrefix(tc.ns) {
				t.Errorf("ValueMatch(ns, \"\", false).Prefix = %q, want ColocatedValuePrefix %q", got, ColocatedValuePrefix(tc.ns))
			}
			if got := MembersMatch(tc.ns, "", false).Prefix; got != ColocatedMembersPrefix(tc.ns) {
				t.Errorf("MembersMatch(ns, \"\", false).Prefix = %q, want ColocatedMembersPrefix %q", got, ColocatedMembersPrefix(tc.ns))
			}
			if got := ValueMatch(tc.ns, tc.pk, false).Prefix; got != ValueKey(tc.ns, tc.pk, false) {
				t.Errorf("colocated ValueMatch prefix = %q, want ValueKey %q", got, ValueKey(tc.ns, tc.pk, false))
			}
			if m := ValueMatch(tc.ns, tc.pk, true); m.Prefix+m.Suffix != ValueKey(tc.ns, tc.pk, true) {
				t.Errorf("sharded ValueMatch prefix+suffix = %q, want ValueKey %q", m.Prefix+m.Suffix, ValueKey(tc.ns, tc.pk, true))
			}
			if got := MembersMatch(tc.ns, tc.pk, false).Prefix; got != MembersKey(tc.ns, tc.pk, false) {
				t.Errorf("colocated MembersMatch prefix = %q, want MembersKey %q", got, MembersKey(tc.ns, tc.pk, false))
			}
			if m := MembersMatch(tc.ns, tc.pk, true); m.Prefix+m.Suffix != MembersKey(tc.ns, tc.pk, true) {
				t.Errorf("sharded MembersMatch prefix+suffix = %q, want MembersKey %q", m.Prefix+m.Suffix, MembersKey(tc.ns, tc.pk, true))
			}
			prefix := Match{Prefix: NonAliasKeyPrefix(tc.ns, "k::")}
			if !prefix.Contains(NonAliasKey(tc.ns, "k::u1")) {
				t.Error("NonAliasKeyPrefix(ns, \"k::\") must prefix NonAliasKey(ns, \"k::u1\")")
			}
		})
	}
}
