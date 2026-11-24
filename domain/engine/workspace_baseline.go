package engine

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
)

// WorkspaceBaselineInitializer creates a run's committed workspace baseline
// before any operation of the run can be dispatched. P3 T6's
// runner.BaselineInitializer implements it; P3 T13 installs it.
type WorkspaceBaselineInitializer interface {
	EnsureBaseline(ctx context.Context, runID uuid.UUID, dir string) error
}

// SetWorkspaceBaselineInitializer installs the baseline hook. Call it before
// executors freeze / the runtime starts. nil keeps the pre-Phase-3 behaviour.
func (e *Engine) SetWorkspaceBaselineInitializer(i WorkspaceBaselineInitializer) {
	e.workspaceBaseline = i
}

// ensureWorkspaceBaseline runs the hook. A failure fails the run visibly and
// returns false, so nothing is dispatched.
func (e *Engine) ensureWorkspaceBaseline(ctx context.Context, instanceID uuid.UUID, dir, currentNodeID string) bool {
	if e.workspaceBaseline == nil {
		return true
	}
	if err := e.workspaceBaseline.EnsureBaseline(ctx, instanceID, dir); err != nil {
		slog.Error("failed to initialize the workspace baseline; no operation of the run is dispatched",
			"instanceID", instanceID, "error", err)
		e.failInstance(ctx, instanceID, currentNodeID)
		return false
	}
	return true
}
