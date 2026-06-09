package config

import (
	"strings"

	domainconfig "github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/identity"
)

// ToGroupConfig maps the load-time domainconfig.RBACConfig into the
// resolution-time identity.GroupConfig consumed by the group-resolution helpers.
// Department keys and lead identifiers are normalized to lower-case + trimmed;
// blank identifiers are dropped. The DefaultGroup string is passed through
// verbatim — identity.GroupConfig.DefaultGroupOr normalizes it (and applies the
// "default" sentinel) at use.
func ToGroupConfig(in domainconfig.RBACConfig) identity.GroupConfig {
	var leads map[string][]string
	if len(in.Leads) > 0 {
		leads = make(map[string][]string, len(in.Leads))
		for dept, ids := range in.Leads {
			ndept := identity.NormalizeDept(dept)
			if ndept == "" {
				continue
			}
			normIDs := make([]string, 0, len(ids))
			for _, id := range ids {
				nid := strings.ToLower(strings.TrimSpace(id))
				if nid != "" {
					normIDs = append(normIDs, nid)
				}
			}
			if len(normIDs) > 0 {
				leads[ndept] = normIDs
			}
		}
	}
	var mapping map[string]string
	if len(in.Mapping) > 0 {
		mapping = make(map[string]string, len(in.Mapping))
		for raw, friendly := range in.Mapping {
			k := identity.NormalizeDept(raw)
			v := identity.NormalizeDept(friendly)
			if k == "" || v == "" {
				continue // drop blank key or blank value
			}
			mapping[k] = v
		}
		if len(mapping) == 0 {
			mapping = nil
		}
	}
	return identity.GroupConfig{
		GroupClaim:   in.GroupClaim,
		DefaultGroup: in.DefaultGroup,
		Leads:        leads,
		Mapping:      mapping,
	}
}
