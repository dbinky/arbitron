package engine

import (
	"context"
	"log/slog"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// GroupTerminalHandler receives terminal transitions of parallel-group tasks.
// Reconcile remains the correctness authority; this callback accelerates wakes.
//
// Calls are synchronous, with no engine lock held. Implementations must allow
// re-entry: cancelling a sibling through the engine dispatches its terminal
// transition on the same goroutine.
type GroupTerminalHandler interface {
	OnTaskTerminal(ctx context.Context, childID uuid.UUID)
}

var _ GroupTerminalHandler = (*GroupCoordinator)(nil)

// SetGroupTerminalHandler wires the coordinator before FreezeExecutors and
// before any runs can finish. A nil handler is a programming error.
func (e *Engine) SetGroupTerminalHandler(h GroupTerminalHandler) {
	if h == nil {
		panic("engine: SetGroupTerminalHandler requires a non-nil GroupTerminalHandler")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.groupHandler = h
}

func (e *Engine) groupTerminalHandler() GroupTerminalHandler {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.groupHandler
}

// Only the coordinator may decide whether a group parent can continue. Group
// tasks must never fall through to the legacy wait-for-child wake.
func (e *Engine) dispatchGroupTerminal(ctx context.Context, child *instance.Instance) {
	if !child.State.IsTerminal() {
		return
	}
	h := e.groupTerminalHandler()
	if h == nil {
		runID, key := "", ""
		if child.GroupRunID != nil {
			runID = child.GroupRunID.String()
		}
		if child.GroupTaskKey != nil {
			key = *child.GroupTaskKey
		}
		slog.Warn("group task reached a terminal state but no group terminal handler is wired; only the group reconciler can settle its group",
			"childID", child.ID, "groupRunID", runID, "taskKey", key, "state", child.State)
		return
	}
	h.OnTaskTerminal(ctx, child.ID)
}
