package config

import (
	"reflect"
	"testing"

	domainconfig "github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/stretchr/testify/assert"
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
	got := ToGroupConfig(in)
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
	got := ToGroupConfig(domainconfig.RBACConfig{})
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
	got := ToGroupConfig(in)
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
	got := ToGroupConfig(in)
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
	got := ToGroupConfig(in)
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
	got := ToGroupConfig(in)
	_, hasBlankKey := got.Mapping[""]
	assert.False(t, hasBlankKey, "blank key dropped")
	_, hasBlankVal := got.Mapping["guid-123"]
	assert.False(t, hasBlankVal, "entry with blank value dropped")
	assert.Equal(t, "platform", got.Mapping["guid-ok"])
}

func TestToGroupConfig_NilMapping_YieldsNilMapping(t *testing.T) {
	got := ToGroupConfig(domainconfig.RBACConfig{})
	assert.Nil(t, got.Mapping)
}
