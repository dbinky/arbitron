package engine_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readGetRepo is an InstanceRepository whose Get returns a fixed instance (or a
// fixed error). All mutators panic.
type readGetRepo struct {
	inst *instance.Instance
	err  error
}

func (r *readGetRepo) Get(_ context.Context, _ uuid.UUID) (*instance.Instance, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.inst, nil
}
func (r *readGetRepo) List(context.Context, instance.ListFilters) ([]instance.InstanceSummary, error) {
	panic("unused")
}
func (r *readGetRepo) Create(context.Context, *instance.Instance) error { panic("unused") }
func (r *readGetRepo) UpdateState(context.Context, uuid.UUID, instance.State) error {
	panic("unused")
}
func (r *readGetRepo) UpdateVariables(context.Context, uuid.UUID, map[string]any) error {
	panic("unused")
}
func (r *readGetRepo) UpdateCurrentNode(context.Context, uuid.UUID, string) error { panic("unused") }
func (r *readGetRepo) SetFailed(context.Context, uuid.UUID, string) error         { panic("unused") }
func (r *readGetRepo) SetCompleted(context.Context, uuid.UUID) error              { panic("unused") }
func (r *readGetRepo) ListRunning(context.Context) ([]instance.Instance, error)   { panic("unused") }
func (r *readGetRepo) UpdatePriority(context.Context, uuid.UUID, instance.Priority) error {
	panic("unused")
}
func (r *readGetRepo) SetWaiting(context.Context, uuid.UUID, *time.Time) error { panic("unused") }
func (r *readGetRepo) SetWebhookFields(context.Context, uuid.UUID, *string, *string) error {
	panic("unused")
}
func (r *readGetRepo) SetWebhookReceived(context.Context, uuid.UUID, time.Time) error {
	panic("unused")
}
func (r *readGetRepo) Stats(context.Context, string, time.Time, time.Time) (*instance.WorkflowStats, error) {
	panic("unused")
}

var _ instance.InstanceRepository = (*readGetRepo)(nil)

// countingNodeExecRepo records how many times its read methods were called.
type countingNodeExecRepo struct {
	listCalls int
	getCalls  int
}

func (r *countingNodeExecRepo) Create(context.Context, *instance.NodeExecution) error { return nil }
func (r *countingNodeExecRepo) Update(context.Context, *instance.NodeExecution) error { return nil }
func (r *countingNodeExecRepo) ListByInstance(_ context.Context, _ uuid.UUID) ([]instance.NodeExecution, error) {
	r.listCalls++
	return []instance.NodeExecution{{NodeID: "n1"}}, nil
}
func (r *countingNodeExecRepo) GetByInstanceAndNode(_ context.Context, _ uuid.UUID, nodeID string) (*instance.NodeExecution, error) {
	r.getCalls++
	return &instance.NodeExecution{NodeID: nodeID}, nil
}
func (r *countingNodeExecRepo) AggregateByInstances(_ context.Context, _ []uuid.UUID) (map[uuid.UUID]instance.NodeExecutionAggregate, error) {
	return map[uuid.UUID]instance.NodeExecutionAggregate{}, nil
}

var _ instance.NodeExecutionRepository = (*countingNodeExecRepo)(nil)

// countingSnapshotRepo records how many times ListByInstance was called.
type countingSnapshotRepo struct {
	listCalls int
}

func (r *countingSnapshotRepo) Create(context.Context, *instance.VariableSnapshot) error { return nil }
func (r *countingSnapshotRepo) ListByInstance(_ context.Context, _ uuid.UUID) ([]instance.VariableSnapshot, error) {
	r.listCalls++
	return []instance.VariableSnapshot{{NodeID: "n1"}}, nil
}

var _ instance.VariableSnapshotRepository = (*countingSnapshotRepo)(nil)

func newReadEngine(inst *instance.Instance, getErr error) (*engine.Engine, *countingNodeExecRepo, *countingSnapshotRepo) {
	instRepo := &readGetRepo{inst: inst, err: getErr}
	nodeRepo := &countingNodeExecRepo{}
	snapRepo := &countingSnapshotRepo{}
	eng := engine.NewEngine(
		newScopeAwareRepo(), instRepo, nodeRepo, snapRepo, newMockUserRepo(),
		map[workflow.NodeType]engine.NodeExecutor{}, newMockEventBus(), newMockQueueManager(), &mockQueueRepo{},
		"",
	)
	return eng, nodeRepo, snapRepo
}

func ownedInstance(owner uuid.UUID) *instance.Instance {
	return &instance.Instance{
		ID:        uuid.New(),
		OwnerID:   owner,
		State:     instance.StateRunning,
		Variables: map[string]any{"k": "v"},
	}
}

// --- Happy path: owner reads everything ---

func TestRead_Owner_AllMethods(t *testing.T) {
	owner := uuid.New()
	inst := ownedInstance(owner)
	eng, nodeRepo, snapRepo := newReadEngine(inst, nil)
	ctx := userCtx(owner)

	got, err := eng.GetInstance(ctx, inst.ID)
	require.NoError(t, err)
	assert.Equal(t, inst.ID, got.ID)

	tr, err := eng.GetTrace(ctx, inst.ID)
	require.NoError(t, err)
	assert.Len(t, tr, 1)

	ne, err := eng.GetNodeTrace(ctx, inst.ID, "n1")
	require.NoError(t, err)
	assert.Equal(t, "n1", ne.NodeID)

	vars, err := eng.GetVariables(ctx, inst.ID)
	require.NoError(t, err)
	assert.Equal(t, "v", vars["k"])

	hist, err := eng.GetVariableHistory(ctx, inst.ID)
	require.NoError(t, err)
	assert.Len(t, hist, 1)

	assert.Equal(t, 1, nodeRepo.listCalls)
	assert.Equal(t, 1, nodeRepo.getCalls)
	assert.Equal(t, 1, snapRepo.listCalls)
}

// --- Validation: admin (different UserID) reads everything ---

func TestRead_Admin_AllMethods(t *testing.T) {
	owner := uuid.New()
	inst := ownedInstance(owner)
	eng, _, _ := newReadEngine(inst, nil)
	ctx := adminCtx(uuid.New()) // not the owner

	_, err := eng.GetInstance(ctx, inst.ID)
	require.NoError(t, err)
	_, err = eng.GetTrace(ctx, inst.ID)
	require.NoError(t, err)
	_, err = eng.GetNodeTrace(ctx, inst.ID, "n1")
	require.NoError(t, err)
	_, err = eng.GetVariables(ctx, inst.ID)
	require.NoError(t, err)
	_, err = eng.GetVariableHistory(ctx, inst.ID)
	require.NoError(t, err)
}

// --- Error/failure: infra error propagates, not masked as ErrNotFound ---

func TestRead_InfraError_Propagates(t *testing.T) {
	sentinel := errors.New("db connection reset")
	eng, _, _ := newReadEngine(nil, sentinel)
	ctx := userCtx(uuid.New())

	_, err := eng.GetInstance(ctx, uuid.New())
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
	assert.NotErrorIs(t, err, instance.ErrNotFound, "infra error must not be masked as not-found")
}

// --- Boundary: instance absent -> ErrNotFound, sub-resources never read ---

func TestRead_Absent_NotFoundNoSubReads(t *testing.T) {
	eng, nodeRepo, snapRepo := newReadEngine(nil, instance.ErrNotFound)
	ctx := userCtx(uuid.New())

	_, err := eng.GetTrace(ctx, uuid.New())
	assert.ErrorIs(t, err, instance.ErrNotFound)
	_, err = eng.GetNodeTrace(ctx, uuid.New(), "n1")
	assert.ErrorIs(t, err, instance.ErrNotFound)
	_, err = eng.GetVariables(ctx, uuid.New())
	assert.ErrorIs(t, err, instance.ErrNotFound)
	_, err = eng.GetVariableHistory(ctx, uuid.New())
	assert.ErrorIs(t, err, instance.ErrNotFound)

	assert.Equal(t, 0, nodeRepo.listCalls)
	assert.Equal(t, 0, nodeRepo.getCalls)
	assert.Equal(t, 0, snapRepo.listCalls)
}

// --- Isolation/security: non-owner non-admin -> ErrNotFound, no sub-reads ---

func TestRead_NonOwner_NotFoundNoSubReads(t *testing.T) {
	owner := uuid.New()
	inst := ownedInstance(owner)
	eng, nodeRepo, snapRepo := newReadEngine(inst, nil)
	ctx := userCtx(uuid.New()) // a different non-admin user

	_, err := eng.GetInstance(ctx, inst.ID)
	assert.ErrorIs(t, err, instance.ErrNotFound, "non-owner gets not-found (existence withheld)")

	_, err = eng.GetTrace(ctx, inst.ID)
	assert.ErrorIs(t, err, instance.ErrNotFound)
	_, err = eng.GetNodeTrace(ctx, inst.ID, "n1")
	assert.ErrorIs(t, err, instance.ErrNotFound)
	_, err = eng.GetVariables(ctx, inst.ID)
	assert.ErrorIs(t, err, instance.ErrNotFound)
	_, err = eng.GetVariableHistory(ctx, inst.ID)
	assert.ErrorIs(t, err, instance.ErrNotFound)

	assert.Equal(t, 0, nodeRepo.listCalls, "trace never read for a non-owner")
	assert.Equal(t, 0, nodeRepo.getCalls)
	assert.Equal(t, 0, snapRepo.listCalls, "variable history never read for a non-owner")
}
