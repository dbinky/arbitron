package engine

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// AdoptStrandedParent rescues a parent left in `running` at the awaited spawn
// node nodeID with no walker driving it: the state a pre-fix resume left the
// 2026-11-19 feature-pipeline run in, where the child's terminal wake skipped a
// non-waiting parent and nothing else would ever move it.
//
// It moves the parent `running -> waiting` (conditional on both state and
// node) so the ordinary spawn wake machinery can re-queue it; on re-entry the
// spawn node short-circuits on the terminal link and routes success or
// failure. It reports whether it moved the parent.
//
// The live-walker check and the write happen under e.mu. Every path that puts
// a run into `running` and registers its walker does so under the same lock
// (StartInstance, Retry, resumeFromRunning), so a run that is merely starting
// can never be mistaken for a stranded one. A parent with a live walker is
// never touched: adopting it would hand one run to two walkers.
// enterRunning writes id `running` and registers its walker context as ONE step
// under e.mu, so no reader holding e.mu (AdoptStrandedParent) can observe a
// running run without its walker. On a write error nothing is registered.
func (e *Engine) enterRunning(ctx context.Context, id uuid.UUID) (context.Context, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.instances.UpdateState(ctx, id, instance.StateRunning); err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	e.contexts[id] = cancel
	return runCtx, nil
}

func (e *Engine) AdoptStrandedParent(ctx context.Context, parentID uuid.UUID, nodeID string) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, live := e.contexts[parentID]; live {
		return false, nil
	}
	ok, err := e.spawnLinks.SuspendStrandedAt(ctx, parentID, nodeID)
	if err != nil {
		return false, fmt.Errorf("adopting stranded parent %s: %w", parentID, err)
	}
	if ok {
		slog.Warn("adopted parent stranded in running at an awaited spawn node",
			"parentID", parentID, "nodeID", nodeID)
	}
	return ok, nil
}
