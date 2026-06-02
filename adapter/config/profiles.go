package config

import (
	domainconfig "github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/provider"
)

// ProfilesFromConfig maps the config-file profile shape onto domain provider
// profiles, converting each Kind string into a provider.ProfileKind. It is the
// seam main.go (Task 6) uses to build a StaticProfileResolver. Mapping is
// straight-through; an out-of-range kind passes through as a literal
// ProfileKind and is rejected later by NewStaticProfileResolver.
func ProfilesFromConfig(cfgs []domainconfig.ProfileConfig) []provider.Profile {
	if cfgs == nil {
		return nil
	}
	out := make([]provider.Profile, len(cfgs))
	for i, c := range cfgs {
		out[i] = provider.Profile{
			Alias:         c.Alias,
			Kind:          provider.ProfileKind(c.Kind),
			ConnectionRef: c.ConnectionRef,
			Model:         c.Model,
		}
	}
	return out
}
