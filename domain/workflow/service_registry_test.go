package workflow_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRegistry is an in-memory workflow.WorkflowRegistry keyed by name, with the
// same UNIQUE(name) semantics as the SQLite implementation: Register rejects a
// duplicate name with ErrAlreadyExists; the id-targeted operations report
// ErrNotFound for an unknown id.
type fakeRegistry struct {
	mu         sync.Mutex
	byName     map[string]workflow.RegistryEntry
	byID       map[uuid.UUID]string
	failReg    error // when set, Register returns this error (to probe rollback)
	failDel    error // when set, Delete returns this error (to probe partial rollback)
	failRename error // when set, Rename returns this error (to probe rename rollback)
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{
		byName: map[string]workflow.RegistryEntry{},
		byID:   map[uuid.UUID]string{},
	}
}

func (r *fakeRegistry) Register(_ context.Context, e workflow.RegistryEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failReg != nil {
		return r.failReg
	}
	if _, ok := r.byName[e.Name]; ok {
		return workflow.ErrAlreadyExists
	}
	r.byName[e.Name] = e
	r.byID[e.ID] = e.Name
	return nil
}

func (r *fakeRegistry) ResolveID(_ context.Context, name string) (uuid.UUID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.byName[name]
	if !ok {
		return uuid.Nil, workflow.ErrNotFound
	}
	return e.ID, nil
}

func (r *fakeRegistry) ResolveName(_ context.Context, id uuid.UUID) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name, ok := r.byID[id]
	if !ok {
		return "", workflow.ErrNotFound
	}
	return name, nil
}

func (r *fakeRegistry) Rename(_ context.Context, id uuid.UUID, newName string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failRename != nil {
		return r.failRename
	}
	old, ok := r.byID[id]
	if !ok {
		return workflow.ErrNotFound
	}
	if _, exists := r.byName[newName]; exists && newName != old {
		return workflow.ErrAlreadyExists
	}
	e := r.byName[old]
	e.Name = newName
	delete(r.byName, old)
	r.byName[newName] = e
	r.byID[id] = newName
	return nil
}

func (r *fakeRegistry) SetArchived(_ context.Context, id uuid.UUID, archived bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	name, ok := r.byID[id]
	if !ok {
		return workflow.ErrNotFound
	}
	e := r.byName[name]
	e.Archived = archived
	r.byName[name] = e
	return nil
}

func (r *fakeRegistry) SetOwner(_ context.Context, id, owner uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	name, ok := r.byID[id]
	if !ok {
		return workflow.ErrNotFound
	}
	e := r.byName[name]
	e.OwnerID = owner
	r.byName[name] = e
	return nil
}

func (r *fakeRegistry) Delete(_ context.Context, id uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failDel != nil {
		return r.failDel
	}
	name, ok := r.byID[id]
	if !ok {
		return workflow.ErrNotFound
	}
	delete(r.byName, name)
	delete(r.byID, id)
	return nil
}

func (r *fakeRegistry) List(_ context.Context) ([]workflow.RegistryEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]workflow.RegistryEntry, 0, len(r.byName))
	for _, e := range r.byName {
		out = append(out, e)
	}
	return out, nil
}

func (r *fakeRegistry) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byName)
}

// --- Happy path ---

// TestService_Create_RegistersID verifies a create allocates+persists a non-zero
// id, registers a row, and that resolveID(name) returns it (via ResolveID on the
// registry, the same lookup the service uses internally).
func TestService_Create_RegistersID(t *testing.T) {
	repo := newFakeRepo()
	reg := newFakeRegistry()
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetRegistry(reg)
	owner := uuid.New()
	ctx := ctxWith(owner, false)

	require.NoError(t, svc.Create(ctx, "w", wf("w")))

	// The persisted workflow.json carries a non-zero id.
	stored := repo.bucket(workflow.Private(owner))["w"]
	require.NotNil(t, stored)
	assert.NotEqual(t, uuid.Nil, stored.ID, "persisted workflow has a non-zero id")

	// A registry row exists and resolves both directions to that id.
	gotID, err := reg.ResolveID(ctx, "w")
	require.NoError(t, err)
	assert.Equal(t, stored.ID, gotID)

	gotName, err := reg.ResolveName(ctx, gotID)
	require.NoError(t, err)
	assert.Equal(t, "w", gotName)
}

// --- Success ---

// TestService_ResolveHelpers verifies the unexported resolve helpers round-trip
// name<->id through the wired registry.
func TestService_ResolveHelpers(t *testing.T) {
	repo := newFakeRepo()
	reg := newFakeRegistry()
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetRegistry(reg)
	owner := uuid.New()
	ctx := ctxWith(owner, false)

	require.NoError(t, svc.Create(ctx, "alpha", wf("alpha")))

	id, err := workflow.ServiceResolveID(svc, ctx, "alpha")
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, id)

	name, err := workflow.ServiceResolveName(svc, ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "alpha", name)
}

// TestService_Create_NoRegistry_StillWorks verifies that without a wired registry
// (local/unconfigured deployments) create still works name-addressed as before.
func TestService_Create_NoRegistry_StillWorks(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil) // no registry
	owner := uuid.New()

	require.NoError(t, svc.Create(ctxWith(owner, false), "w", wf("w")))
	_, ok := repo.bucket(workflow.Private(owner))["w"]
	assert.True(t, ok)
}

// --- Failure ---

// TestService_Create_DuplicateName_NoFolderWritten verifies that a second create
// of a name already in the registry is rejected with ErrAlreadyExists and that
// the second create does NOT write a folder (uniqueness check happens first).
func TestService_Create_DuplicateName_NoFolderWritten(t *testing.T) {
	repo := newFakeRepo()
	reg := newFakeRegistry()
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetRegistry(reg)
	owner := uuid.New()
	ctx := ctxWith(owner, false)

	require.NoError(t, svc.Create(ctx, "alpha", wf("alpha")))
	// Use a different owner so the failure cannot be attributed to the repo's own
	// per-scope name collision — the registry must reject it globally.
	other := uuid.New()
	octx := ctxWith(other, false)

	err := svc.Create(octx, "alpha", wf("alpha"))
	require.ErrorIs(t, err, workflow.ErrAlreadyExists)

	// The second caller's private scope must not have a folder written for it.
	_, ok := repo.bucket(workflow.Private(other))["alpha"]
	assert.False(t, ok, "duplicate create must not write a folder")
	assert.Equal(t, 1, reg.count(), "only one registry row")
}

// --- Error ---

// TestService_Create_RegistryFailure_RollsBack verifies that if the folder write
// fails after a successful registry insert, the registry row is rolled back so
// the two stores do not diverge.
func TestService_Create_FolderFailure_RollsBackRegistry(t *testing.T) {
	repo := newFakeRepo()
	reg := newFakeRegistry()
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetRegistry(reg)
	owner := uuid.New()
	ctx := ctxWith(owner, false)

	// Pre-seed the folder so repo.Create returns ErrAlreadyExists (a folder-write
	// failure independent of the registry).
	repo.bucket(workflow.Private(owner))["w"] = wf("w")

	err := svc.Create(ctx, "w", wf("w"))
	require.Error(t, err)

	// The registry must not retain a row for a workflow whose folder write failed.
	assert.Equal(t, 0, reg.count(), "registry rolled back on folder-write failure")
}

// --- Edge: case/whitespace follows safeWorkflowName (case-sensitive) ---

// TestService_Create_CaseSensitiveNames verifies that names differing only by
// case are distinct (mirroring the case-sensitive filesystem/safeWorkflowName
// behavior and the BINARY-collation UNIQUE(name) in SQLite).
func TestService_Create_CaseSensitiveNames(t *testing.T) {
	repo := newFakeRepo()
	reg := newFakeRegistry()
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetRegistry(reg)
	owner := uuid.New()
	ctx := ctxWith(owner, false)

	require.NoError(t, svc.Create(ctx, "Alpha", wf("Alpha")))
	require.NoError(t, svc.Create(ctx, "alpha", wf("alpha")), "case-different name is distinct")
	assert.Equal(t, 2, reg.count())
}

// --- Lazy backfill on load ---

// TestService_Load_LazyBackfill_StableAcrossReloads verifies that loading an
// id-less workflow assigns+persists a stable id and registers it, and that a
// reload returns the same id.
func TestService_Load_LazyBackfill_StableAcrossReloads(t *testing.T) {
	repo := newFakeRepo()
	reg := newFakeRegistry()
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetRegistry(reg)
	owner := uuid.New()
	ctx := ctxWith(owner, false)

	// Seed an id-less (legacy) workflow directly in the repo.
	legacy := wf("legacy")
	legacy.ID = uuid.Nil
	repo.bucket(workflow.Private(owner))["legacy"] = legacy

	loaded, err := svc.Load(ctx, "legacy")
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, loaded.ID, "lazy backfill assigned an id")
	firstID := loaded.ID

	// The id was persisted to the folder and registered.
	assert.Equal(t, firstID, repo.bucket(workflow.Private(owner))["legacy"].ID)
	regID, err := reg.ResolveID(ctx, "legacy")
	require.NoError(t, err)
	assert.Equal(t, firstID, regID)

	// Reload yields the SAME id (no re-allocation).
	reloaded, err := svc.Load(ctx, "legacy")
	require.NoError(t, err)
	assert.Equal(t, firstID, reloaded.ID, "id stable across reloads")
	assert.Equal(t, 1, reg.count(), "no duplicate registry rows on reload")
}

// TestService_Load_BackfillAdoptsExistingRow verifies that if a registry row
// already exists for the name (e.g. registered by startup backfill) but the
// folder is id-less, load adopts the existing id rather than failing on the
// UNIQUE(name) violation.
func TestService_Load_BackfillAdoptsExistingRow(t *testing.T) {
	repo := newFakeRepo()
	reg := newFakeRegistry()
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetRegistry(reg)
	owner := uuid.New()
	ctx := ctxWith(owner, false)

	existingID := uuid.New()
	require.NoError(t, reg.Register(ctx, workflow.RegistryEntry{
		ID: existingID, Name: "legacy", Scope: workflow.ScopePrivate, OwnerID: owner,
	}))

	legacy := wf("legacy")
	legacy.ID = uuid.Nil
	repo.bucket(workflow.Private(owner))["legacy"] = legacy

	loaded, err := svc.Load(ctx, "legacy")
	require.NoError(t, err)
	assert.Equal(t, existingID, loaded.ID, "adopted the existing registry id")
	assert.Equal(t, 1, reg.count())
}

// TestService_Load_NoRegistry_StillWorks verifies that loading without a wired
// registry preserves the prior behavior (no id assignment, no error).
func TestService_Load_NoRegistry_StillWorks(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil) // no registry
	owner := uuid.New()
	ctx := ctxWith(owner, false)

	legacy := wf("legacy")
	legacy.ID = uuid.Nil
	repo.bucket(workflow.Private(owner))["legacy"] = legacy

	loaded, err := svc.Load(ctx, "legacy")
	require.NoError(t, err)
	assert.Equal(t, uuid.Nil, loaded.ID)
}

// --- Error: rollback delete also fails ---

// TestService_Create_RollbackDeleteFails_CreateErrorStillReturned verifies the
// cross-store partial failure contract: if the folder write fails AND the
// compensating registry Delete also fails, Create still returns the original
// folder write error. The delete error is silently swallowed (the `_ =` in
// service.go is intentional — the orphan row is an operational concern, not a
// caller concern) and must not replace the actionable folder error.
func TestService_Create_RollbackDeleteFails_CreateErrorStillReturned(t *testing.T) {
	repo := newFakeRepo()
	reg := newFakeRegistry()
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetRegistry(reg)
	owner := uuid.New()
	ctx := ctxWith(owner, false)

	// Pre-seed the folder so that the repo.Create step returns ErrAlreadyExists
	// (simulates a filesystem collision independent of the registry).
	repo.bucket(workflow.Private(owner))["w"] = wf("w")

	// Make the rollback Delete return an error too.
	delErr := errors.New("registry delete unavailable")
	reg.failDel = delErr

	err := svc.Create(ctx, "w", wf("w"))

	// The caller must see the folder error, not the delete error.
	require.ErrorIs(t, err, workflow.ErrAlreadyExists,
		"Create must return the folder-write error even when rollback Delete also fails")
	assert.False(t, errors.Is(err, delErr),
		"the rollback delete error must not surface to the caller")

	// The registry retains the orphan row (rollback failed), but the contract is
	// that Create returns the write error regardless.
	assert.Equal(t, 1, reg.count(), "orphan row remains because delete failed")
}

// --- Error: ensureID path D (folder Update fails during lazy backfill) ---

// TestService_Load_EnsureID_UpdateFailurePropagates verifies that when the
// folder persistence step (repo.Update) inside ensureID fails, Load propagates
// the error and does not return a half-initialised workflow with a freshly
// allocated id that was never durably written.
func TestService_Load_EnsureID_UpdateFailurePropagates(t *testing.T) {
	inner := newFakeRepo()
	reg := newFakeRegistry()
	owner := uuid.New()
	ctx := ctxWith(owner, false)

	// Seed an id-less (legacy) workflow in the inner repo.
	legacy := wf("legacy")
	legacy.ID = uuid.Nil
	inner.bucket(workflow.Private(owner))["legacy"] = legacy

	// Wrap inner with a repo that intercepts Update and returns a fixed error.
	updateErr := errors.New("disk full")
	repo := &errOnUpdateRepo{fakeRepo: inner, err: updateErr}

	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetRegistry(reg)

	_, err := svc.Load(ctx, "legacy")
	require.ErrorIs(t, err, updateErr,
		"Load must propagate the Update error so the caller knows the id was not durably persisted")
}

// errOnUpdateRepo wraps fakeRepo and injects an error on every Update call.
type errOnUpdateRepo struct {
	*fakeRepo
	err error
}

func (r *errOnUpdateRepo) Update(ctx context.Context, s workflow.Scope, name string, wf *workflow.Workflow) error {
	return r.err
}

var _ = errors.Is
