package engine

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// WorkspaceRemovalHook is told after a run's server workspace directory has
// been removed, so runners holding copies can delete theirs.
type WorkspaceRemovalHook interface {
	WorkspaceRemoved(ctx context.Context, runID uuid.UUID)
}

const removalHookTimeout = 30 * time.Second

// SetWorkspaceRemovalHook installs the hook for the engine's completion cleanup.
// Group launchers and pruners use WithWorkspaceRemovalHook instead.
func (e *Engine) SetWorkspaceRemovalHook(h WorkspaceRemovalHook) { e.removalHook = h }

func notifyRemoval(h WorkspaceRemovalHook, id uuid.UUID) {
	if h == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), removalHookTimeout)
	defer cancel()
	h.WorkspaceRemoved(ctx, id)
}

type hookedWorkspaces struct {
	WorkspaceManager
	hook WorkspaceRemovalHook
}

// WithWorkspaceRemovalHook notifies h after each successful removal by wm.
func WithWorkspaceRemovalHook(wm WorkspaceManager, h WorkspaceRemovalHook) WorkspaceManager {
	return hookedWorkspaces{WorkspaceManager: wm, hook: h}
}

func (w hookedWorkspaces) RemoveInstanceDir(id uuid.UUID) error {
	if err := w.WorkspaceManager.RemoveInstanceDir(id); err != nil {
		return err
	}
	notifyRemoval(w.hook, id)
	return nil
}
