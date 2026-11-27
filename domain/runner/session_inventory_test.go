package runner_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type invWaiting struct {
	insts []instance.Instance
	err   error
}

func (w invWaiting) ListWaitingAtNode(context.Context) ([]instance.Instance, error) {
	return w.insts, w.err
}

type invGroups struct {
	runs map[uuid.UUID]map[string]*instance.GroupRun
	err  error
}

func (g invGroups) GetByParentNode(_ context.Context, parent uuid.UUID, node string) (*instance.GroupRun, error) {
	return g.runs[parent][node], g.err
}

func invSnapshot(nodeID, nodeType string) map[string]any {
	return map[string]any{"name": "wf", "nodes": []any{map[string]any{"id": nodeID, "type": nodeType, "config": map[string]any{}}}}
}

func invWaitingAt(nodeID, nodeType string) instance.Instance {
	n := nodeID
	return instance.Instance{ID: uuid.New(), State: instance.StateWaiting, CurrentNodeID: &n, WorkflowSnapshot: invSnapshot(nodeID, nodeType)}
}

func TestInteractiveSessionInventory_CountsOpenBuiltInSessions(t *testing.T) {
	agent, llm := invWaitingAt("chat", "interactive_agent"), invWaitingAt("ask", "interactive_llm")
	timer := invWaitingAt("pause", "timer_delay")
	resolving, idle := invWaitingAt("g1", "parallel_group"), invWaitingAt("g2", "parallel_group")
	groups := invGroups{runs: map[uuid.UUID]map[string]*instance.GroupRun{
		resolving.ID: {"g1": {State: instance.GroupRunResolvingConflicts, Gather: &instance.GatherReport{ActiveSession: &instance.ResolutionSessionRef{}}}},
		idle.ID:      {"g2": {State: instance.GroupRunRunning}},
	}}
	inv, err := runner.NewInteractiveSessionInventory(invWaiting{insts: []instance.Instance{agent, llm, timer, resolving, idle}}, groups)
	require.NoError(t, err)

	n, err := inv.CountPinned(context.Background(), runner.BuiltInRunnerID)
	require.NoError(t, err)
	assert.Equal(t, 3, n)

	n, err = inv.CountPinned(context.Background(), "mac-1")
	require.NoError(t, err)
	assert.Zero(t, n, "Phase 3 runs every interactive session on the built-in runner")
}

func TestInteractiveSessionInventory_ReadFailuresFailClosed(t *testing.T) {
	inv, err := runner.NewInteractiveSessionInventory(invWaiting{err: errors.New("database is locked")}, invGroups{})
	require.NoError(t, err)
	_, err = inv.CountPinned(context.Background(), runner.BuiltInRunnerID)
	require.ErrorContains(t, err, "database is locked")

	bad := invWaitingAt("gone", "interactive_agent")
	bad.WorkflowSnapshot = invSnapshot("other", "interactive_agent")
	inv, err = runner.NewInteractiveSessionInventory(invWaiting{insts: []instance.Instance{bad}}, invGroups{})
	require.NoError(t, err)
	_, err = inv.CountPinned(context.Background(), runner.BuiltInRunnerID)
	require.Error(t, err, "a waiting node missing from its snapshot is not counted as zero")

	grp := invWaitingAt("g1", "parallel_group")
	inv, err = runner.NewInteractiveSessionInventory(invWaiting{insts: []instance.Instance{grp}}, invGroups{err: errors.New("disk I/O error")})
	require.NoError(t, err)
	_, err = inv.CountPinned(context.Background(), runner.BuiltInRunnerID)
	require.ErrorContains(t, err, "disk I/O error")
}

func TestNewInteractiveSessionInventory_RequiresBothReads(t *testing.T) {
	_, err := runner.NewInteractiveSessionInventory(nil, invGroups{})
	require.Error(t, err)
	_, err = runner.NewInteractiveSessionInventory(invWaiting{}, nil)
	require.Error(t, err)
}
