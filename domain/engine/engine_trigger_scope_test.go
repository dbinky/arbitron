package engine_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scopeAwareRepo is a workflow.Repository keyed by (scope, name) so the
// Trigger scope-resolution tests can observe private-vs-shared visibility.
type scopeAwareRepo struct {
	mu    sync.Mutex
	store map[string]map[string]*workflow.Workflow
}

func newScopeAwareRepo() *scopeAwareRepo {
	return &scopeAwareRepo{store: map[string]map[string]*workflow.Workflow{}}
}

func saKey(s workflow.Scope) string {
	if s.Kind == workflow.ScopeShared {
		return "shared"
	}
	return "private:" + s.OwnerID.String()
}

func (r *scopeAwareRepo) bucket(s workflow.Scope) map[string]*workflow.Workflow {
	k := saKey(s)
	if r.store[k] == nil {
		r.store[k] = map[string]*workflow.Workflow{}
	}
	return r.store[k]
}

func (r *scopeAwareRepo) put(s workflow.Scope, wf *workflow.Workflow) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bucket(s)[wf.Name] = wf
}

func (r *scopeAwareRepo) Load(_ context.Context, s workflow.Scope, name string) (*workflow.Workflow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	wf, ok := r.bucket(s)[name]
	if !ok {
		return nil, workflow.ErrNotFound
	}
	return wf, nil
}

func (r *scopeAwareRepo) List(_ context.Context, _ workflow.Scope) ([]workflow.WorkflowSummary, error) {
	return nil, nil
}
func (r *scopeAwareRepo) SaveNodePositions(_ context.Context, _ workflow.Scope, _ string, _ map[string]workflow.NodePosition) error {
	return nil
}
func (r *scopeAwareRepo) Create(_ context.Context, s workflow.Scope, name string, wf *workflow.Workflow) error {
	r.put(s, wf)
	return nil
}
func (r *scopeAwareRepo) Update(_ context.Context, _ workflow.Scope, _ string, _ *workflow.Workflow) error {
	return nil
}
func (r *scopeAwareRepo) Delete(_ context.Context, _ workflow.Scope, _ string) error { return nil }
func (r *scopeAwareRepo) WriteFile(_ context.Context, _ workflow.Scope, _ string, _ string, _ []byte) error {
	return nil
}
func (r *scopeAwareRepo) DeleteFile(_ context.Context, _ workflow.Scope, _ string, _ string) error {
	return nil
}
func (r *scopeAwareRepo) ListFiles(_ context.Context, _ workflow.Scope, _ string) ([]string, error) {
	return nil, nil
}
func (r *scopeAwareRepo) Exists(_ context.Context, s workflow.Scope, name string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.bucket(s)[name]
	return ok, nil
}
func (r *scopeAwareRepo) CopyTree(_ context.Context, _ workflow.Scope, _ string, _ workflow.Scope, _ string, _ bool) error {
	return nil
}

// triggerHarness wires an engine whose Enqueue records the owner and leaves the
// instance queued (does not run it), so Trigger's snapshot/owner are inspectable.
type triggerHarness struct {
	repo          *scopeAwareRepo
	instances     *mockInstanceRepo
	users         *mockUserRepo
	engine        *engine.Engine
	enqueuedOwner uuid.UUID
}

func newTriggerHarness() *triggerHarness {
	repo := newScopeAwareRepo()
	instRepo := newMockInstanceRepo()
	nodeExecRepo := newMockNodeExecutionRepo()
	snapRepo := newMockSnapshotRepo()
	userRepo := newMockUserRepo()
	eventBus := newMockEventBus()
	qm := newMockQueueManager()

	executors := map[workflow.NodeType]engine.NodeExecutor{}
	eng := engine.NewEngine(repo, instRepo, nodeExecRepo, snapRepo, userRepo, executors, eventBus, qm, &mockQueueRepo{instRepo: instRepo}, "")

	h := &triggerHarness{repo: repo, instances: instRepo, users: userRepo, engine: eng}
	qm.mu.Lock()
	qm.enqueueFn = func(_ context.Context, _ uuid.UUID, ownerID uuid.UUID) (bool, error) {
		h.enqueuedOwner = ownerID
		return false, nil // stay queued; do not run
	}
	qm.mu.Unlock()
	return h
}

func sharedWf(name string) *workflow.Workflow {
	return &workflow.Workflow{Name: name, Description: "shared-def", Nodes: []workflow.Node{{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true}}}
}

func ctxAs(u uuid.UUID) context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{UserID: u, Username: "u", Role: identity.RoleUser})
}

// --- Happy path ---

func TestTrigger_SharedWorkflow_OwnedByTriggeringUser(t *testing.T) {
	h := newTriggerHarness()
	caller := uuid.New()
	h.repo.put(workflow.Shared(), sharedWf("pub-wf"))

	id, _, err := h.engine.Trigger(ctxAs(caller), "pub-wf", nil, "normal")
	require.NoError(t, err)

	inst, err := h.instances.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, caller, inst.OwnerID, "instance owned by the triggering user, not the publisher")
	assert.Equal(t, "shared-def", inst.WorkflowSnapshot["description"], "snapshot is the shared definition")
	assert.Equal(t, caller, h.enqueuedOwner, "enqueued under the triggering user's capacity")

	// No private copy was created.
	ok, err := h.repo.Exists(context.Background(), workflow.Private(caller), "pub-wf")
	require.NoError(t, err)
	assert.False(t, ok, "running a shared workflow must not create a private copy")
}

// --- Validation ---

func TestTrigger_MissingRequiredInput_StillErrors(t *testing.T) {
	h := newTriggerHarness()
	caller := uuid.New()
	wf := sharedWf("needs-input")
	wf.Inputs = []workflow.InputField{{Name: "x", Type: "string", Required: true}}
	h.repo.put(workflow.Shared(), wf)

	_, _, err := h.engine.Trigger(ctxAs(caller), "needs-input", map[string]any{}, "normal")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required input")
}

// --- Error/failure ---

func TestTrigger_UnknownName_NotFound(t *testing.T) {
	h := newTriggerHarness()
	caller := uuid.New()

	_, _, err := h.engine.Trigger(ctxAs(caller), "ghost", nil, "normal")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load workflow")
	assert.True(t, errors.Is(err, workflow.ErrNotFound))
}

// TestTrigger_DefaultUserUnavailable_FailsClosed verifies that a system trigger
// (no identity in context) fails closed when the default user cannot be resolved
// rather than proceeding with a nil owner. A nil owner would silently assign
// the instance to no real user, breaking all visibility scoping.
func TestTrigger_DefaultUserUnavailable_FailsClosed(t *testing.T) {
	h := newTriggerHarness()
	h.users.defaultUserErr = errors.New("db unavailable")
	h.repo.put(workflow.Shared(), sharedWf("sys-wf"))

	_, _, err := h.engine.Trigger(context.Background(), "sys-wf", nil, "normal")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to resolve triggering user")
	// No instance created.
	assert.Empty(t, h.instances.instances)
}

// --- Boundary ---

func TestTrigger_PrivateWinsOverShared(t *testing.T) {
	h := newTriggerHarness()
	caller := uuid.New()

	priv := sharedWf("dup")
	priv.Description = "private-def"
	h.repo.put(workflow.Private(caller), priv)
	h.repo.put(workflow.Shared(), sharedWf("dup")) // description "shared-def"

	id, _, err := h.engine.Trigger(ctxAs(caller), "dup", nil, "normal")
	require.NoError(t, err)

	inst, err := h.instances.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, "private-def", inst.WorkflowSnapshot["description"], "private definition wins")
}

func TestTrigger_SystemTrigger_UsesDefaultUserAndShared(t *testing.T) {
	h := newTriggerHarness()
	h.repo.put(workflow.Shared(), sharedWf("sys-wf"))

	// No identity in context: resolves the default user.
	id, _, err := h.engine.Trigger(context.Background(), "sys-wf", nil, "normal")
	require.NoError(t, err)

	inst, err := h.instances.Get(context.Background(), id)
	require.NoError(t, err)
	def, _ := h.users.GetDefaultUser(context.Background())
	assert.Equal(t, def, inst.OwnerID, "system trigger owned by the default user")
}

// TestTrigger_OwnerIsExactlyWhatListScopingKeysOn pins the FA-32 paired
// invariant end-to-end: a shared workflow triggered by user A creates an
// instance owned by A, and A's owner ID is exactly the key that the list
// scoping uses — so A sees the instance in their list and B does not.
func TestTrigger_OwnerIsExactlyWhatListScopingKeysOn(t *testing.T) {
	h := newTriggerHarness()
	a := uuid.New()
	b := uuid.New()
	h.repo.put(workflow.Shared(), sharedWf("pub-wf"))

	// A triggers the shared workflow.
	id, _, err := h.engine.Trigger(ctxAs(a), "pub-wf", nil, "normal")
	require.NoError(t, err)

	// A's ListInstances must contain the new instance.
	listA, err := h.engine.ListInstances(ctxAs(a), instance.ListFilters{})
	require.NoError(t, err)
	var foundA bool
	for _, s := range listA {
		if s.ID == id {
			foundA = true
		}
	}
	assert.True(t, foundA, "triggering user A must see their own instance in ListInstances")

	// B's ListInstances must NOT contain A's instance — ownership is disjoint.
	listB, err := h.engine.ListInstances(ctxAs(b), instance.ListFilters{})
	require.NoError(t, err)
	for _, s := range listB {
		assert.NotEqual(t, id, s.ID, "user B must not see user A's instance — owner scoping must key on the OwnerID set at trigger time")
	}
}

// --- Isolation/security ---

func TestTrigger_CannotSeeOtherUsersPrivate(t *testing.T) {
	h := newTriggerHarness()
	a := uuid.New()
	b := uuid.New()

	// Only A has it privately.
	h.repo.put(workflow.Private(a), sharedWf("a-only"))

	_, _, err := h.engine.Trigger(ctxAs(b), "a-only", nil, "normal")
	require.Error(t, err)
	assert.True(t, errors.Is(err, workflow.ErrNotFound), "B cannot reach A's private workflow")
}
