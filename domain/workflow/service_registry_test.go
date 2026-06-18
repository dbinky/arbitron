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
	mu              sync.Mutex
	byName          map[string]workflow.RegistryEntry
	byID            map[uuid.UUID]string
	failReg         error // when set, Register returns this error (to probe rollback)
	failDel         error // when set, Delete returns this error (to probe partial rollback)
	failRename      error // when set, Rename returns this error (to probe rename rollback)
	failList        error // when set, List returns this error
	failResolve     error // when set, Resolve returns this error (to probe scope-resolution failure paths)
	failResolveID   error // when set, ResolveID returns this error (to probe read-path resolve failure)
	failSetArchived error // when set, SetArchived returns this error (to probe archive failure path)
	failSetOwner    error // when set, SetOwner returns this error (to probe transfer rollback)
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

func (r *fakeRegistry) Resolve(_ context.Context, name string) (workflow.RegistryEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failResolve != nil {
		return workflow.RegistryEntry{}, r.failResolve
	}
	e, ok := r.byName[name]
	if !ok {
		return workflow.RegistryEntry{}, workflow.ErrNotFound
	}
	return e, nil
}

func (r *fakeRegistry) ResolveID(_ context.Context, name string) (uuid.UUID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failResolveID != nil {
		return uuid.Nil, r.failResolveID
	}
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
	if r.failSetArchived != nil {
		return r.failSetArchived
	}
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
	if r.failSetOwner != nil {
		return r.failSetOwner
	}
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
	if r.failList != nil {
		return nil, r.failList
	}
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

// TestService_Create_SharedNameTaken_AlreadyExists verifies that a private
// Create is rejected when the name is already registered as a SHARED workflow.
// UNIQUE(name) in the registry enforces global uniqueness across scopes.
func TestService_Create_SharedNameTaken_AlreadyExists(t *testing.T) {
	repo := newFakeRepo()
	reg := newFakeRegistry()
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetRegistry(reg)
	owner := uuid.New()
	ctx := ctxWith(owner, false)

	// Seed a shared-scope entry directly.
	sharedID := uuid.New()
	repo.bucket(workflow.Shared())["shared-wf"] = wf("shared-wf")
	require.NoError(t, reg.Register(ctx, workflow.RegistryEntry{
		ID: sharedID, Name: "shared-wf", Scope: workflow.ScopeShared,
	}))

	// Create a PRIVATE workflow with the same name → must fail globally.
	err := svc.Create(ctx, "shared-wf", wf("shared-wf"))
	require.ErrorIs(t, err, workflow.ErrAlreadyExists,
		"private create must be rejected when the name is already registered as shared")

	// No private folder was written.
	_, ok := repo.bucket(workflow.Private(owner))["shared-wf"]
	assert.False(t, ok, "no private folder on a cross-scope name collision")
	assert.Equal(t, 1, reg.count(), "registry count unchanged")
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

// --- Resolve-or-allocate id on load (Should Fix #3: no write on the read path) ---

// TestService_Load_ResolveOrAllocate_DoesNotWrite verifies that loading an
// id-less workflow resolves-or-allocates the id PURELY IN MEMORY: Load returns a
// non-nil id but does NOT register a registry row or rewrite the folder. A read
// must not write — the durable write is owned by the startup BackfillRegistry.
func TestService_Load_ResolveOrAllocate_DoesNotWrite(t *testing.T) {
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
	require.NotEqual(t, uuid.Nil, loaded.ID, "Load surfaces an allocated id in memory")

	// No DURABLE write happened on the read path: no registry row was inserted.
	// (The startup BackfillRegistry owns the durable write; a read must not write.)
	assert.Equal(t, 0, reg.count(), "Load must NOT register a registry row on the read path")
	_, regErr := reg.ResolveID(ctx, "legacy")
	assert.ErrorIs(t, regErr, workflow.ErrNotFound, "no registry row was inserted by a read")
}

// TestService_Load_AdoptsBackfilledID verifies that once the startup backfill has
// registered the name, Load adopts that durable id (stable across reloads) rather
// than allocating a new one.
func TestService_Load_AdoptsBackfilledID(t *testing.T) {
	repo := newFakeRepo()
	reg := newFakeRegistry()
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetRegistry(reg)
	owner := uuid.New()
	ctx := ctxWith(owner, false)

	legacy := wf("legacy")
	legacy.ID = uuid.Nil
	repo.bucket(workflow.Private(owner))["legacy"] = legacy

	// Simulate the startup backfill having registered the durable id.
	backfilledID := uuid.New()
	require.NoError(t, reg.Register(ctx, workflow.RegistryEntry{
		ID: backfilledID, Name: "legacy", Scope: workflow.ScopePrivate, OwnerID: owner,
	}))

	first, err := svc.Load(ctx, "legacy")
	require.NoError(t, err)
	assert.Equal(t, backfilledID, first.ID, "Load adopts the backfilled durable id")

	second, err := svc.Load(ctx, "legacy")
	require.NoError(t, err)
	assert.Equal(t, backfilledID, second.ID, "the adopted id is stable across reloads")
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

// --- Error: a non-ErrNotFound resolve failure on the read path propagates ---

// TestService_Load_ResolveError_Propagates verifies that when the registry
// ResolveID returns a non-ErrNotFound error during the read-path id resolution,
// Load surfaces it (rather than masking it as a fresh allocation). No write is
// attempted on the read path.
func TestService_Load_ResolveError_Propagates(t *testing.T) {
	repo := newFakeRepo()
	reg := newFakeRegistry()
	owner := uuid.New()
	ctx := ctxWith(owner, false)

	legacy := wf("legacy")
	legacy.ID = uuid.Nil
	repo.bucket(workflow.Private(owner))["legacy"] = legacy

	resolveErr := errors.New("registry unavailable")
	reg.failResolveID = resolveErr

	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetRegistry(reg)

	_, err := svc.Load(ctx, "legacy")
	require.ErrorIs(t, err, resolveErr,
		"a non-ErrNotFound resolve error must propagate from the read-path id resolution")
}

// errOnUpdateRepo wraps fakeRepo and injects an error on every Update call.
type errOnUpdateRepo struct {
	*fakeRepo
	err error
}

func (r *errOnUpdateRepo) Update(ctx context.Context, s workflow.Scope, name string, wf *workflow.Workflow) error {
	return r.err
}

// TestService_Publish_PrivateNameTaken_AlreadyExists verifies the global
// uniqueness invariant for Publish: if the desired publishedName is already
// registered in the registry under a PRIVATE scope, Publish must return
// ErrAlreadyExists rather than silently linking the shared publication to the
// private workflow's id. This is a cross-scope collision.
func TestService_Publish_PrivateNameTaken_AlreadyExists(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	reg := newFakeRegistry()
	publisher := uuid.New()
	privateOwner := uuid.New()

	// Seed private "alpha" (registered as private).
	privID := uuid.New()
	w := wf("alpha")
	w.ID = privID
	repo.bucket(workflow.Private(privateOwner))["alpha"] = w
	ctx := ctxWith(privateOwner, false)
	require.NoError(t, reg.Register(ctx, workflow.RegistryEntry{
		ID: privID, Name: "alpha", Scope: workflow.ScopePrivate, OwnerID: privateOwner,
	}))

	// Publisher has a private "src" workflow they want to publish as "alpha".
	// Use admin=true to pass authorization (focus is on the name-collision check).
	pubCtx := ctxWith(publisher, true)
	repo.bucket(workflow.Private(publisher))["src"] = wf("src")

	svc := workflow.NewWorkflowService(repo, pubs)
	svc.SetRegistry(reg)

	err := svc.Publish(pubCtx, "src", "alpha", workflow.VisibilityPublic, "")
	require.ErrorIs(t, err, workflow.ErrAlreadyExists,
		"publishing to a name already registered as private must return ErrAlreadyExists")

	// No shared folder or publication record should be written.
	_, hasShared := repo.bucket(workflow.Shared())["alpha"]
	assert.False(t, hasShared, "no shared folder on cross-scope collision")
	_, isPub, _ := pubs.PublicationOf(ctx, "alpha")
	assert.False(t, isPub, "no publication record on cross-scope collision")
}

// TestService_Publish_CrossScopeCheck_PublisherOfError_Propagates verifies that
// a PublisherOf error inside the cross-scope collision check propagates from
// Publish before any write occurs. This covers the case where the publication
// store is unavailable at collision-check time.
func TestService_Publish_CrossScopeCheck_PublisherOfError_Propagates(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	reg := newFakeRegistry()
	publisher := uuid.New()
	pubCtx := ctxWith(publisher, true)
	repo.bucket(workflow.Private(publisher))["src"] = wf("src")

	// Register "alpha" as private so the ResolveID check fires (not ErrNotFound).
	alphaID := uuid.New()
	require.NoError(t, reg.Register(pubCtx, workflow.RegistryEntry{
		ID: alphaID, Name: "alpha", Scope: workflow.ScopePrivate, OwnerID: uuid.New(),
	}))

	lookupErr := errors.New("publication store unavailable")
	pubs.publisherOfErr = lookupErr

	svc := workflow.NewWorkflowService(repo, pubs)
	svc.SetRegistry(reg)

	err := svc.Publish(pubCtx, "src", "alpha", workflow.VisibilityPublic, "")
	require.ErrorIs(t, err, lookupErr,
		"PublisherOf error in the cross-scope check must propagate from Publish")

	// No shared folder written.
	_, hasShared := repo.bucket(workflow.Shared())["alpha"]
	assert.False(t, hasShared, "no shared folder when cross-scope check errors")
}

// TestService_Publish_NoRegistry_CrossScopeNotChecked verifies that in local
// (no-registry) mode, Publish does NOT enforce cross-scope name uniqueness —
// the registry is the uniqueness authority; without it, local mode allows any
// publish. This is the documented non-goal for unconfigured deployments.
func TestService_Publish_NoRegistry_CrossScopeNotChecked(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	publisher := uuid.New()
	pubCtx := ctxWith(publisher, true)
	repo.bucket(workflow.Private(publisher))["src"] = wf("src")

	// No registry wired.
	svc := workflow.NewWorkflowService(repo, pubs)

	err := svc.Publish(pubCtx, "src", "alpha", workflow.VisibilityPublic, "")
	require.NoError(t, err,
		"no-registry mode must allow publish regardless of cross-scope collisions")
}

// TestService_Publish_SamePublisher_CrossScopeCheckPasses verifies that the
// cross-scope check does NOT block a legitimate re-publish by the same publisher.
// When ResolveID returns an existing (shared-scope) entry and PublisherOf
// confirms the same publisher, the check passes through to WriteAndRecord's
// overwrite path.
func TestService_Publish_SamePublisher_CrossScopeCheckPasses(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	reg := newFakeRegistry()
	publisher := uuid.New()
	pubCtx := ctxWith(publisher, true)
	repo.bucket(workflow.Private(publisher))["src"] = wf("src")

	svc := workflow.NewWorkflowService(repo, pubs)
	svc.SetRegistry(reg)

	// First publish.
	require.NoError(t, svc.Publish(pubCtx, "src", "alpha", workflow.VisibilityPublic, ""),
		"first publish must succeed")

	// Seed updated private source so re-publish has something to copy.
	repo.bucket(workflow.Private(publisher))["src"] = wf("src")

	// Re-publish by the same publisher must NOT be blocked by the cross-scope check.
	err := svc.Publish(pubCtx, "src", "alpha", workflow.VisibilityPublic, "")
	require.NoError(t, err,
		"re-publish by the same publisher must not be blocked by the cross-scope collision check")
}

var _ = errors.Is
