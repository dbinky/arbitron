package config

import (
	"reflect"
	"testing"

	domainconfig "github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/identity"
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
