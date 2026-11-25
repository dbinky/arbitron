package runner

import (
	"context"
	"slices"
)

// RequiredModelSource lists the exact Anthropic subscription models the
// server's profiles need. The runner verifies each natively (contracts D1).
type RequiredModelSource interface {
	RequiredClaudeModels(ctx context.Context) ([]string, error)
}

const evtModelsUnavailable = "runner.models_unavailable"

// SetRequiredModels installs the admission model source. Call before Admit;
// nil means no models are requested.
func (h *Hub) SetRequiredModels(s RequiredModelSource) {
	h.mu.Lock()
	h.requiredModels = s
	h.mu.Unlock()
}

func (h *Hub) admittedPayload(ctx context.Context, id RunnerID) *AdmittedPayload {
	h.mu.Lock()
	src := h.requiredModels
	h.mu.Unlock()
	out := &AdmittedPayload{}
	if src == nil {
		return out
	}
	models, err := src.RequiredClaudeModels(ctx)
	if err != nil {
		h.record(ctx, evtModelsUnavailable, id, "", Cause{Kind: "system", Actor: string(id)}, map[string]any{"error": "model source failed"})
		return out
	}
	for _, m := range models {
		if m != "" && !slices.Contains(out.ClaudeModels, m) {
			out.ClaudeModels = append(out.ClaudeModels, m)
		}
	}
	slices.Sort(out.ClaudeModels)
	if len(out.ClaudeModels) > maxRemoteCapabilities {
		out.ClaudeModels = out.ClaudeModels[:maxRemoteCapabilities]
	}
	return out
}
