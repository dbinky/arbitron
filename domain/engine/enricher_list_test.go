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

// enricherListRepo is a recordingListRepo variant that also stores full
// instances so the enricher's per-row Get (for snapshot/variables) resolves.
type enricherListRepo struct {
	summaries  []instance.InstanceSummary
	instances  map[uuid.UUID]*instance.Instance
	gotFilters instance.ListFilters
}

func (r *enricherListRepo) List(_ context.Context, filters instance.ListFilters) ([]instance.InstanceSummary, error) {
	r.gotFilters = filters
	return r.summaries, nil
}
func (r *enricherListRepo) Get(_ context.Context, id uuid.UUID) (*instance.Instance, error) {
	inst, ok := r.instances[id]
	if !ok {
		return nil, instance.ErrNotFound
	}
	return inst, nil
}
func (r *enricherListRepo) Create(context.Context, *instance.Instance) error { panic("unused") }
func (r *enricherListRepo) UpdateState(context.Context, uuid.UUID, instance.State) error {
	panic("unused")
}
func (r *enricherListRepo) UpdateVariables(context.Context, uuid.UUID, map[string]any) error {
	panic("unused")
}
func (r *enricherListRepo) UpdateCurrentNode(context.Context, uuid.UUID, string) error {
	panic("unused")
}
func (r *enricherListRepo) SetFailed(context.Context, uuid.UUID, string) error { panic("unused") }
func (r *enricherListRepo) SetCompleted(context.Context, uuid.UUID) error      { panic("unused") }
func (r *enricherListRepo) ListRunning(context.Context) ([]instance.Instance, error) {
	panic("unused")
}
func (r *enricherListRepo) UpdatePriority(context.Context, uuid.UUID, instance.Priority) error {
	panic("unused")
}
func (r *enricherListRepo) SetWaiting(context.Context, uuid.UUID, *time.Time) error { panic("unused") }
func (r *enricherListRepo) SetWebhookFields(context.Context, uuid.UUID, *string, *string) error {
	panic("unused")
}
func (r *enricherListRepo) SetWebhookReceived(context.Context, uuid.UUID, time.Time) error {
	panic("unused")
}
func (r *enricherListRepo) Stats(context.Context, string, time.Time, time.Time) (*instance.WorkflowStats, error) {
	panic("unused")
}

var _ instance.InstanceRepository = (*enricherListRepo)(nil)

func TestEngine_ListEnriched_AppliesAuthzThenEnriches(t *testing.T) {
	owner := uuid.New()
	nodeID := "n1"
	wf := workflow.Workflow{Name: "wf", Nodes: []workflow.Node{{ID: nodeID, Label: "Step"}}}
	inst := &instance.Instance{ID: uuid.New(), OwnerID: owner, WorkflowName: "wf", WorkflowSnapshot: snapshotMap(t, wf), State: instance.StateRunning, CurrentNodeID: &nodeID, Variables: map[string]any{"title": "T"}, CreatedAt: time.Now()}
	repo := &enricherListRepo{
		summaries: []instance.InstanceSummary{{ID: inst.ID, OwnerID: owner, WorkflowName: "wf", State: instance.StateRunning, CurrentNodeID: &nodeID, CreatedAt: inst.CreatedAt}},
		instances: map[uuid.UUID]*instance.Instance{inst.ID: inst},
	}
	nodeExecRepo := newMockNodeExecutionRepo()
	userRepo := newEnricherUserRepo()
	userRepo.users[owner] = instance.User{ID: owner, Username: "alice"}

	eng := engine.NewEngine(
		newScopeAwareRepo(), repo, nodeExecRepo, newMockSnapshotRepo(),
		userRepo, map[workflow.NodeType]engine.NodeExecutor{}, newMockEventBus(), newMockQueueManager(), &mockQueueRepo{},
	)
	eng.SetInstanceEnricher(engine.NewInstanceEnricher(repo, nodeExecRepo, userRepo, nil, ""))

	got, err := eng.ListEnriched(userCtx(owner), instance.ListFilters{Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "T — wf", got[0].RunName)
	assert.Equal(t, "alice", got[0].Submitter.Name)
	assert.Equal(t, "Step", got[0].FriendlyStep)
	require.NotNil(t, repo.gotFilters.OwnerID)
	assert.Equal(t, owner, *repo.gotFilters.OwnerID, "authz filter applied before enrichment")
}

func TestEngine_ListEnriched_NoIdentity_NotFound(t *testing.T) {
	repo := &enricherListRepo{}
	eng := engine.NewEngine(
		newScopeAwareRepo(), repo, newMockNodeExecutionRepo(), newMockSnapshotRepo(),
		newMockUserRepo(), map[workflow.NodeType]engine.NodeExecutor{}, newMockEventBus(), newMockQueueManager(), &mockQueueRepo{},
	)
	eng.SetInstanceEnricher(engine.NewInstanceEnricher(repo, newMockNodeExecutionRepo(), newEnricherUserRepo(), nil, ""))

	_, err := eng.ListEnriched(context.Background(), instance.ListFilters{})
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrNotFound)
}

func TestEngine_ListEnriched_RepoError_Propagates(t *testing.T) {
	sentinel := errors.New("boom")
	repo := &recordingListRepo{err: sentinel}
	eng := engine.NewEngine(
		newScopeAwareRepo(), repo, newMockNodeExecutionRepo(), newMockSnapshotRepo(),
		newMockUserRepo(), map[workflow.NodeType]engine.NodeExecutor{}, newMockEventBus(), newMockQueueManager(), &mockQueueRepo{},
	)
	eng.SetInstanceEnricher(engine.NewInstanceEnricher(repo, newMockNodeExecutionRepo(), newEnricherUserRepo(), nil, ""))

	_, err := eng.ListEnriched(userCtx(uuid.New()), instance.ListFilters{})
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
}
