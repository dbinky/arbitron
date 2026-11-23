package runner

import (
	"context"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

// HermeticChatAffinity permits chat bindings to leave the built-in runner.
// Each call is further restricted by its identity, session and tool needs.
func HermeticChatAffinity(b provider.ResolvedBinding) Affinity {
	if b.Kind == provider.ProfileKindChat {
		return AffinityNone
	}
	return AffinityBuiltIn
}

func hermeticCallAffinity(ctx context.Context, base Affinity, b provider.ResolvedBinding, req engine.ChatRequest) Affinity {
	if base != AffinityNone {
		return base
	}
	id, ok := engine.StepIdentityFromContext(ctx)
	if !ok || (id.Kind != engine.OperationNode && id.Kind != engine.OperationChildInput) {
		return AffinityBuiltIn
	}
	if req.SessionID != "" {
		return AffinityBuiltIn
	}
	if !req.SuppressTools && (len(req.ToolScope) > 0 || len(b.AllowedTools) > 0) {
		return AffinityBuiltIn
	}
	return AffinityNone
}
