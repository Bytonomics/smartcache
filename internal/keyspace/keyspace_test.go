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
