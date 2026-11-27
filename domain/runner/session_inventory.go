package runner

import (
	"context"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// WaitingInstances lists instances parked at a node.
type WaitingInstances interface {
	ListWaitingAtNode(ctx context.Context) ([]instance.Instance, error)
}

// GroupRunReader reads a parent's latest group run for a node.
// A nil run with no error means there is no run.
type GroupRunReader interface {
	GetByParentNode(ctx context.Context, parentID uuid.UUID, nodeID string) (*instance.GroupRun, error)
}

// InteractiveSessionInventory counts the open sessions that block a drain.
// Phase 3 interactive and conflict-resolution sessions all run on built-in.
type InteractiveSessionInventory struct {
	instances WaitingInstances
	groups    GroupRunReader
}

func NewInteractiveSessionInventory(i WaitingInstances, g GroupRunReader) (*InteractiveSessionInventory, error) {
	if i == nil || g == nil {
		return nil, errors.New("runner: interactive session inventory requires instance and group-run reads")
	}
	return &InteractiveSessionInventory{instances: i, groups: g}, nil
}

// CountPinned fails closed on unreadable instance or group state.
func (v *InteractiveSessionInventory) CountPinned(ctx context.Context, id RunnerID) (int, error) {
	if id != BuiltInRunnerID {
		return 0, nil
	}
	insts, err := v.instances.ListWaitingAtNode(ctx)
	if err != nil {
		return 0, fmt.Errorf("runner: interactive session inventory: %w", err)
	}
	n := 0
	for _, inst := range insts {
		if inst.CurrentNodeID == nil || *inst.CurrentNodeID == "" {
			continue
		}
		node, err := engine.FindNodeByID(inst.WorkflowSnapshot, *inst.CurrentNodeID)
		if err != nil {
			return 0, fmt.Errorf("runner: interactive session inventory: instance %s: %w", inst.ID, err)
		}
		switch {
		case workflow.IsInteractive(node.Type):
			n++
		case node.Type == workflow.NodeTypeParallelGroup:
			run, err := v.groups.GetByParentNode(ctx, inst.ID, node.ID)
			if err != nil {
				return 0, fmt.Errorf("runner: interactive session inventory: instance %s: %w", inst.ID, err)
			}
			if run != nil && run.State == instance.GroupRunResolvingConflicts && run.Gather != nil && run.Gather.ActiveSession != nil {
				n++
			}
		}
	}
	return n, nil
}
