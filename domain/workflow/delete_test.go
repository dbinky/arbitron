package workflow_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeActiveCounter is an in-memory workflow.ActiveInstanceCounter. It models the
// instance store: active records the count of non-terminal instances per workflow
// id; history records instances regardless of state, so a test can assert that a
// completed (terminal) instance row survives a Delete (history preservation).
type fakeActiveCounter struct {
	active  map[uuid.UUID]int      // non-terminal instance count by workflow id
	history map[uuid.UUID][]string // all instance states ever recorded by workflow id
	err     error                  // when set, CountActiveByWorkflowID returns it
}

func newFakeActiveCounter() *fakeActiveCounter {
	return &fakeActiveCounter{active: map[uuid.UUID]int{}, history: map[uuid.UUID][]string{}}
}

// seed records an instance in state for workflowID. Non-terminal states increment
// the active count; every state is appended to history (the row is never deleted).
func (c *fakeActiveCounter) seed(workflowID uuid.UUID, state string) {
	c.history[workflowID] = append(c.history[workflowID], state)
	switch state {
	case "completed", "failed", "cancelled":
		// terminal — not active
	default:
		c.active[workflowID]++
	}
}

func (c *fakeActiveCounter) CountActiveByWorkflowID(_ context.Context, workflowID uuid.UUID) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	return c.active[workflowID], nil
}

// deleteSvc wires a service with a registry + active-instance counter, the
// server-mode composition the admin Delete guard requires.
func deleteSvc(t *testing.T) (*workflow.WorkflowService, *fakeRepo, *fakeRegistry, *fakePublishers, *fakeActiveCounter) {
	t.Helper()
	repo := newFakeRepo()
	reg := newFakeRegistry()
	pubs := newFakePublishers()
	counter := newFakeActiveCounter()
	svc := workflow.NewWorkflowService(repo, pubs)
	svc.SetRegistry(reg)
	svc.SetActiveInstanceCounter(counter)
	return svc, repo, reg, pubs, counter
}

// seedPrivateRegistered creates a private workflow with a stable id and registers
// it, returning the id.
func seedPrivateRegistered(t *testing.T, repo *fakeRepo, reg *fakeRegistry, owner uuid.UUID, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	w := wf(name)
	w.ID = id
	repo.bucket(workflow.Private(owner))[name] = w
	require.NoError(t, reg.Register(context.Background(), workflow.RegistryEntry{
		ID: id, Name: name, Scope: workflow.ScopePrivate, OwnerID: owner,
	}))
	return id
}

// --- Failure: delete refused while running ---

// TestService_Delete_RunningInstance_Refused verifies an admin Delete is refused
// with ErrHasRunningInstances when a non-terminal instance carries the workflow
// id, and that nothing (folder or registry row) is removed.
func TestService_Delete_RunningInstance_Refused(t *testing.T) {
	svc, repo, reg, _, counter := deleteSvc(t)
	admin := uuid.New()
	idA := seedPrivateRegistered(t, repo, reg, admin, "alpha")
	counter.seed(idA, "running")

	err := svc.Delete(ctxWith(admin, true), "alpha")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrHasRunningInstances)

	_, folderStill := repo.bucket(workflow.Private(admin))["alpha"]
	assert.True(t, folderStill, "folder must remain when delete is refused")
	_, regErr := reg.ResolveID(context.Background(), "alpha")
	assert.NoError(t, regErr, "registry row must remain when delete is refused")
}

// --- Happy path: delete removes everything; history preserved ---

// TestService_Delete_RemovesFolderAndRegistry verifies that with no active
// instances an admin Delete removes the folder and the registry row, while a
// previously-completed instance row remains queryable (history preserved).
func TestService_Delete_RemovesFolderAndRegistry(t *testing.T) {
	svc, repo, reg, _, counter := deleteSvc(t)
	admin := uuid.New()
	idA := seedPrivateRegistered(t, repo, reg, admin, "alpha")
	counter.seed(idA, "completed") // a past, terminal run

	require.NoError(t, svc.Delete(ctxWith(admin, true), "alpha"))

	_, folderStill := repo.bucket(workflow.Private(admin))["alpha"]
	assert.False(t, folderStill, "folder removed after delete")
	_, regErr := reg.ResolveID(context.Background(), "alpha")
	assert.ErrorIs(t, regErr, workflow.ErrNotFound, "registry row removed after delete")

	// History preserved: the completed instance row is untouched by Delete.
	assert.Contains(t, counter.history[idA], "completed",
		"past instance/audit rows must be preserved (history)")
}

// --- Error: non-admin rejected ---

// TestService_Delete_NonAdmin_Forbidden verifies a non-admin caller is rejected
// with ErrForbidden and nothing is removed.
func TestService_Delete_NonAdmin_Forbidden(t *testing.T) {
	svc, repo, reg, _, _ := deleteSvc(t)
	owner := uuid.New()
	seedPrivateRegistered(t, repo, reg, owner, "alpha")

	err := svc.Delete(ctxWith(owner, false), "alpha")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrForbidden)

	_, folderStill := repo.bucket(workflow.Private(owner))["alpha"]
	assert.True(t, folderStill, "non-admin delete must not remove the folder")
}

// --- Failure: delete a non-existent workflow ---

// TestService_Delete_Unknown_NotFound verifies deleting a name with no registry
// row returns ErrNotFound.
func TestService_Delete_Unknown_NotFound(t *testing.T) {
	svc, _, _, _, _ := deleteSvc(t)
	err := svc.Delete(ctxWith(uuid.New(), true), "ghost")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrNotFound)
}

// --- Edge: delete a published/shared workflow clears publication + shared folder ---

// TestService_Delete_PublishedShared_ClearsPublication verifies that deleting a
// shared (published) workflow also removes its publication record and the shared
// folder — the case that is impossible under the shared-read-only rule today.
func TestService_Delete_PublishedShared_ClearsPublication(t *testing.T) {
	svc, repo, reg, pubs, _ := deleteSvc(t)
	admin := uuid.New()
	publisher := uuid.New()

	// A shared, published workflow with a registered id.
	id := uuid.New()
	sw := wf("alpha")
	sw.ID = id
	repo.bucket(workflow.Shared())["alpha"] = sw
	require.NoError(t, reg.Register(context.Background(), workflow.RegistryEntry{
		ID: id, Name: "alpha", Scope: workflow.ScopeShared,
	}))
	require.NoError(t, pubs.Upsert(context.Background(), "alpha", publisher, "src", workflow.VisibilityPublic, ""))

	require.NoError(t, svc.Delete(ctxWith(admin, true), "alpha"))

	_, folderStill := repo.bucket(workflow.Shared())["alpha"]
	assert.False(t, folderStill, "shared folder removed")
	_, regErr := reg.ResolveID(context.Background(), "alpha")
	assert.ErrorIs(t, regErr, workflow.ErrNotFound, "registry row removed")
	_, published, _ := pubs.PublicationOf(context.Background(), "alpha")
	assert.False(t, published, "publication record cleared")
}

// --- Edge: no registry wired (local mode) falls back to private folder delete ---

// TestService_Delete_NoRegistry_LocalMode verifies that without a wired registry
// (the MCP/local composition) Delete still removes the caller's private folder.
// Local mode seeds an admin identity, so requireAdmin passes.
func TestService_Delete_NoRegistry_LocalMode(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil) // no registry, no counter
	owner := uuid.New()
	repo.bucket(workflow.Private(owner))["mine"] = wf("mine")

	require.NoError(t, svc.Delete(ctxWith(owner, true), "mine"))
	_, ok := repo.bucket(workflow.Private(owner))["mine"]
	assert.False(t, ok, "local-mode delete removes the private folder")
}
