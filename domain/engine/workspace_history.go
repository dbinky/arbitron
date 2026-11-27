package engine

import (
	"context"
	"fmt"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// WorkspaceHistorySource reports a run's durable workspace home moves.
type WorkspaceHistorySource interface {
	WorkspaceHistory(ctx context.Context, runID uuid.UUID) ([]instance.WorkspaceMove, error)
}

// SetWorkspaceHistorySource installs the history projection before serving.
func (e *Engine) SetWorkspaceHistorySource(s WorkspaceHistorySource) { e.workspaceHistory = s }

func (e *Engine) attachWorkspaceHistory(ctx context.Context, inst *instance.Instance) error {
	if e.workspaceHistory == nil || inst == nil {
		return nil
	}
	moves, err := e.workspaceHistory.WorkspaceHistory(ctx, inst.ID)
	if err != nil {
		return fmt.Errorf("reading workspace history for %s: %w", inst.ID, err)
	}
	inst.WorkspaceMoves = moves
	return nil
}

// movesForExecution joins on the exact placement job, never wall-clock ranges,
// node names or retry numbers (which repeat across loop visits). Maintenance
// moves remain visible on the instance but cannot be attributed to an attempt.
func movesForExecution(all []instance.WorkspaceMove, ex instance.NodeExecution) []instance.WorkspaceMove {
	jobs := make(map[string]bool, len(ex.PlacementHistory))
	for _, placement := range ex.PlacementHistory {
		jobs[placement.JobID] = true
	}
	var out []instance.WorkspaceMove
	for _, move := range all {
		if move.TargetJobID != nil && jobs[move.TargetJobID.String()] {
			out = append(out, move)
		}
	}
	return out
}
