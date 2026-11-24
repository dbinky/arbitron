package engine

import "context"

// ExecutedModelProber is an agent adapter's restricted exact-model check: one
// bounded turn on its pinned binding in workDir, returning the model the
// harness's own record shows executed and the harness session/thread id.
// Implemented by adapter/claudecli.AgentAdapter and adapter/codex.Provider.
type ExecutedModelProber interface {
	ProbeExecutedModel(ctx context.Context, prompt, workDir string) (model, sessionID string, err error)
}
