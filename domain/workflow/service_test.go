package workflow_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRepo is an in-memory WorkflowRepository keyed by (scopeKey, name) so the
// service's scope routing is observable.
type fakeRepo struct {
	// store[scopeKey][name] = *Workflow
	store map[string]map[string]*workflow.Workflow
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{store: map[string]map[string]*workflow.Workflow{}}
}

func scopeKey(s workflow.Scope) string {
	if s.Kind == workflow.ScopeShared {
		return "shared"
	}
	return "private:" + s.OwnerID.String()
}

func (r *fakeRepo) bucket(s workflow.Scope) map[string]*workflow.Workflow {
	k := scopeKey(s)
	if r.store[k] == nil {
		r.store[k] = map[string]*workflow.Workflow{}
	}
	return r.store[k]
}

func (r *fakeRepo) Load(_ context.Context, s workflow.Scope, name string) (*workflow.Workflow, error) {
	wf, ok := r.bucket(s)[name]
	if !ok {
		return nil, workflow.ErrNotFound
	}
	return wf, nil
}

func (r *fakeRepo) List(_ context.Context, s workflow.Scope) ([]workflow.WorkflowSummary, error) {
	var out []workflow.WorkflowSummary
	for _, wf := range r.bucket(s) {
		out = append(out, workflow.WorkflowSummary{Name: wf.Name, Description: wf.Description, Author: wf.Author, NodeCount: len(wf.Nodes)})
	}
	return out, nil
}

func (r *fakeRepo) SaveNodePositions(_ context.Context, s workflow.Scope, name string, _ map[string]workflow.NodePosition) error {
	if _, ok := r.bucket(s)[name]; !ok {
		return workflow.ErrNotFound
	}
	return nil
}

func (r *fakeRepo) Create(_ context.Context, s workflow.Scope, name string, wf *workflow.Workflow) error {
	b := r.bucket(s)
	if _, ok := b[name]; ok {
		return workflow.ErrAlreadyExists
	}
	b[name] = wf
	return nil
}

func (r *fakeRepo) Update(_ context.Context, s workflow.Scope, name string, wf *workflow.Workflow) error {
	b := r.bucket(s)
	if _, ok := b[name]; !ok {
		return workflow.ErrNotFound
	}
	b[name] = wf
	return nil
}

func (r *fakeRepo) Delete(_ context.Context, s workflow.Scope, name string) error {
	b := r.bucket(s)
	if _, ok := b[name]; !ok {
		return workflow.ErrNotFound
	}
	delete(b, name)
	return nil
}

func (r *fakeRepo) WriteFile(_ context.Context, s workflow.Scope, name, _ string, _ []byte) error {
	if _, ok := r.bucket(s)[name]; !ok {
		return workflow.ErrNotFound
	}
	return nil
}

func (r *fakeRepo) DeleteFile(_ context.Context, s workflow.Scope, name, _ string) error {
	if _, ok := r.bucket(s)[name]; !ok {
		return workflow.ErrNotFound
	}
	return nil
}

func (r *fakeRepo) ListFiles(_ context.Context, s workflow.Scope, name string) ([]string, error) {
	if _, ok := r.bucket(s)[name]; !ok {
		return nil, workflow.ErrNotFound
	}
	return []string{}, nil
}

func (r *fakeRepo) Exists(_ context.Context, s workflow.Scope, name string) (bool, error) {
	_, ok := r.bucket(s)[name]
	return ok, nil
}

func (r *fakeRepo) CopyTree(_ context.Context, src workflow.Scope, srcName string, dst workflow.Scope, dstName string, overwrite bool) error {
	wf, ok := r.bucket(src)[srcName]
	if !ok {
		return workflow.ErrNotFound
	}
	if _, exists := r.bucket(dst)[dstName]; exists && !overwrite {
		return workflow.ErrAlreadyExists
	}
	// Deep-ish copy so the destination is independent of the source.
	cp := *wf
	cp.Name = dstName
	r.bucket(dst)[dstName] = &cp
	return nil
}

// fakePublishers is a fake PublishedStore (lookup + write).
type fakePublishers struct {
	pub map[string]uuid.UUID
}

func newFakePublishers() *fakePublishers {
	return &fakePublishers{pub: map[string]uuid.UUID{}}
}

func (p *fakePublishers) PublisherOf(_ context.Context, name string) (uuid.UUID, bool, error) {
	id, ok := p.pub[name]
	return id, ok, nil
}

func (p *fakePublishers) Upsert(_ context.Context, name string, publisher uuid.UUID, _ string) error {
	p.pub[name] = publisher
	return nil
}

func (p *fakePublishers) Delete(_ context.Context, name string) error {
	delete(p.pub, name)
	return nil
}

func ctxWith(id uuid.UUID, admin bool) context.Context {
	role := identity.RoleUser
	if admin {
		role = identity.RoleAdmin
	}
	return identity.WithIdentity(context.Background(), identity.Identity{UserID: id, Username: "u", Role: role})
}

func wf(name string) *workflow.Workflow {
	return &workflow.Workflow{Name: name, Description: "d", Author: "a"}
}

// --- Happy path ---

func TestService_Create_TargetsPrivate(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil)
	owner := uuid.New()

	require.NoError(t, svc.Create(ctxWith(owner, false), "w", wf("w")))

	_, ok := repo.bucket(workflow.Private(owner))["w"]
	assert.True(t, ok, "created in caller's private scope")
	_, sharedOK := repo.bucket(workflow.Shared())["w"]
	assert.False(t, sharedOK, "never written to shared")
}

func TestService_Load_PrivateThenShared(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil)
	owner := uuid.New()
	require.NoError(t, repo.Create(context.Background(), workflow.Private(owner), "p", wf("p")))

	got, err := svc.Load(ctxWith(owner, false), "p")
	require.NoError(t, err)
	assert.Equal(t, "p", got.Name)
}

func TestService_List_PrivatePlusShared(t *testing.T) {
	repo := newFakeRepo()
	pubID := uuid.New()
	pubs := &fakePublishers{pub: map[string]uuid.UUID{"sh": pubID}}
	svc := workflow.NewWorkflowService(repo, pubs)
	owner := uuid.New()

	require.NoError(t, repo.Create(context.Background(), workflow.Private(owner), "mine", wf("mine")))
	require.NoError(t, repo.Create(context.Background(), workflow.Shared(), "sh", wf("sh")))

	list, err := svc.List(ctxWith(owner, false))
	require.NoError(t, err)
	require.Len(t, list, 2)

	byName := map[string]workflow.WorkflowSummary{}
	for _, s := range list {
		byName[s.Name] = s
	}
	assert.Equal(t, workflow.ScopeLabelPrivate, byName["mine"].Scope)
	assert.Empty(t, byName["mine"].Publisher)
	assert.Equal(t, workflow.ScopeLabelShared, byName["sh"].Scope)
	assert.Equal(t, pubID.String(), byName["sh"].Publisher)
}

// --- Validation ---

func TestService_NoIdentity_Rejected(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil)

	err := svc.Create(context.Background(), "w", wf("w"))
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrInvalidName)

	_, err = svc.Load(context.Background(), "w")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrInvalidName)
}

func TestService_List_NameCollisionYieldsTwoEntries(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil)
	owner := uuid.New()

	require.NoError(t, repo.Create(context.Background(), workflow.Private(owner), "dup", wf("dup")))
	require.NoError(t, repo.Create(context.Background(), workflow.Shared(), "dup", wf("dup")))

	list, err := svc.List(ctxWith(owner, false))
	require.NoError(t, err)
	require.Len(t, list, 2)

	scopes := []string{list[0].Scope, list[1].Scope}
	assert.Contains(t, scopes, workflow.ScopeLabelPrivate)
	assert.Contains(t, scopes, workflow.ScopeLabelShared)
}

// --- Error/failure ---

func TestService_Update_SharedOnly_ReturnsSharedReadOnly(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil)
	owner := uuid.New()
	require.NoError(t, repo.Create(context.Background(), workflow.Shared(), "pub", wf("pub")))

	err := svc.Update(ctxWith(owner, false), "pub", wf("pub"))
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrSharedReadOnly)
	assert.NotErrorIs(t, err, workflow.ErrNotFound)
}

func TestService_Update_Nowhere_ReturnsNotFound(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil)
	owner := uuid.New()

	err := svc.Update(ctxWith(owner, false), "ghost", wf("ghost"))
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrNotFound)
}

// --- Boundary ---

func TestService_List_EmptyPrivate_ReturnsSharedOnly(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil)
	owner := uuid.New()
	require.NoError(t, repo.Create(context.Background(), workflow.Shared(), "sh", wf("sh")))

	list, err := svc.List(ctxWith(owner, false))
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "sh", list[0].Name)
	assert.Equal(t, workflow.ScopeLabelShared, list[0].Scope)
	assert.Empty(t, list[0].Publisher, "nil publisher lookup leaves Publisher empty")
}

// --- Isolation/security ---

func TestService_LoadAs_NonAdminOtherOwner_Unauthorized(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil)
	caller := uuid.New()
	other := uuid.New()
	require.NoError(t, repo.Create(context.Background(), workflow.Private(other), "secret", wf("secret")))

	_, err := svc.LoadAs(ctxWith(caller, false), other, "secret")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrUnauthorized)
}

func TestService_LoadAs_AdminOtherOwner_Loads(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil)
	admin := uuid.New()
	other := uuid.New()
	require.NoError(t, repo.Create(context.Background(), workflow.Private(other), "secret", wf("secret")))

	got, err := svc.LoadAs(ctxWith(admin, true), other, "secret")
	require.NoError(t, err)
	assert.Equal(t, "secret", got.Name)
}

func TestService_LoadAs_SelfOwner_LoadsPrivate(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil)
	owner := uuid.New()
	require.NoError(t, repo.Create(context.Background(), workflow.Private(owner), "mine", wf("mine")))

	got, err := svc.LoadAs(ctxWith(owner, false), owner, "mine")
	require.NoError(t, err)
	assert.Equal(t, "mine", got.Name)
}

func TestService_Load_DoesNotLeakOtherUsersPrivate(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil)
	a := uuid.New()
	b := uuid.New()
	require.NoError(t, repo.Create(context.Background(), workflow.Private(a), "a-secret", wf("a-secret")))

	_, err := svc.Load(ctxWith(b, false), "a-secret")
	require.Error(t, err)
	assert.True(t, errors.Is(err, workflow.ErrNotFound))
}

// --- Publish / Copy ---

func TestService_Publish_HappyPath(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	svc := workflow.NewWorkflowService(repo, pubs)
	owner := uuid.New()
	require.NoError(t, repo.Create(context.Background(), workflow.Private(owner), "src", wf("src")))

	require.NoError(t, svc.Publish(ctxWith(owner, false), "src", "pub"))

	_, ok := repo.bucket(workflow.Shared())["pub"]
	assert.True(t, ok, "published into shared")
	pubID, recorded, err := pubs.PublisherOf(context.Background(), "pub")
	require.NoError(t, err)
	require.True(t, recorded)
	assert.Equal(t, owner, pubID)
}

func TestService_Publish_SourceMissing_NotFound(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, newFakePublishers())
	owner := uuid.New()

	err := svc.Publish(ctxWith(owner, false), "ghost", "pub")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrNotFound)
}

func TestService_Publish_DifferentPublisher_Unauthorized(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	svc := workflow.NewWorkflowService(repo, pubs)
	a := uuid.New()
	b := uuid.New()

	// "pub" already published by a.
	require.NoError(t, repo.Create(context.Background(), workflow.Shared(), "pub", wf("pub")))
	pubs.pub["pub"] = a

	// b owns a private "src" and tries to publish over a's name.
	require.NoError(t, repo.Create(context.Background(), workflow.Private(b), "src", wf("src")))
	err := svc.Publish(ctxWith(b, false), "src", "pub")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrUnauthorized)

	// Record unchanged.
	pubID, _, _ := pubs.PublisherOf(context.Background(), "pub")
	assert.Equal(t, a, pubID)
}

func TestService_Publish_SamePublisher_Overwrites(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	svc := workflow.NewWorkflowService(repo, pubs)
	owner := uuid.New()

	require.NoError(t, repo.Create(context.Background(), workflow.Private(owner), "src", wf("src")))
	require.NoError(t, svc.Publish(ctxWith(owner, false), "src", "pub"))
	// Re-publish by the same owner succeeds (overwrite).
	require.NoError(t, svc.Publish(ctxWith(owner, false), "src", "pub"))
}

func TestService_Publish_NoStore_Errors(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil) // no published store
	owner := uuid.New()
	require.NoError(t, repo.Create(context.Background(), workflow.Private(owner), "src", wf("src")))

	err := svc.Publish(ctxWith(owner, false), "src", "pub")
	require.Error(t, err)
}

func TestService_Copy_HappyPath(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil)
	owner := uuid.New()
	require.NoError(t, repo.Create(context.Background(), workflow.Shared(), "orig", wf("orig")))

	require.NoError(t, svc.Copy(ctxWith(owner, false), "orig", "mine"))

	_, ok := repo.bucket(workflow.Private(owner))["mine"]
	assert.True(t, ok, "copied into caller's private scope")
}

func TestService_Copy_SharedMissing_NotFound(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil)
	owner := uuid.New()

	err := svc.Copy(ctxWith(owner, false), "ghost", "mine")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrNotFound)
}

func TestService_Copy_DuplicatePrivateName_AlreadyExists(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil)
	owner := uuid.New()
	require.NoError(t, repo.Create(context.Background(), workflow.Shared(), "orig", wf("orig")))
	require.NoError(t, repo.Create(context.Background(), workflow.Private(owner), "mine", wf("mine")))

	err := svc.Copy(ctxWith(owner, false), "orig", "mine")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrAlreadyExists)
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

// Happy path: Delete removes a workflow from the caller's private scope.
func TestService_Delete_TargetsPrivate(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil)
	owner := uuid.New()
	require.NoError(t, repo.Create(context.Background(), workflow.Private(owner), "mine", wf("mine")))

	require.NoError(t, svc.Delete(ctxWith(owner, false), "mine"))

	_, ok := repo.bucket(workflow.Private(owner))["mine"]
	assert.False(t, ok, "workflow must be removed from private scope after delete")
}

// Failure: deleting a shared-only workflow returns ErrSharedReadOnly (not ErrNotFound).
func TestService_Delete_SharedOnly_ReturnsSharedReadOnly(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil)
	owner := uuid.New()
	require.NoError(t, repo.Create(context.Background(), workflow.Shared(), "pub", wf("pub")))

	err := svc.Delete(ctxWith(owner, false), "pub")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrSharedReadOnly)
	assert.NotErrorIs(t, err, workflow.ErrNotFound)
}

// Error: no identity in context is rejected.
func TestService_Delete_NoIdentity_Rejected(t *testing.T) {
	svc := workflow.NewWorkflowService(newFakeRepo(), nil)

	err := svc.Delete(context.Background(), "w")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrInvalidName)
}

// Failure: deleting a workflow that exists in neither scope returns ErrNotFound.
func TestService_Delete_Nowhere_ReturnsNotFound(t *testing.T) {
	svc := workflow.NewWorkflowService(newFakeRepo(), nil)

	err := svc.Delete(ctxWith(uuid.New(), false), "ghost")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrNotFound)
}

// Edge: Delete only removes the caller's private copy; another user's private
// copy with the same name is untouched.
func TestService_Delete_OnlyAffectsCallerPrivate(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil)
	a := uuid.New()
	b := uuid.New()
	require.NoError(t, repo.Create(context.Background(), workflow.Private(a), "shared-name", wf("shared-name")))
	require.NoError(t, repo.Create(context.Background(), workflow.Private(b), "shared-name", wf("shared-name")))

	require.NoError(t, svc.Delete(ctxWith(a, false), "shared-name"))

	_, aGone := repo.bucket(workflow.Private(a))["shared-name"]
	assert.False(t, aGone, "caller a's copy removed")
	_, bStill := repo.bucket(workflow.Private(b))["shared-name"]
	assert.True(t, bStill, "user b's copy must be untouched")
}

// ---------------------------------------------------------------------------
// WriteFile (service-level)
// ---------------------------------------------------------------------------

// Happy path: WriteFile routes to the caller's private scope.
func TestService_WriteFile_TargetsPrivate(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil)
	owner := uuid.New()
	require.NoError(t, repo.Create(context.Background(), workflow.Private(owner), "wf", wf("wf")))

	err := svc.WriteFile(ctxWith(owner, false), "wf", "run.sh", []byte("#!/bin/bash"))
	assert.NoError(t, err)
}

// Failure: writing into a shared-only workflow returns ErrSharedReadOnly.
func TestService_WriteFile_SharedOnly_ReturnsSharedReadOnly(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil)
	owner := uuid.New()
	require.NoError(t, repo.Create(context.Background(), workflow.Shared(), "pub", wf("pub")))

	err := svc.WriteFile(ctxWith(owner, false), "pub", "a.sh", []byte("x"))
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrSharedReadOnly)
}

// Error: no identity is rejected.
func TestService_WriteFile_NoIdentity_Rejected(t *testing.T) {
	svc := workflow.NewWorkflowService(newFakeRepo(), nil)

	err := svc.WriteFile(context.Background(), "wf", "a.sh", []byte("x"))
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrInvalidName)
}

// Failure: workflow exists in neither scope returns ErrNotFound.
func TestService_WriteFile_Nowhere_ReturnsNotFound(t *testing.T) {
	svc := workflow.NewWorkflowService(newFakeRepo(), nil)

	err := svc.WriteFile(ctxWith(uuid.New(), false), "ghost", "a.sh", []byte("x"))
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrNotFound)
}

// Edge: WriteFile does not touch another user's private workflow.
func TestService_WriteFile_OnlyAffectsCallerPrivate(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil)
	a := uuid.New()
	b := uuid.New()
	require.NoError(t, repo.Create(context.Background(), workflow.Private(b), "shared-name", wf("shared-name")))

	// a has no private "shared-name" — WriteFile must not silently target b's.
	err := svc.WriteFile(ctxWith(a, false), "shared-name", "a.sh", []byte("x"))
	require.Error(t, err, "must not write into another user's private scope")
}

// ---------------------------------------------------------------------------
// DeleteFile (service-level)
// ---------------------------------------------------------------------------

// Happy path: DeleteFile routes to the caller's private scope.
func TestService_DeleteFile_TargetsPrivate(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil)
	owner := uuid.New()
	require.NoError(t, repo.Create(context.Background(), workflow.Private(owner), "wf", wf("wf")))

	err := svc.DeleteFile(ctxWith(owner, false), "wf", "run.sh")
	assert.NoError(t, err)
}

// Failure: deleting a file from a shared-only workflow returns ErrSharedReadOnly.
func TestService_DeleteFile_SharedOnly_ReturnsSharedReadOnly(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil)
	owner := uuid.New()
	require.NoError(t, repo.Create(context.Background(), workflow.Shared(), "pub", wf("pub")))

	err := svc.DeleteFile(ctxWith(owner, false), "pub", "a.sh")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrSharedReadOnly)
}

// Error: no identity is rejected.
func TestService_DeleteFile_NoIdentity_Rejected(t *testing.T) {
	svc := workflow.NewWorkflowService(newFakeRepo(), nil)

	err := svc.DeleteFile(context.Background(), "wf", "a.sh")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrInvalidName)
}

// Failure: workflow in neither scope returns ErrNotFound.
func TestService_DeleteFile_Nowhere_ReturnsNotFound(t *testing.T) {
	svc := workflow.NewWorkflowService(newFakeRepo(), nil)

	err := svc.DeleteFile(ctxWith(uuid.New(), false), "ghost", "a.sh")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrNotFound)
}

// Edge: DeleteFile does not reach another user's private workflow.
func TestService_DeleteFile_OnlyAffectsCallerPrivate(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil)
	a := uuid.New()
	b := uuid.New()
	require.NoError(t, repo.Create(context.Background(), workflow.Private(b), "shared-name", wf("shared-name")))

	err := svc.DeleteFile(ctxWith(a, false), "shared-name", "a.sh")
	require.Error(t, err, "must not target another user's private scope")
}

// ---------------------------------------------------------------------------
// SaveNodePositions (service-level)
// ---------------------------------------------------------------------------

// Happy path: SaveNodePositions routes to the caller's private scope.
func TestService_SaveNodePositions_TargetsPrivate(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil)
	owner := uuid.New()
	require.NoError(t, repo.Create(context.Background(), workflow.Private(owner), "wf", wf("wf")))

	err := svc.SaveNodePositions(ctxWith(owner, false), "wf", map[string]workflow.NodePosition{"n1": {X: 1, Y: 2}})
	assert.NoError(t, err)
}

// Failure: saving positions on a shared-only workflow returns ErrSharedReadOnly.
func TestService_SaveNodePositions_SharedOnly_ReturnsSharedReadOnly(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil)
	owner := uuid.New()
	require.NoError(t, repo.Create(context.Background(), workflow.Shared(), "pub", wf("pub")))

	err := svc.SaveNodePositions(ctxWith(owner, false), "pub", map[string]workflow.NodePosition{})
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrSharedReadOnly)
}

// Error: no identity is rejected.
func TestService_SaveNodePositions_NoIdentity_Rejected(t *testing.T) {
	svc := workflow.NewWorkflowService(newFakeRepo(), nil)

	err := svc.SaveNodePositions(context.Background(), "wf", map[string]workflow.NodePosition{})
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrInvalidName)
}

// Failure: workflow in neither scope returns ErrNotFound.
func TestService_SaveNodePositions_Nowhere_ReturnsNotFound(t *testing.T) {
	svc := workflow.NewWorkflowService(newFakeRepo(), nil)

	err := svc.SaveNodePositions(ctxWith(uuid.New(), false), "ghost", nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrNotFound)
}

// Edge: SaveNodePositions does not reach another user's private workflow.
func TestService_SaveNodePositions_OnlyAffectsCallerPrivate(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil)
	a := uuid.New()
	b := uuid.New()
	require.NoError(t, repo.Create(context.Background(), workflow.Private(b), "shared-name", wf("shared-name")))

	err := svc.SaveNodePositions(ctxWith(a, false), "shared-name", map[string]workflow.NodePosition{})
	require.Error(t, err, "must not reach another user's private scope")
}

// ---------------------------------------------------------------------------
// ListFiles (service-level)
// ---------------------------------------------------------------------------

// Happy path: ListFiles returns files from the caller's private workflow.
func TestService_ListFiles_PrivateHit(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil)
	owner := uuid.New()
	require.NoError(t, repo.Create(context.Background(), workflow.Private(owner), "wf", wf("wf")))

	files, err := svc.ListFiles(ctxWith(owner, false), "wf")
	require.NoError(t, err)
	assert.NotNil(t, files)
}

// Success: ListFiles falls back to shared when the workflow is absent from private.
func TestService_ListFiles_SharedFallback(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil)
	owner := uuid.New()
	require.NoError(t, repo.Create(context.Background(), workflow.Shared(), "pub", wf("pub")))

	files, err := svc.ListFiles(ctxWith(owner, false), "pub")
	require.NoError(t, err)
	assert.NotNil(t, files)
}

// Failure: workflow absent from both scopes returns ErrNotFound.
func TestService_ListFiles_Nowhere_ReturnsNotFound(t *testing.T) {
	svc := workflow.NewWorkflowService(newFakeRepo(), nil)

	_, err := svc.ListFiles(ctxWith(uuid.New(), false), "ghost")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrNotFound)
}

// Error: no identity is rejected.
func TestService_ListFiles_NoIdentity_Rejected(t *testing.T) {
	svc := workflow.NewWorkflowService(newFakeRepo(), nil)

	_, err := svc.ListFiles(context.Background(), "wf")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrInvalidName)
}

// Edge: ListFiles does not expose another user's private files — it reads only
// the caller's private partition (or shared).
func TestService_ListFiles_DoesNotLeakOtherUsersPrivate(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil)
	a := uuid.New()
	b := uuid.New()
	require.NoError(t, repo.Create(context.Background(), workflow.Private(a), "secret", wf("secret")))

	// b does not own "secret" and it is not in shared — must get ErrNotFound.
	_, err := svc.ListFiles(ctxWith(b, false), "secret")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrNotFound)
}
