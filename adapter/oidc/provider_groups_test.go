package oidc

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestExtractGroups_ArrayClaim(t *testing.T) {
	raw := json.RawMessage(`["engineering","platform"]`)
	got := extractGroups(map[string]json.RawMessage{"groups": raw}, "groups")
	if !reflect.DeepEqual(got, []string{"engineering", "platform"}) {
		t.Errorf("got %v, want [engineering platform]", got)
	}
}

func TestExtractGroups_SingleStringClaim(t *testing.T) {
	raw := json.RawMessage(`"engineering"`)
	got := extractGroups(map[string]json.RawMessage{"groups": raw}, "groups")
	if !reflect.DeepEqual(got, []string{"engineering"}) {
		t.Errorf("got %v, want [engineering]", got)
	}
}

func TestExtractGroups_EmptyClaimNameReturnsNil(t *testing.T) {
	raw := json.RawMessage(`["x"]`)
	if got := extractGroups(map[string]json.RawMessage{"groups": raw}, ""); got != nil {
		t.Errorf("got %v, want nil when groupclaim is empty", got)
	}
}

func TestExtractGroups_MissingClaimReturnsNil(t *testing.T) {
	if got := extractGroups(map[string]json.RawMessage{}, "groups"); got != nil {
		t.Errorf("got %v, want nil when claim absent", got)
	}
}

func TestExtractGroups_NonStringElementsDropped(t *testing.T) {
	raw := json.RawMessage(`["eng", 42, "sales"]`)
	got := extractGroups(map[string]json.RawMessage{"groups": raw}, "groups")
	if !reflect.DeepEqual(got, []string{"eng", "sales"}) {
		t.Errorf("got %v, want [eng sales] (non-string dropped)", got)
	}
}
