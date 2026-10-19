package engine

import (
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/workflow"
)

// GroupSupport is the level of parallel_group execution this engine has been
// wired for. It is raised by the composition root only once the matching
// executor is registered: Phase 2 (P2-T14) sets GroupSupportResults, Phase 3
// (P3-T11) GroupSupportResultsAndFiles. The zero value refuses every
// group-bearing workflow.
type GroupSupport int

const (
	// GroupSupportNone refuses every workflow containing a parallel_group node
	// (Phase 1 default: authoring and validation only).
	GroupSupportNone GroupSupport = iota
	// GroupSupportResults executes gatherMode "results" groups and refuses
	// "results_and_files" groups (Phase 2).
	GroupSupportResults
	// GroupSupportResultsAndFiles executes every group (Phase 3).
	GroupSupportResultsAndFiles
)

// String renders the level with the gatherMode vocabulary.
func (l GroupSupport) String() string {
	switch l {
	case GroupSupportNone:
		return "none"
	case GroupSupportResults:
		return "results"
	case GroupSupportResultsAndFiles:
		return "results_and_files"
	}
	return fmt.Sprintf("GroupSupport(%d)", int(l))
}

// ErrGroupExecutionUnsupported is returned (wrapped, with the node and a fix)
// when a trigger asks this server to run a parallel_group it cannot execute.
// No instance is created.
var ErrGroupExecutionUnsupported = errors.New("parallel_group execution is not available on this server")

// SetGroupSupport sets the parallel_group execution level. Call it only from
// the composition root, after registering the executor that level requires.
func (e *Engine) SetGroupSupport(level GroupSupport) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.groupSupport = level
}

// GroupSupportLevel reports the configured parallel_group execution level.
func (e *Engine) GroupSupportLevel() GroupSupport {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.groupSupport
}

// checkGroupSupport returns nil when every parallel_group node in wf can run at
// level, else an error wrapping ErrGroupExecutionUnsupported for the FIRST
// offending node in definition order. It never downgrades a files-requesting
// group to results-only. An unknown level fails closed (treated as None).
func checkGroupSupport(wf *workflow.Workflow, level GroupSupport) error {
	if wf == nil || level == GroupSupportResultsAndFiles {
		return nil
	}
	for _, n := range wf.Nodes {
		if n.Type != workflow.NodeTypeParallelGroup {
			continue
		}
		mode := ""
		if n.Config.Group != nil {
			mode = n.Config.Group.GatherMode
		}
		if level == GroupSupportResults && mode == workflow.GroupGatherResults {
			continue
		}
		return groupUnsupportedError(wf.Name, n.ID, mode, level)
	}
	return nil
}

func groupUnsupportedError(workflowName, nodeID, mode string, level GroupSupport) error {
	shown := fmt.Sprintf("%q", mode)
	if mode == "" {
		shown = "<unset>"
	}
	switch {
	case level == GroupSupportResults && mode == workflow.GroupGatherResultsAndFiles:
		return fmt.Errorf("%w: workflow %q node %q uses gatherMode %s, but this server can only execute gatherMode %q; "+
			"file gathering is not available yet — set gatherMode to %q with workflow_group_upsert, or wait until file gathering is enabled",
			ErrGroupExecutionUnsupported, workflowName, nodeID, shown, workflow.GroupGatherResults, workflow.GroupGatherResults)
	case level == GroupSupportResults:
		return fmt.Errorf("%w: workflow %q node %q has gatherMode %s, which this server cannot execute; "+
			"run workflow_validate and fix the group with workflow_group_upsert",
			ErrGroupExecutionUnsupported, workflowName, nodeID, shown)
	default:
		return fmt.Errorf("%w: workflow %q node %q (gatherMode %s): this server cannot execute parallel_group yet. "+
			"The definition is saved and can still be authored and checked (workflow_group_get, workflow_validate), "+
			"but it cannot be triggered until parallel group execution is enabled on this server",
			ErrGroupExecutionUnsupported, workflowName, nodeID, shown)
	}
}
