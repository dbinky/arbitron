package main

import (
	"context"
	"slices"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
)

// profileClaudeModels lists the exact Anthropic subscription models the
// server's chat profiles resolve to (contracts D1).
type profileClaudeModels struct {
	profiles provider.ProfileLister
	resolver provider.ProfileResolver // live, non-pinning
}

var _ runner.RequiredModelSource = profileClaudeModels{}

func (p profileClaudeModels) RequiredClaudeModels(ctx context.Context) ([]string, error) {
	all, err := p.profiles.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, prof := range all {
		if prof.Kind != provider.ProfileKindChat || prof.ConnectionRef != "" {
			continue
		}
		b, err := p.resolver.Resolve(ctx, prof.Alias)
		if err != nil {
			continue
		} // Unresolvable profiles cannot run anywhere.
		if modelidentity.ClassifyFamily("claude-cli", b.Model) == "anthropic" && !slices.Contains(out, b.Model) {
			out = append(out, b.Model)
		}
	}
	slices.Sort(out)
	return out, nil
}
