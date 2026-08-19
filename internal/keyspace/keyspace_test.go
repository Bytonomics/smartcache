package keyspace

import "testing"

func TestNonAliasKey(t *testing.T) {
	if got := NonAliasKey("user", "5"); got != "bc:user:5" {
		t.Errorf("NonAliasKey: got %q, want bc:user:5", got)
	}
}

func TestValueKey(t *testing.T) {
	if got := ValueKey("user", "5", false); got != "bc:{user}:5" {
		t.Errorf("colocated value: got %q, want bc:{user}:5", got)
	}
	if got := ValueKey("user", "5", true); got != "bc:{user:5}" {
		t.Errorf("sharded value: got %q, want bc:{user:5}", got)
	}
}

func TestMembersKey(t *testing.T) {
	if got := MembersKey("user", "5", false); got != "bc:memb:{user}:5" {
		t.Errorf("colocated members: got %q, want bc:memb:{user}:5", got)
	}
	if got := MembersKey("user", "5", true); got != "bc:memb:{user:5}" {
		t.Errorf("sharded members: got %q, want bc:memb:{user:5}", got)
	}
}

func TestPointerKey(t *testing.T) {
	if got := PointerKey("user", "email", "foo@bar.com", false); got != "bc:grp:{user}:email:foo@bar.com" {
		t.Errorf("colocated pointer: got %q", got)
	}
	if got := PointerKey("user", "email", "foo@bar.com", true); got != "bc:grp:{user:email:foo@bar.com}" {
		t.Errorf("sharded pointer: got %q", got)
	}
}

func TestColocatedPrefixes(t *testing.T) {
	if got := ColocatedValuePrefix("user"); got != "bc:{user}:" {
		t.Errorf("value prefix: got %q", got)
	}
	if got := ColocatedMembersPrefix("user"); got != "bc:memb:{user}:" {
		t.Errorf("members prefix: got %q", got)
	}
	if got := ColocatedGrpPrefix("user"); got != "bc:grp:{user}:" {
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
	if ColocatedGrpPrefix("user")+"email:foo" != PointerKey("user", "email", "foo", false) {
		t.Error("grpPrefix + field:value must equal colocated pointer key")
	}
}
