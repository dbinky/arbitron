package config

import (
	"fmt"
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
//
// It REJECTS a chained mapping: a configuration where some normalized mapping
// VALUE is also a normalized mapping KEY. identity.ReconcileMappings rewrites
// stored departments over Go's randomized map range, so a chain (e.g.
// eng→engineering, engineering→r-and-d) would land rows under a department
// decided by iteration order — non-deterministic, boot-dependent authorization
// state. This is the single normalization chokepoint, so the guard lives here:
// the domain reconciler is entitled to trust a flat, non-chained mapping. The
// fix is to forbid the chain at load, NOT to resolve it with a transitive pass.
// A self-mapping (key == value) is harmless and is not treated as a chain.
func ToGroupConfig(in domainconfig.RBACConfig) (identity.GroupConfig, error) {
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
		// Chained-mapping guard: an O(n) pass over the normalized map. If any
		// normalized VALUE also appears as a normalized KEY, the mapping is a chain
		// (the value would itself be remapped on a subsequent entry), which makes
		// the boot-time department reconcile non-deterministic. A self-mapping
		// (k == v) is excluded — it is a no-op, not a chain.
		for k, v := range mapping {
			if v == k {
				continue
			}
			if _, ok := mapping[v]; ok {
				return identity.GroupConfig{}, fmt.Errorf(
					"rbac mapping is chained: value %q (from key %q) is also a mapping key — "+
						"flatten the mapping so no value is also a key (a transitive chain produces "+
						"non-deterministic department scoping at boot)", v, k)
			}
		}
	}
	return identity.GroupConfig{
		GroupClaim:   in.GroupClaim,
		DefaultGroup: in.DefaultGroup,
		Leads:        leads,
		Mapping:      mapping,
	}, nil
}
