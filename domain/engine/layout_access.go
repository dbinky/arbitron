package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

var errNoWorkflowSnapshot = errors.New("instance has no workflow snapshot")

// InstanceLayoutSnapshot returns the immutable run definition under GetInstance's
// view policy. It never looks up the current workflow.
func (e *Engine) InstanceLayoutSnapshot(ctx context.Context, id uuid.UUID) (*workflow.Workflow, error) {
	inst, err := e.GetInstance(ctx, id)
	if err != nil {
		return nil, err
	}
	wf, err := DecodeWorkflowSnapshot(inst.WorkflowSnapshot)
	if err != nil {
		return nil, fmt.Errorf("instance %s: %w", id, err)
	}
	return wf, nil
}

// AuthorizeInstanceLayoutRetry applies intervention policy without mutating the run.
func (e *Engine) AuthorizeInstanceLayoutRetry(ctx context.Context, id uuid.UUID) (*workflow.Workflow, error) {
	inst, err := e.authorizeIntervention(ctx, id)
	if err != nil {
		return nil, err
	}
	wf, err := DecodeWorkflowSnapshot(inst.WorkflowSnapshot)
	if err != nil {
		return nil, fmt.Errorf("instance %s: %w", id, err)
	}
	return wf, nil
}

// DecodeWorkflowSnapshot reverses TriggerWithDepartment's JSON snapshot encoding,
// preserving presentation metadata and legacy positions.
func DecodeWorkflowSnapshot(snapshot map[string]any) (*workflow.Workflow, error) {
	if len(snapshot) == 0 {
		return nil, errNoWorkflowSnapshot
	}
	b, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("marshalling workflow snapshot: %w", err)
	}
	var wf workflow.Workflow
	if err := json.Unmarshal(b, &wf); err != nil {
		return nil, fmt.Errorf("parsing workflow snapshot: %w", err)
	}
	if len(wf.Nodes) == 0 {
		return nil, fmt.Errorf("parsing workflow snapshot: no nodes")
	}
	return &wf, nil
}
