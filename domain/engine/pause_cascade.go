package engine

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// Pause cascade.
//
// Pausing a run pauses every active run it spawned, at any depth, and records
// on each descendant WHICH ancestor's pause stopped it (the cascade marker,
// SpawnLinkRepository.MarkCascadePaused). Resuming that ancestor resumes only
// the descendants carrying its marker, so a pause an operator placed on a child
// directly is never undone by resuming the parent.
//
// The walk mirrors cascadeCancel: every child from ListChildren is visited,
// terminal and already-paused ones included (they may own active descendants);
// each is re-authorized; an authorization failure halts that branch; a visited
// set bounds corrupt link cycles; and the caller detaches ctx from request
// cancellation so a disconnect cannot strand half a tree running.

// cascadePause pauses parentID's active descendants on behalf of by and returns
// how many it paused.
func (e *Engine) cascadePause(ctx context.Context, parentID, by uuid.UUID, visited map[uuid.UUID]bool) int {
	children, err := e.spawnLinks.ListChildren(ctx, parentID)
	if err != nil {
		slog.Error("pause cascade: failed to list children", "parentID", parentID, "error", err)
		return 0
	}
	paused := 0
	for _, child := range children {
		if visited[child.ID] {
			continue
		}
		visited[child.ID] = true

		ok, perr := e.pauseDescendant(ctx, child.ID, by)
		if perr != nil {
			slog.Warn("pause cascade: failed to pause child, branch not descended",
				"parentID", parentID, "childID", child.ID, "listedState", child.State, "error", perr)
			continue
		}
		if ok {
			paused++
		}
		paused += e.cascadePause(ctx, child.ID, by, visited)
	}
	return paused
}

// pauseDescendant authorizes id and, if it is running, waiting or queued,
// moves it to paused with by's cascade marker. It reports whether it paused
// the run; a run that is not pausable (terminal, already paused, or one that
// finished after it was listed) is not an error. An authorization or storage
// failure is returned so the caller stops descending that branch.
func (e *Engine) pauseDescendant(ctx context.Context, id, by uuid.UUID) (bool, error) {
	inst, err := e.authorizeIntervention(ctx, id)
	if err != nil {
		return false, err
	}
	ok, err := e.spawnLinks.MarkCascadePaused(ctx, id, by)
	if err != nil {
		return false, fmt.Errorf("cascade-pausing %s: %w", id, err)
	}
	if !ok {
		return false, nil
	}
	e.eventBus.Publish(id, Event{Type: "instance.paused", Timestamp: time.Now()})
	if inst.State == instance.StateRunning {
		e.queueManager.NotifyCapacityFreed(context.Background())
	}
	e.recordIntervention(ctx, inst, instance.ActionPause, nil)
	return true, nil
}
