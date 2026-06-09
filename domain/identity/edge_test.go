package identity

import "testing"

func TestEdgeIdentity_ZeroValue(t *testing.T) {
	var id EdgeIdentity
	if id.Email != "" {
		t.Fatalf("zero EdgeIdentity.Email = %q, want empty", id.Email)
	}
	if id.Groups != nil {
		t.Fatalf("zero EdgeIdentity.Groups = %v, want nil", id.Groups)
	}
}

func TestEdgeIdentity_CarriesEmailAndGroups(t *testing.T) {
	id := EdgeIdentity{Email: "alice@corp.example", Groups: []string{"eng", "platform"}}
	if id.Email != "alice@corp.example" {
		t.Fatalf("Email = %q", id.Email)
	}
	if len(id.Groups) != 2 || id.Groups[0] != "eng" || id.Groups[1] != "platform" {
		t.Fatalf("Groups = %v", id.Groups)
	}
}
