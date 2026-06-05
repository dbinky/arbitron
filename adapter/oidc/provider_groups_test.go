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

// edge: empty JSON array → nil (len(out)==0 guard after iterating zero elements)
func TestExtractGroups_EmptyArrayReturnsNil(t *testing.T) {
	raw := json.RawMessage(`[]`)
	if got := extractGroups(map[string]json.RawMessage{"groups": raw}, "groups"); got != nil {
		t.Errorf("got %v, want nil for empty array", got)
	}
}

// edge: every element is non-string → all dropped → nil
func TestExtractGroups_AllNonStringElementsReturnsNil(t *testing.T) {
	raw := json.RawMessage(`[42, true, null]`)
	if got := extractGroups(map[string]json.RawMessage{"groups": raw}, "groups"); got != nil {
		t.Errorf("got %v, want nil when all elements are non-string", got)
	}
}

// edge: single empty-string value → nil (the single != "" guard rejects it)
func TestExtractGroups_EmptySingleStringReturnsNil(t *testing.T) {
	raw := json.RawMessage(`""`)
	if got := extractGroups(map[string]json.RawMessage{"groups": raw}, "groups"); got != nil {
		t.Errorf("got %v, want nil for empty single string", got)
	}
}

// failure: JSON null claim value → nil (null unmarshals as nil []any; out stays empty)
func TestExtractGroups_NullClaimReturnsNil(t *testing.T) {
	raw := json.RawMessage(`null`)
	if got := extractGroups(map[string]json.RawMessage{"groups": raw}, "groups"); got != nil {
		t.Errorf("got %v, want nil for null claim value", got)
	}
}

// success variant: a custom claim name is honoured; "groups" is not hardcoded
func TestExtractGroups_CustomClaimName(t *testing.T) {
	raw := json.RawMessage(`["admin","ops"]`)
	got := extractGroups(map[string]json.RawMessage{"memberOf": raw}, "memberOf")
	if !reflect.DeepEqual(got, []string{"admin", "ops"}) {
		t.Errorf("got %v, want [admin ops]", got)
	}
}
