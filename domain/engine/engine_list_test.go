package engine_test

import (
	"context"
	"errors"
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

// recordingListRepo is an instance.InstanceRepository whose List records the
// ListFilters it was handed and returns a fixed summary set (or a fixed error).
// Only List is exercised; every other method panics so a test that calls them is
// obviously wrong.
type recordingListRepo struct {
	gotFilters instance.ListFilters
	calls      int
	summaries  []instance.InstanceSummary
	err        error
}

func (r *recordingListRepo) List(_ context.Context, filters instance.ListFilters) ([]instance.InstanceSummary, error) {
	r.calls++
	r.gotFilters = filters
	if r.err != nil {
		return nil, r.err
	}
	return r.summaries, nil
}

func (r *recordingListRepo) Create(context.Context, *instance.Instance) error { panic("unused") }
func (r *recordingListRepo) Get(context.Context, uuid.UUID) (*instance.Instance, error) {
	panic("unused")
}
func (r *recordingListRepo) UpdateState(context.Context, uuid.UUID, instance.State) error {
	panic("unused")
}
func (r *recordingListRepo) UpdateVariables(context.Context, uuid.UUID, map[string]any) error {
	panic("unused")
}
func (r *recordingListRepo) UpdateCurrentNode(context.Context, uuid.UUID, string) error {
	panic("unused")
}
func (r *recordingListRepo) SetFailed(context.Context, uuid.UUID, string) error { panic("unused") }
func (r *recordingListRepo) SetCompleted(context.Context, uuid.UUID) error      { panic("unused") }
func (r *recordingListRepo) ListRunning(context.Context) ([]instance.Instance, error) {
	panic("unused")
}
func (r *recordingListRepo) UpdatePriority(context.Context, uuid.UUID, instance.Priority) error {
	panic("unused")
}
func (r *recordingListRepo) SetWaiting(context.Context, uuid.UUID, *time.Time) error {
	panic("unused")
}
func (r *recordingListRepo) SetWebhookFields(context.Context, uuid.UUID, *string, *string) error {
	panic("unused")
}
func (r *recordingListRepo) SetWebhookReceived(context.Context, uuid.UUID, time.Time) error {
	panic("unused")
}
func (r *recordingListRepo) Stats(context.Context, string, time.Time, time.Time) (*instance.WorkflowStats, error) {
	panic("unused")
}

var _ instance.InstanceRepository = (*recordingListRepo)(nil)

func newListEngine(repo instance.InstanceRepository) *engine.Engine {
	executors := map[workflow.NodeType]engine.NodeExecutor{}
	return engine.NewEngine(
		newScopeAwareRepo(), repo, newMockNodeExecutionRepo(), newMockSnapshotRepo(),
		newMockUserRepo(), executors, newMockEventBus(), newMockQueueManager(), &mockQueueRepo{},
		"",
	)
}

func adminCtx(u uuid.UUID) context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{UserID: u, Username: "admin", Role: identity.RoleAdmin})
}

func userCtx(u uuid.UUID) context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{UserID: u, Username: "user", Role: identity.RoleUser})
}

// bgAdmin returns a background context carrying an admin identity. Intervention
// tests that pre-date per-user scoping use it so the owner-or-admin gate admits
// the action regardless of the instance owner.
func bgAdmin() context.Context {
	return adminCtx(uuid.New())
}

// --- Happy path: admin -> no owner constraint, summaries pass through ---

func TestListInstances_Admin_NoOwnerFilter(t *testing.T) {
	fixed := []instance.InstanceSummary{{ID: uuid.New(), WorkflowName: "wf"}}
	repo := &recordingListRepo{summaries: fixed}
	eng := newListEngine(repo)

	got, err := eng.ListInstances(adminCtx(uuid.New()), instance.ListFilters{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, fixed, got, "summaries pass through unchanged")
	assert.Nil(t, repo.gotFilters.OwnerID, "admin is unconstrained: owner filter nil")
	assert.Equal(t, 10, repo.gotFilters.Limit, "other filters preserved")
}

// --- Validation: caller-supplied OwnerID is overwritten for a non-admin ---

func TestListInstances_NonAdmin_OverwritesSuppliedOwner(t *testing.T) {
	repo := &recordingListRepo{}
	eng := newListEngine(repo)
	caller := uuid.New()
	other := uuid.New()

	_, err := eng.ListInstances(userCtx(caller), instance.ListFilters{OwnerID: &other})
	require.NoError(t, err)
	require.NotNil(t, repo.gotFilters.OwnerID)
	assert.Equal(t, caller, *repo.gotFilters.OwnerID, "service overrides any caller-supplied owner with the caller's own UserID")
	assert.NotEqual(t, other, *repo.gotFilters.OwnerID, "scope cannot be widened to another user")
}

// --- Error/failure: repo error propagates unchanged ---

func TestListInstances_RepoError_Propagates(t *testing.T) {
	sentinel := errors.New("boom")
	repo := &recordingListRepo{err: sentinel}
	eng := newListEngine(repo)

	_, err := eng.ListInstances(userCtx(uuid.New()), instance.ListFilters{})
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
}

// --- Boundary: no identity -> ErrNotFound, repo never called ---

func TestListInstances_NoIdentity_NotFoundNoRepoCall(t *testing.T) {
	repo := &recordingListRepo{}
	eng := newListEngine(repo)

	_, err := eng.ListInstances(context.Background(), instance.ListFilters{})
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrNotFound)
	assert.Equal(t, 0, repo.calls, "repo must not be queried without an identity")
}

// --- Isolation/security: each non-admin sees only their own owner filter ---

func TestListInstances_NonAdmin_PerCallerOwnerFilter(t *testing.T) {
	repo := &recordingListRepo{}
	eng := newListEngine(repo)

	a := uuid.New()
	_, err := eng.ListInstances(userCtx(a), instance.ListFilters{})
	require.NoError(t, err)
	require.NotNil(t, repo.gotFilters.OwnerID)
	assert.Equal(t, a, *repo.gotFilters.OwnerID)

	b := uuid.New()
	_, err = eng.ListInstances(userCtx(b), instance.ListFilters{})
	require.NoError(t, err)
	require.NotNil(t, repo.gotFilters.OwnerID)
	assert.Equal(t, b, *repo.gotFilters.OwnerID, "a different non-admin gets a different owner filter")
}
