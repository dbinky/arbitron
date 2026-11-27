// domain/engine/recovery.go
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// RecoveryAction is the pool's verdict for one running run at startup.
type RecoveryAction string

const (
	// RecoveryResume re-enters the current step: a live remote job is adopted
	// by the dispatcher, an interrupted built-in job was fenced and its
	// workspace restored, so the step repeats.
	RecoveryResume RecoveryAction = "resume"
	// RecoveryReplayCommitted re-enters the current step whose result is
	// already durably committed; the run loop replays it (P3 T5) and nothing
	// is restored or repeated.
	RecoveryReplayCommitted RecoveryAction = "replay_committed"
	// RecoveryFail marks the run failed (today's behaviour) — used when the
	// pool cannot safely recover it.
	RecoveryFail RecoveryAction = "fail"
)

// RecoveryPolicy prepares one running run for re-entry (runner.PoolRecovery).
type RecoveryPolicy interface {
	PrepareRun(ctx context.Context, inst *instance.Instance) (RecoveryAction, error)
}

const EventTypeInstanceRecovered = "instance.recovered"

var ErrNoRecoveryPolicy = errors.New("engine: RecoverRunningInstances needs a recovery policy")

// nonResumableOnRestart: steps whose re-entry would duplicate work owned by
// another reconciler, or whose session cannot move before Phase 4. They keep
// today's crash behaviour (failed; the spawn/group reconcilers settle them).
var nonResumableOnRestart = map[workflow.NodeType]string{
	workflow.NodeTypeInteractiveLLM:   "interactive steps are not resumed after a restart",
	workflow.NodeTypeInteractiveAgent: "interactive steps are not resumed after a restart",
	workflow.NodeTypeSpawnWorkflow:    "spawn steps are settled by the spawn reconciler",
	workflow.NodeTypeParallelGroup:    "group steps are settled by the group coordinator",
}

// RecoverRunningInstances replaces RecoverCrashedInstancesContext at startup
// (design §3.5): every running run whose current step can re-enter is
// prepared by rec and restarted through StartInstance; the rest are failed
// synchronously exactly as before. A SetFailed or StartInstance error aborts
// startup; a policy error fails only that run.
func (e *Engine) RecoverRunningInstances(ctx context.Context, rec RecoveryPolicy) error {
	if rec == nil {
		return ErrNoRecoveryPolicy
	}
	running, err := e.instances.ListRunning(ctx)
	var corrupt *instance.CorruptPinsError
	if errors.As(err, &corrupt) {
		for _, id := range corrupt.IDs {
			if ferr := e.failCorruptRun(ctx, id, false); ferr != nil {
				return ferr
			}
		}
	} else if err != nil {
		return fmt.Errorf("failed to list running instances: %w", err)
	}

	for i := range running {
		inst := &running[i] // ListRunning returns values; the policy takes *instance.Instance (unstick 2026-11-27)
		node := ""
		if inst.CurrentNodeID != nil {
			node = *inst.CurrentNodeID
		}
		if reason := nonResumable(inst, node); reason != "" {
			if err := e.failRecovered(ctx, inst.ID, node, reason); err != nil {
				return err
			}
			continue
		}
		action, perr := rec.PrepareRun(ctx, inst)
		if perr != nil {
			slog.Error("recovery preparation failed", "instanceID", inst.ID, "error", perr)
			if err := e.failRecovered(ctx, inst.ID, node, "recovery preparation failed"); err != nil {
				return err
			}
			continue
		}
		switch action {
		case RecoveryResume, RecoveryReplayCommitted:
			e.eventBus.Publish(inst.ID, Event{Type: EventTypeInstanceRecovered, Timestamp: time.Now(), Data: map[string]any{
				"reason": "restart", "action": string(action), "nodeId": node, "notice": instance.RecoveryNoticeText,
			}})
			if err := e.StartInstance(ctx, inst.ID); err != nil {
				return fmt.Errorf("resuming instance %s: %w", inst.ID, err)
			}
		case RecoveryFail:
			if err := e.failRecovered(ctx, inst.ID, node, "the pool could not recover the run"); err != nil {
				return err
			}
		default:
			return fmt.Errorf("recovery policy returned unknown action %q for %s", action, inst.ID)
		}
	}
	return nil
}

// nonResumable returns "" when the run can re-enter its current step.
func nonResumable(inst *instance.Instance, node string) string {
	_, nodes, _, err := parseWorkflowGraph(inst.WorkflowSnapshot)
	if err != nil {
		return "workflow snapshot cannot be parsed"
	}
	if len(nodes) == 0 { // a nil snapshot marshals to null and parses without error (unstick 2026-11-27)
		return "workflow snapshot is missing"
	}
	if node == "" {
		return ""
	}
	n, ok := nodes[node]
	if !ok {
		return "current node is not in the workflow snapshot"
	}
	return nonResumableOnRestart[n.Type]
}

func (e *Engine) failRecovered(ctx context.Context, id uuid.UUID, node, detail string) error {
	if err := e.instances.SetFailed(ctx, id, node); err != nil {
		return fmt.Errorf("failed to set instance %s as failed: %w", id, err)
	}
	e.eventBus.Publish(id, Event{Type: EventTypeInstanceRecovered, Timestamp: time.Now(), Data: map[string]any{
		"reason": "restart", "action": string(RecoveryFail), "nodeId": node, "detail": detail,
	}})
	return nil
}
