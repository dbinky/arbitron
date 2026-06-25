package engine_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// enricherTestDeps bundles the real engine-test mocks the enricher is driven
// with. They are constructed fresh per test so seeding is isolated.
type enricherTestDeps struct {
	instRepo     *enricherInstanceRepo
	nodeExecRepo *mockNodeExecutionRepo
	userRepo     *enricherUserRepo
}

func newEnricherDeps() *enricherTestDeps {
	return &enricherTestDeps{
		instRepo:     newEnricherInstanceRepo(),
		nodeExecRepo: newMockNodeExecutionRepo(),
		userRepo:     newEnricherUserRepo(),
	}
}

func (d *enricherTestDeps) enricher() *engine.InstanceEnricher {
	return engine.NewInstanceEnricher(d.instRepo, d.nodeExecRepo, d.userRepo, nil, "")
}

func TestInstanceEnricher_HappyPath_AllFieldsPresent(t *testing.T) {
	d := newEnricherDeps()
	owner := uuid.New()
	d.userRepo.users[owner] = instance.User{ID: owner, Username: "alice"}

	nodeID := "draft-node"
	wf := workflow.Workflow{
		Name:   "feature-pipeline",
		Inputs: []workflow.InputField{{Name: "title", Type: "string", Required: true}},
		Nodes:  []workflow.Node{{ID: nodeID, Label: "Generate draft", Config: workflow.NodeConfig{Backend: "claude-code"}}},
	}
	inst := &instance.Instance{
		ID:               uuid.New(),
		OwnerID:          owner,
		WorkflowName:     "feature-pipeline",
		WorkflowSnapshot: snapshotMap(t, wf),
		State:            instance.StateRunning,
		Priority:         instance.PriorityNormal,
		CurrentNodeID:    &nodeID,
		Variables:        map[string]any{"title": "Refactor checkout"},
		CreatedAt:        time.Now(),
	}
	d.instRepo.instances[inst.ID] = inst

	model := "gpt-4o"
	d.nodeExecRepo.executions = append(d.nodeExecRepo.executions, &instance.NodeExecution{
		ID: uuid.New(), InstanceID: inst.ID, NodeID: nodeID, Attempt: 2, LLMModel: &model, StartedAt: time.Now(),
	})

	summaries := []instance.InstanceSummary{{ID: inst.ID, WorkflowName: inst.WorkflowName, OwnerID: owner, State: instance.StateRunning, CurrentNodeID: &nodeID, CreatedAt: inst.CreatedAt}}

	enr := d.enricher()
	got, err := enr.Enrich(context.Background(), summaries, identity.Identity{UserID: owner, Username: "alice", Role: identity.RoleUser})
	require.NoError(t, err)
	require.Len(t, got, 1)

	row := got[0]
	assert.Equal(t, inst.ID, row.ID)
	assert.Equal(t, "Refactor checkout — feature-pipeline", row.RunName)
	assert.Equal(t, "alice", row.Submitter.Name)
	assert.Equal(t, "Generate draft", row.FriendlyStep, "label from the current node")
	assert.Equal(t, "gpt-4o", row.Model)
	assert.Equal(t, "claude-code", row.Backend)
	assert.Equal(t, 2, row.RetryCount)
	assert.Equal(t, 0, row.QueuePosition, "no queue-position provider wired -> 0 (non-queued)")
}

// snapshotMap serializes a workflow definition into the map[string]any shape
// stored on Instance.WorkflowSnapshot, so tests can seed the snapshot the
// enricher parses with engine.FindNodeByID.
func snapshotMap(t *testing.T, wf workflow.Workflow) map[string]any {
	t.Helper()
	b, err := json.Marshal(wf)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	return m
}

// enricherInstanceRepo is a minimal instance.InstanceRepository for enricher
// tests. Only Get is exercised (the enricher loads full instances for
// snapshots); every other method panics so a test that calls them is wrong.
type enricherInstanceRepo struct {
	instances map[uuid.UUID]*instance.Instance
}

func newEnricherInstanceRepo() *enricherInstanceRepo {
	return &enricherInstanceRepo{instances: make(map[uuid.UUID]*instance.Instance)}
}

func (r *enricherInstanceRepo) Get(_ context.Context, id uuid.UUID) (*instance.Instance, error) {
	inst, ok := r.instances[id]
	if !ok {
		return nil, instance.ErrNotFound
	}
	return inst, nil
}
func (r *enricherInstanceRepo) Create(context.Context, *instance.Instance) error { panic("unused") }
func (r *enricherInstanceRepo) List(context.Context, instance.ListFilters) ([]instance.InstanceSummary, error) {
	panic("unused")
}
func (r *enricherInstanceRepo) UpdateState(context.Context, uuid.UUID, instance.State) error {
	panic("unused")
}
func (r *enricherInstanceRepo) UpdateVariables(context.Context, uuid.UUID, map[string]any) error {
	panic("unused")
}
func (r *enricherInstanceRepo) UpdateCurrentNode(context.Context, uuid.UUID, string) error {
	panic("unused")
}
func (r *enricherInstanceRepo) SetFailed(context.Context, uuid.UUID, string) error { panic("unused") }
func (r *enricherInstanceRepo) SetCompleted(context.Context, uuid.UUID) error      { panic("unused") }
func (r *enricherInstanceRepo) ListRunning(context.Context) ([]instance.Instance, error) {
	panic("unused")
}
func (r *enricherInstanceRepo) UpdatePriority(context.Context, uuid.UUID, instance.Priority) error {
	panic("unused")
}
func (r *enricherInstanceRepo) SetWaiting(context.Context, uuid.UUID, *time.Time) error {
	panic("unused")
}
func (r *enricherInstanceRepo) SetWebhookFields(context.Context, uuid.UUID, *string, *string) error {
	panic("unused")
}
func (r *enricherInstanceRepo) SetWebhookReceived(context.Context, uuid.UUID, time.Time) error {
	panic("unused")
}
func (r *enricherInstanceRepo) Stats(context.Context, string, time.Time, time.Time) (*instance.WorkflowStats, error) {
	panic("unused")
}

var _ instance.InstanceRepository = (*enricherInstanceRepo)(nil)

// enricherUserRepo is a minimal instance.UserRepository for enricher tests.
// Only GetByID is exercised (the enricher batch-looks-up owners); the other
// methods satisfy the interface with zero values. users maps ownerID -> User.
type enricherUserRepo struct {
	users map[uuid.UUID]instance.User
}

func newEnricherUserRepo() *enricherUserRepo {
	return &enricherUserRepo{users: make(map[uuid.UUID]instance.User)}
}

func (r *enricherUserRepo) GetByID(_ context.Context, id uuid.UUID) (*instance.User, error) {
	u, ok := r.users[id]
	if !ok {
		return nil, instance.ErrNotFound
	}
	return &u, nil
}
func (r *enricherUserRepo) GetDefaultUser(context.Context) (uuid.UUID, error) { return uuid.Nil, nil }
func (r *enricherUserRepo) GetBySubject(context.Context, string) (*instance.User, error) {
	return nil, instance.ErrNotFound
}
func (r *enricherUserRepo) GetByEmail(context.Context, string) (*instance.User, error) {
	return nil, instance.ErrNotFound
}
func (r *enricherUserRepo) Create(context.Context, *instance.User) error { return nil }
func (r *enricherUserRepo) UpdateUsername(context.Context, uuid.UUID, string) error {
	return nil
}
func (r *enricherUserRepo) List(context.Context) ([]instance.User, error)       { return nil, nil }
func (r *enricherUserRepo) UpdateRole(context.Context, uuid.UUID, string) error { return nil }
func (r *enricherUserRepo) CountAdmins(context.Context) (int, error)            { return 0, nil }
func (r *enricherUserRepo) ListDepartments(context.Context, uuid.UUID) ([]string, error) {
	return nil, nil
}
func (r *enricherUserRepo) ReplaceDepartments(context.Context, uuid.UUID, []string) error {
	return nil
}

var _ instance.UserRepository = (*enricherUserRepo)(nil)

func TestInstanceEnricher_UserLookupFails_SubmitterEmpty(t *testing.T) {
	d := newEnricherDeps()
	owner := uuid.New()
	// No user seeded for owner -> userRepo returns ErrNotFound
	nodeID := "n1"
	wf := workflow.Workflow{Name: "wf", Nodes: []workflow.Node{{ID: nodeID, Label: "step"}}}
	inst := &instance.Instance{ID: uuid.New(), OwnerID: owner, WorkflowName: "wf", WorkflowSnapshot: snapshotMap(t, wf), State: instance.StateRunning, CurrentNodeID: &nodeID, CreatedAt: time.Now()}
	d.instRepo.instances[inst.ID] = inst
	summaries := []instance.InstanceSummary{{ID: inst.ID, OwnerID: owner, State: instance.StateRunning, CurrentNodeID: &nodeID, CreatedAt: inst.CreatedAt}}

	got, err := d.enricher().Enrich(context.Background(), summaries, identity.Identity{UserID: owner, Username: "self", Role: identity.RoleUser})
	require.NoError(t, err, "user lookup failure degrades the field, not the list")
	require.Len(t, got, 1)
	assert.Equal(t, "", got[0].Submitter.Name)
	assert.Equal(t, "step", got[0].FriendlyStep, "other fields still resolved")
}

func TestInstanceEnricher_SnapshotMissingNode_FriendlyStepEmpty(t *testing.T) {
	d := newEnricherDeps()
	owner := uuid.New()
	d.userRepo.users[owner] = instance.User{ID: owner, Username: "alice"}
	wf := workflow.Workflow{Name: "wf", Nodes: []workflow.Node{{ID: "real-node", Label: "Real"}}}
	inst := &instance.Instance{ID: uuid.New(), OwnerID: owner, WorkflowName: "wf", WorkflowSnapshot: snapshotMap(t, wf), State: instance.StateRunning, CurrentNodeID: strPtr("missing-node"), CreatedAt: time.Now()}
	d.instRepo.instances[inst.ID] = inst
	summaries := []instance.InstanceSummary{{ID: inst.ID, OwnerID: owner, State: instance.StateRunning, CurrentNodeID: strPtr("missing-node"), CreatedAt: inst.CreatedAt}}

	got, err := d.enricher().Enrich(context.Background(), summaries, identity.Identity{UserID: owner, Username: "alice", Role: identity.RoleUser})
	require.NoError(t, err)
	assert.Equal(t, "", got[0].FriendlyStep, "node not in snapshot -> empty, row still returned")
	assert.NotEmpty(t, got[0].RunName, "run name still derived")
}

func TestInstanceEnricher_QueuedInstance_NoExecutions(t *testing.T) {
	d := newEnricherDeps()
	owner := uuid.New()
	d.userRepo.users[owner] = instance.User{ID: owner, Username: "alice"}
	wf := workflow.Workflow{Name: "wf", Inputs: []workflow.InputField{{Name: "title", Type: "string"}}}
	inst := &instance.Instance{ID: uuid.New(), OwnerID: owner, WorkflowName: "wf", WorkflowSnapshot: snapshotMap(t, wf), State: instance.StateQueued, Variables: map[string]any{"title": "T"}, CreatedAt: time.Now()}
	d.instRepo.instances[inst.ID] = inst
	summaries := []instance.InstanceSummary{{ID: inst.ID, OwnerID: owner, State: instance.StateQueued, CreatedAt: inst.CreatedAt}}

	got, err := d.enricher().Enrich(context.Background(), summaries, identity.Identity{UserID: owner, Username: "alice", Role: identity.RoleUser})
	require.NoError(t, err)
	assert.Equal(t, "", got[0].Model, "queued: no model")
	assert.Equal(t, 0, got[0].RetryCount, "queued: 0 retries")
	assert.Equal(t, "", got[0].FriendlyStep, "queued: no current node")
	assert.Equal(t, "T — wf", got[0].RunName)
}

func TestInstanceEnricher_EmptyPage(t *testing.T) {
	d := newEnricherDeps()
	got, err := d.enricher().Enrich(context.Background(), nil, identity.Identity{UserID: uuid.New(), Role: identity.RoleUser})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestInstanceEnricher_DuplicateOwnerIDs_DedupedToOneLookup(t *testing.T) {
	d := newEnricherDeps()
	owner := uuid.New()
	d.userRepo.users[owner] = instance.User{ID: owner, Username: "alice"}
	wf := workflow.Workflow{Name: "wf"}
	inst1 := &instance.Instance{ID: uuid.New(), OwnerID: owner, WorkflowName: "wf", WorkflowSnapshot: snapshotMap(t, wf), State: instance.StateRunning, CreatedAt: time.Now()}
	inst2 := &instance.Instance{ID: uuid.New(), OwnerID: owner, WorkflowName: "wf", WorkflowSnapshot: snapshotMap(t, wf), State: instance.StateRunning, CreatedAt: time.Now()}
	d.instRepo.instances[inst1.ID] = inst1
	d.instRepo.instances[inst2.ID] = inst2

	summaries := []instance.InstanceSummary{
		{ID: inst1.ID, OwnerID: owner, State: instance.StateRunning, CreatedAt: inst1.CreatedAt},
		{ID: inst2.ID, OwnerID: owner, State: instance.StateRunning, CreatedAt: inst2.CreatedAt},
	}
	got, err := d.enricher().Enrich(context.Background(), summaries, identity.Identity{UserID: owner, Username: "alice", Role: identity.RoleUser})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "alice", got[0].Submitter.Name)
	assert.Equal(t, "alice", got[1].Submitter.Name)
}

func TestInstanceEnricher_BackendUnset_EmptyBackend(t *testing.T) {
	d := newEnricherDeps()
	owner := uuid.New()
	d.userRepo.users[owner] = instance.User{ID: owner, Username: "alice"}
	nodeID := "n1"
	wf := workflow.Workflow{Name: "wf", Nodes: []workflow.Node{{ID: nodeID, Label: "step", Config: workflow.NodeConfig{Backend: workflow.BackendUnset}}}}
	inst := &instance.Instance{ID: uuid.New(), OwnerID: owner, WorkflowName: "wf", WorkflowSnapshot: snapshotMap(t, wf), State: instance.StateRunning, CurrentNodeID: &nodeID, CreatedAt: time.Now()}
	d.instRepo.instances[inst.ID] = inst
	summaries := []instance.InstanceSummary{{ID: inst.ID, OwnerID: owner, State: instance.StateRunning, CurrentNodeID: &nodeID, CreatedAt: inst.CreatedAt}}

	// No defaultAgentBackend set -> empty cell (no fallback).
	got, err := d.enricher().Enrich(context.Background(), summaries, identity.Identity{UserID: owner, Username: "alice", Role: identity.RoleUser})
	require.NoError(t, err)
	assert.Equal(t, "", got[0].Backend, "BackendUnset with no default -> empty cell")
}

func TestInstanceEnricher_BackendUnset_ResolvesToDefault(t *testing.T) {
	d := newEnricherDeps()
	owner := uuid.New()
	d.userRepo.users[owner] = instance.User{ID: owner, Username: "alice"}
	nodeID := "n1"
	wf := workflow.Workflow{Name: "wf", Nodes: []workflow.Node{{ID: nodeID, Label: "step", Config: workflow.NodeConfig{Backend: workflow.BackendUnset}}}}
	inst := &instance.Instance{ID: uuid.New(), OwnerID: owner, WorkflowName: "wf", WorkflowSnapshot: snapshotMap(t, wf), State: instance.StateRunning, CurrentNodeID: &nodeID, CreatedAt: time.Now()}
	d.instRepo.instances[inst.ID] = inst
	summaries := []instance.InstanceSummary{{ID: inst.ID, OwnerID: owner, State: instance.StateRunning, CurrentNodeID: &nodeID, CreatedAt: inst.CreatedAt}}

	enr := engine.NewInstanceEnricher(d.instRepo, d.nodeExecRepo, d.userRepo, nil, "claude-code")
	got, err := enr.Enrich(context.Background(), summaries, identity.Identity{UserID: owner, Username: "alice", Role: identity.RoleUser})
	require.NoError(t, err)
	assert.Equal(t, "claude-code", got[0].Backend, "BackendUnset with defaultAgentBackend -> resolves to default")
}

func TestInstanceEnricher_FailedInstance_UsesFailedNodeID(t *testing.T) {
	d := newEnricherDeps()
	owner := uuid.New()
	d.userRepo.users[owner] = instance.User{ID: owner, Username: "alice"}
	failedNode := "fail-node"
	wf := workflow.Workflow{Name: "wf", Nodes: []workflow.Node{{ID: failedNode, Label: "Crashed step"}, {ID: "other", Label: "Other"}}}
	inst := &instance.Instance{ID: uuid.New(), OwnerID: owner, WorkflowName: "wf", WorkflowSnapshot: snapshotMap(t, wf), State: instance.StateFailed, FailedNodeID: &failedNode, CreatedAt: time.Now()}
	d.instRepo.instances[inst.ID] = inst
	summaries := []instance.InstanceSummary{{ID: inst.ID, OwnerID: owner, State: instance.StateFailed, CreatedAt: inst.CreatedAt}}

	got, err := d.enricher().Enrich(context.Background(), summaries, identity.Identity{UserID: owner, Username: "alice", Role: identity.RoleUser})
	require.NoError(t, err)
	assert.Equal(t, "Crashed step", got[0].FriendlyStep, "failed -> label from FailedNodeID")
}

// Security: the account identity must be the EMAIL only. The OIDC subject is an
// opaque IdP identifier and must never be disclosed as a fallback when the email
// is absent (it would leak the IdP subject to other viewers).
func TestInstanceEnricher_AccountIdentity_NoSubjectFallback(t *testing.T) {
	d := newEnricherDeps()
	owner := uuid.New()
	subject := "oidc|opaque-subject-123"
	d.userRepo.users[owner] = instance.User{ID: owner, Username: "alice", Subject: &subject} // email nil
	wf := workflow.Workflow{Name: "wf"}
	inst := &instance.Instance{ID: uuid.New(), OwnerID: owner, WorkflowName: "wf", WorkflowSnapshot: snapshotMap(t, wf), State: instance.StateRunning, CreatedAt: time.Now()}
	d.instRepo.instances[inst.ID] = inst
	summaries := []instance.InstanceSummary{{ID: inst.ID, OwnerID: owner, State: instance.StateRunning, CreatedAt: inst.CreatedAt}}

	got, err := d.enricher().Enrich(context.Background(), summaries, identity.Identity{UserID: owner, Username: "alice", Role: identity.RoleUser})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "", got[0].Submitter.AccountIdentity, "subject must NOT be exposed as an account-identity fallback")
}

func strPtr(s string) *string { return &s }
