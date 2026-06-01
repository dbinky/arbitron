// Package provider implements adapters for the domain provider ports. It hosts
// the StaticProfileResolver: an immutable in-memory profile registry built once
// at startup from config.
package provider

import (
	"fmt"

	"github.com/arbitron/arbitron/domain/provider"
)

// StaticProfileResolver resolves aliases against an immutable in-memory registry
// loaded once at startup from config. In P1 every profile resolves to
// claude-cli: kind chat -> ChatAdapter, kind agent -> AgentAdapter (the engine
// selects the adapter by Profile.Kind).
type StaticProfileResolver struct {
	byAlias map[string]provider.Profile
}

var _ provider.ProfileResolver = (*StaticProfileResolver)(nil)

// NewStaticProfileResolver builds the alias->Profile map. It fails fast at
// construction (startup) rather than at resolve time on a duplicate alias or an
// unknown kind value.
func NewStaticProfileResolver(profiles []provider.Profile) (*StaticProfileResolver, error) {
	byAlias := make(map[string]provider.Profile, len(profiles))
	for _, p := range profiles {
		if _, dup := byAlias[p.Alias]; dup {
			return nil, fmt.Errorf("provider: duplicate profile alias %q", p.Alias)
		}
		switch p.Kind {
		case provider.ProfileKindChat, provider.ProfileKindAgent:
			// ok
		default:
			return nil, fmt.Errorf("provider: profile %q has unknown kind %q (want chat or agent)", p.Alias, p.Kind)
		}
		byAlias[p.Alias] = p
	}
	return &StaticProfileResolver{byAlias: byAlias}, nil
}

// Resolve returns the profile for an exact alias match, or ErrUnknownProfile
// (wrapped with the alias for context) on a miss.
func (r *StaticProfileResolver) Resolve(alias string) (provider.Profile, error) {
	p, ok := r.byAlias[alias]
	if !ok {
		return provider.Profile{}, fmt.Errorf("%w: %q", provider.ErrUnknownProfile, alias)
	}
	return p, nil
}
