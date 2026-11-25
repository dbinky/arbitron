package provider

import (
	"fmt"
	"github.com/arbitron/arbitron/adapter/codex"
	domprovider "github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
	"slices"
	"strings"
)

// EffectiveAgentTools resolves profile, request, then native global scope.
// A nil result denotes the backend's unrestricted default.
func (f *Factory) EffectiveAgentTools(b domprovider.ResolvedBinding, requested []string) ([]string, error) {
	scope := b.AllowedTools
	if len(scope) == 0 {
		scope = requested
	}
	switch strings.TrimSpace(b.Backend) {
	case workflow.BackendClaudeCode:
		if len(scope) == 0 {
			scope = f.deps.ClaudeAgentConfig.AllowedTools
		}
	case workflow.BackendCodex:
		if err := codex.CheckToolScope(scope); err != nil {
			return nil, err
		}
	case workflow.BackendOpencode, workflow.BackendPi:
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownBackend, b.Backend)
	}
	if len(scope) == 0 {
		return nil, nil
	}
	return slices.Clone(scope), nil
}
