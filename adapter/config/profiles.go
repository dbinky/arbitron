package config

import (
	"slices"

	domainconfig "github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/provider"
)

// ProfilesFromConfig maps the config-file profile shape onto domain provider
// profiles, converting each Kind string into a provider.ProfileKind. It is the
// seam the composition root uses to gather the boot-time profile seed candidates
// (profile-editing Phase 2). Mapping is straight-through; an out-of-range kind
// passes through as a literal ProfileKind and is rejected downstream (the seed
// validator and workflow validation), not at this mapping step.
func ProfilesFromConfig(cfgs []domainconfig.ProfileConfig) []provider.Profile {
	if cfgs == nil {
		return nil
	}
	out := make([]provider.Profile, len(cfgs))
	for i, c := range cfgs {
		mode := provider.SelectionMode(c.SelectionMode)
		if mode == "" {
			mode = provider.SelectionPinned
		}
		var target *provider.FollowTarget
		if c.FollowTarget != nil {
			target = &provider.FollowTarget{Provider: c.FollowTarget.Provider, Tier: c.FollowTarget.Tier}
		}
		out[i] = provider.Profile{
			SelectionMode: mode,
			FollowTarget:  target,
			Alias:         c.Alias,
			Kind:          provider.ProfileKind(c.Kind),
			ConnectionRef: c.ConnectionRef,
			Model:         c.Model,
			Backend:       c.Backend,
			AllowedTools:  slices.Clone(c.AllowedTools),
		}
	}
	return out
}
