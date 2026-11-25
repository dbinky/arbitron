package engine

import (
	"context"
	"log/slog"

	"github.com/arbitron/arbitron/domain/variable"
	"github.com/google/uuid"
)

// prepareWorkspace creates the per-instance workspace, seeds {{workspace}}
// into vars, and persists the resulting snapshot so resumed turns can read
// the same value out of the DB. Returns false when any step fails — the
// caller has already marked the instance failed and should return from run.
//
// SF9 / PR #8 thread 3291907748.
func (e *Engine) prepareWorkspace(
	ctx context.Context,
	instanceID uuid.UUID,
	currentNodeID string,
	vars *variable.VariableStore,
) (ok bool) {
	workspacePath, wsErr := e.workspaceManager.EnsureInstanceDir(instanceID)
	if wsErr != nil {
		slog.Error("failed to ensure instance workspace",
			"instanceID", instanceID, "error", wsErr)
		e.failInstance(ctx, instanceID, currentNodeID)
		return false
	}
	// SetReserved bypasses the reserved-name check; the engine is the
	// authoritative seeder for "workspace". See variable.ReservedInputNames.
	vars.SetReserved(ReservedVarWorkspace, workspacePath)
	// Persist immediately so the workspace path is in inst.Variables before
	// any node runs — ConversationHandler reads it from the DB for resumed
	// interactive turns.
	if err := e.instances.UpdateVariables(ctx, instanceID, vars.Snapshot()); err != nil {
		slog.Error("failed to persist seeded workspace variable",
			"instanceID", instanceID, "error", err)
		e.failInstance(ctx, instanceID, currentNodeID)
		return false
	}
	return e.ensureWorkspaceBaseline(ctx, instanceID, workspacePath, currentNodeID)
}

// cleanupWorkspace removes the per-instance workspace. Symmetric with
// prepareWorkspace: only called from the happy-path completion site.
// Failures are logged but not propagated — the instance is already in a
// terminal state.
//
// SF9 / PR #8 thread 3291907748.
func (e *Engine) cleanupWorkspace(instanceID uuid.UUID) {
	if e.workspaceManager == nil {
		return
	}
	if rmErr := e.workspaceManager.RemoveInstanceDir(instanceID); rmErr != nil {
		slog.Warn("failed to remove instance workspace",
			"instanceID", instanceID, "error", rmErr)
		return
	}
	notifyRemoval(e.removalHook, instanceID)
}

// warnWorkspaceRetained emits a slog.Warn with the on-disk size and file
// count when a workspace is kept past terminal state (e.g. instance failed
// for retention/debugging). Surfaces the sensitive-data-retention surface
// the security expert flagged in PR #8 thread 3291941512 — operators see
// how much agent-generated content is sitting on disk and for how long.
//
// reason is a short tag (e.g. "instance_failed", "set_completed_failed").
func (e *Engine) warnWorkspaceRetained(instanceID uuid.UUID, reason string) {
	if e.workspaceManager == nil {
		return
	}
	bytes, files, err := e.workspaceManager.InstanceDirStats(instanceID)
	if err != nil {
		slog.Warn("workspace retained but stat failed",
			"instanceID", instanceID, "reason", reason, "error", err)
		return
	}
	if files == 0 && bytes == 0 {
		// Nothing to retain — likely the executor never wrote anything.
		return
	}
	slog.Warn("workspace retained on disk",
		"instanceID", instanceID,
		"reason", reason,
		"bytes", bytes,
		"files", files,
	)
}
