package config

import (
	"reflect"
	"testing"

	domainconfig "github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToGroupConfig(t *testing.T) {
	in := domainconfig.RBACConfig{
		GroupClaim:   "groups",
		DefaultGroup: "Default",
		Leads: map[string][]string{
			"Engineering": {"Alice@Corp.Example", " bob-subject ", ""},
			"  Sales  ":   {"carol@corp.example"},
		},
	}
	got, err := ToGroupConfig(in)
	if err != nil {
		t.Fatalf("ToGroupConfig returned unexpected error: %v", err)
	}
	want := identity.GroupConfig{
		GroupClaim:   "groups",
		DefaultGroup: "Default", // DefaultGroupOr normalizes at use; mapping passes through
		Leads: map[string][]string{
			"engineering": {"alice@corp.example", "bob-subject"},
			"sales":       {"carol@corp.example"},
		},
	}
	if got.GroupClaim != want.GroupClaim {
		t.Errorf("GroupClaim = %q, want %q", got.GroupClaim, want.GroupClaim)
	}
	if got.DefaultGroup != want.DefaultGroup {
		t.Errorf("DefaultGroup = %q, want %q", got.DefaultGroup, want.DefaultGroup)
	}
	if !reflect.DeepEqual(got.Leads, want.Leads) {
		t.Errorf("Leads = %#v, want %#v", got.Leads, want.Leads)
	}
}

func TestToGroupConfig_EmptyLeads(t *testing.T) {
	got, err := ToGroupConfig(domainconfig.RBACConfig{})
	if err != nil {
		t.Fatalf("ToGroupConfig returned unexpected error: %v", err)
	}
	if len(got.Leads) != 0 {
		t.Errorf("Leads = %#v, want empty", got.Leads)
	}
}

// A department whose every lead identifier is blank should be dropped entirely
// from the output — it must not appear as an empty-slice entry that would
// silently match no identifiers and pollute the leads map.
func TestToGroupConfig_AllBlankLeadIDs_DeptDropped(t *testing.T) {
	in := domainconfig.RBACConfig{
		Leads: map[string][]string{
			"engineering": {"", "   "}, // all blank → dept dropped
			"sales":       {"carol@corp.example"},
		},
	}
	got, err := ToGroupConfig(in)
	if err != nil {
		t.Fatalf("ToGroupConfig returned unexpected error: %v", err)
	}
	if _, ok := got.Leads["engineering"]; ok {
		t.Errorf("Leads[engineering] = %v, want dept absent (all-blank IDs should drop the dept)", got.Leads["engineering"])
	}
	if len(got.Leads["sales"]) != 1 {
		t.Errorf("Leads[sales] = %v, want [carol@corp.example]", got.Leads["sales"])
	}
}

// GroupClaim and DefaultGroup are passed through verbatim so the caller
// (GroupConfig.DefaultGroupOr) can apply the sentinel fallback at use time.
func TestToGroupConfig_PassThroughFields(t *testing.T) {
	in := domainconfig.RBACConfig{GroupClaim: "memberOf", DefaultGroup: "everyone"}
	got, err := ToGroupConfig(in)
	if err != nil {
		t.Fatalf("ToGroupConfig returned unexpected error: %v", err)
	}
	if got.GroupClaim != "memberOf" {
		t.Errorf("GroupClaim = %q, want memberOf", got.GroupClaim)
	}
	if got.DefaultGroup != "everyone" {
		t.Errorf("DefaultGroup = %q, want everyone", got.DefaultGroup)
	}
}

func TestToGroupConfig_NormalizesMappingKeysAndValues(t *testing.T) {
	in := domainconfig.RBACConfig{
		GroupClaim:   "groups",
		DefaultGroup: "default",
		Mapping: map[string]string{
			"GUID-123": "Engineering",
			" guid-b ": " Sales ",
		},
	}
	got, err := ToGroupConfig(in)
	require.NoError(t, err)
	// Keys and values are normalized (trimmed + lower-cased).
	assert.Equal(t, "engineering", got.Mapping["guid-123"])
	assert.Equal(t, "sales", got.Mapping["guid-b"])
}

func TestToGroupConfig_DropsBlankMappingEntries(t *testing.T) {
	in := domainconfig.RBACConfig{Mapping: map[string]string{
		"   ":      "engineering", // blank key dropped
		"guid-123": "   ",         // blank value dropped
		"guid-ok":  "platform",
	}}
	got, err := ToGroupConfig(in)
	require.NoError(t, err)
	_, hasBlankKey := got.Mapping[""]
	assert.False(t, hasBlankKey, "blank key dropped")
	_, hasBlankVal := got.Mapping["guid-123"]
	assert.False(t, hasBlankVal, "entry with blank value dropped")
	assert.Equal(t, "platform", got.Mapping["guid-ok"])
}

func TestToGroupConfig_NilMapping_YieldsNilMapping(t *testing.T) {
	got, err := ToGroupConfig(domainconfig.RBACConfig{})
	assert.NoError(t, err)
	assert.Nil(t, got.Mapping)
}

// A chained mapping — a normalized VALUE that is also a normalized KEY — makes
// identity.ReconcileMappings non-deterministic over Go's randomized map range
// (the final stored department depends on iteration order, an authorization
// outcome decided by chance). ToGroupConfig is the single normalization
// chokepoint, so it must REJECT the chain at load rather than forbid it later.
func TestToGroupConfig_RejectsChainedMapping(t *testing.T) {
	in := domainconfig.RBACConfig{Mapping: map[string]string{
		"eng":         "engineering",
		"engineering": "r-and-d", // "engineering" is both a value (above) and a key
	}}
	_, err := ToGroupConfig(in)
	require.Error(t, err, "a chained mapping must be rejected at load")
	assert.Contains(t, err.Error(), "engineering",
		"the error must name the offending department that is both a value and a key")
}

// The chain check runs against NORMALIZED keys and values: a value whose
// case/whitespace differs from a key still collides once normalized.
func TestToGroupConfig_RejectsChainedMapping_AfterNormalization(t *testing.T) {
	in := domainconfig.RBACConfig{Mapping: map[string]string{
		"eng":           " Engineering ", // normalizes to "engineering" (a value)
		"  ENGINEERING": "r-and-d",       // normalizes to "engineering" (a key)
	}}
	_, err := ToGroupConfig(in)
	require.Error(t, err, "a chain that only collides after normalization must still be rejected")
}

// A flat mapping (no value is also a key) is accepted unchanged. A self-mapping
// (key == value) is NOT a chain and must not trip the guard.
func TestToGroupConfig_FlatMapping_Accepted(t *testing.T) {
	in := domainconfig.RBACConfig{Mapping: map[string]string{
		"guid-123": "engineering",
		"guid-b":   "sales",
		"platform": "platform", // self-map: key==value, harmless, not a chain
	}}
	got, err := ToGroupConfig(in)
	require.NoError(t, err, "a flat (non-chained) mapping must be accepted")
	assert.Equal(t, "engineering", got.Mapping["guid-123"])
	assert.Equal(t, "sales", got.Mapping["guid-b"])
}
