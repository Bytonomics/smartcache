package smartcache

import "testing"

func TestKeyspace_ValueKey(t *testing.T) {
	if got := valueKey("user", "5", false); got != "bc:user:5" {
		t.Errorf("non-alias value key: got %q, want %q", got, "bc:user:5")
	}
	if got := valueKey("user", "5", true); got != "bc:{user}:5" {
		t.Errorf("alias value key: got %q, want %q", got, "bc:{user}:5")
	}
}

func TestKeyspace_PointerKey(t *testing.T) {
	if got := pointerKey("user", "email", "foo@bar.com"); got != "bc:grp:{user}:email:foo@bar.com" {
		t.Errorf("pointer key: got %q, want %q", got, "bc:grp:{user}:email:foo@bar.com")
	}
}

func TestKeyspace_MembersKey(t *testing.T) {
	if got := membersKey("user", "5"); got != "bc:memb:{user}:5" {
		t.Errorf("members key: got %q, want %q", got, "bc:memb:{user}:5")
	}
}

func TestKeyspace_Prefixes(t *testing.T) {
	if got := valueKeyPrefix("user"); got != "bc:{user}:" {
		t.Errorf("valueKeyPrefix: got %q, want %q", got, "bc:{user}:")
	}
	if got := membersKeyPrefix("user"); got != "bc:memb:{user}:" {
		t.Errorf("membersKeyPrefix: got %q, want %q", got, "bc:memb:{user}:")
	}
	if got := fieldPrefix("user", "email"); got != "bc:grp:{user}:email:" {
		t.Errorf("fieldPrefix: got %q, want %q", got, "bc:grp:{user}:email:")
	}
}

// TestKeyspace_PrefixConsistency locks the exact relationships the Lua/memstore steal and
// one-per-field logic depend on: stripping valueKeyPrefix from a value key yields the primary;
// membersKeyPrefix+primary rebuilds the members key; and every pointer key starts with its field prefix.
func TestKeyspace_PrefixConsistency(t *testing.T) {
	if valueKeyPrefix("user")+"5" != valueKey("user", "5", true) {
		t.Error("valueKeyPrefix + primary must equal the alias value key")
	}
	if membersKeyPrefix("user")+"5" != membersKey("user", "5") {
		t.Error("membersKeyPrefix + primary must equal the members key")
	}
	pk := pointerKey("user", "email", "foo")
	fp := fieldPrefix("user", "email")
	if len(pk) < len(fp) || pk[:len(fp)] != fp {
		t.Error("pointer key must start with its field prefix")
	}
}
