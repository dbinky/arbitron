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
	pub map[string]workflow.Publication
	// upsertErr, when set, makes Upsert fail so the Publish compensation paths are
	// observable.
	upsertErr error
}

func newFakePublishers() *fakePublishers {
	return &fakePublishers{pub: map[string]workflow.Publication{}}
}

func (p *fakePublishers) PublisherOf(_ context.Context, name string) (uuid.UUID, bool, error) {
	pub, ok := p.pub[name]
	return pub.Publisher, ok, nil
}

func (p *fakePublishers) PublicationOf(_ context.Context, name string) (workflow.Publication, bool, error) {
	pub, ok := p.pub[name]
	return pub, ok, nil
}

func (p *fakePublishers) Upsert(_ context.Context, name string, publisher uuid.UUID, sourceName string, visibility workflow.Visibility, department string) error {
	if p.upsertErr != nil {
		return p.upsertErr
	}
	p.pub[name] = workflow.Publication{Publisher: publisher, SourceName: sourceName, Visibility: visibility, Department: department}
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

// ctxWithDepts builds a context identity carrying departments.
func ctxWithDepts(user uuid.UUID, admin bool, depts ...string) context.Context {
	role := identity.RoleUser
	if admin {
		role = identity.RoleAdmin
	}
	return identity.WithIdentity(context.Background(),
		identity.Identity{UserID: user, Username: "u", Role: role, Departments: depts})
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
	pubs := &fakePublishers{pub: map[string]workflow.Publication{"sh": {Publisher: pubID, Visibility: workflow.VisibilityPublic}}}
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
	assert.ErrorIs(t, err, identity.ErrUnauthenticated)

	_, err = svc.Load(context.Background(), "w")
	require.Error(t, err)
	assert.ErrorIs(t, err, identity.ErrUnauthenticated)
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

	require.NoError(t, svc.Publish(ctxWith(owner, true), "src", "pub", workflow.VisibilityPublic, ""))

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

	err := svc.Publish(ctxWith(owner, true), "ghost", "pub", workflow.VisibilityPublic, "")
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
	pubs.pub["pub"] = workflow.Publication{Publisher: a, Visibility: workflow.VisibilityPublic}

	// b owns a private "src" and tries to publish over a's name.
	require.NoError(t, repo.Create(context.Background(), workflow.Private(b), "src", wf("src")))
	err := svc.Publish(ctxWith(b, true), "src", "pub", workflow.VisibilityPublic, "")
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
	require.NoError(t, svc.Publish(ctxWith(owner, true), "src", "pub", workflow.VisibilityPublic, ""))
	// Re-publish by the same owner succeeds (overwrite).
	require.NoError(t, svc.Publish(ctxWith(owner, true), "src", "pub", workflow.VisibilityPublic, ""))
}

// TestService_Publish_SamePublisher_SharedContentUpdated pins the re-publish
// overwrite: when the same publisher publishes a modified source, the shared
// workspace must reflect the new content — not the original version.
func TestService_Publish_SamePublisher_SharedContentUpdated(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	svc := workflow.NewWorkflowService(repo, pubs)
	owner := uuid.New()

	// V1: initial publish.
	v1 := &workflow.Workflow{Name: "src", Description: "v1", Author: "a"}
	require.NoError(t, repo.Create(context.Background(), workflow.Private(owner), "src", v1))
	require.NoError(t, svc.Publish(ctxWith(owner, true), "src", "pub", workflow.VisibilityPublic, ""))

	// Confirm v1 is in shared.
	got, ok := repo.bucket(workflow.Shared())["pub"]
	require.True(t, ok)
	assert.Equal(t, "v1", got.Description, "initial publish must write v1 content")

	// V2: overwrite private source, then re-publish.
	v2 := &workflow.Workflow{Name: "src", Description: "v2", Author: "a"}
	repo.bucket(workflow.Private(owner))["src"] = v2
	require.NoError(t, svc.Publish(ctxWith(owner, true), "src", "pub", workflow.VisibilityPublic, ""))

	// Shared must now reflect v2.
	got, ok = repo.bucket(workflow.Shared())["pub"]
	require.True(t, ok)
	assert.Equal(t, "v2", got.Description,
		"re-publish by same publisher must overwrite shared workspace with new content")
}

func TestService_Publish_NoStore_Errors(t *testing.T) {
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil) // no published store
	owner := uuid.New()
	require.NoError(t, repo.Create(context.Background(), workflow.Private(owner), "src", wf("src")))

	err := svc.Publish(ctxWith(owner, true), "src", "pub", workflow.VisibilityPublic, "")
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
	assert.ErrorIs(t, err, identity.ErrUnauthenticated)
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
	assert.ErrorIs(t, err, identity.ErrUnauthenticated)
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
	assert.ErrorIs(t, err, identity.ErrUnauthenticated)
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
	assert.ErrorIs(t, err, identity.ErrUnauthenticated)
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
	assert.ErrorIs(t, err, identity.ErrUnauthenticated)
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

// --- Publish compensation (copy-then-record ordering invariant) ---

// Happy path: a fresh publish copies the private tree into shared and records
// the publisher.
func TestService_Publish_Fresh_CopiesAndRecords(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	svc := workflow.NewWorkflowService(repo, pubs)
	owner := uuid.New()
	require.NoError(t, repo.Create(context.Background(), workflow.Private(owner), "mine", wf("mine")))

	require.NoError(t, svc.Publish(ctxWith(owner, true), "mine", "shared-name", workflow.VisibilityPublic, ""))

	exists, _ := repo.Exists(context.Background(), workflow.Shared(), "shared-name")
	assert.True(t, exists, "shared tree present after publish")
	pubID, ok, _ := pubs.PublisherOf(context.Background(), "shared-name")
	require.True(t, ok)
	assert.Equal(t, owner, pubID)
}

// Failure (fresh path): when recording the publication (Upsert) fails on a fresh
// publish, the just-copied shared tree must be compensated (deleted) so no
// orphaned, unattributed shared copy is left behind.
func TestService_Publish_FreshUpsertFailure_RollsBackCopiedTree(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	pubs.upsertErr = errors.New("db write failed")
	svc := workflow.NewWorkflowService(repo, pubs)
	owner := uuid.New()
	require.NoError(t, repo.Create(context.Background(), workflow.Private(owner), "mine", wf("mine")))

	err := svc.Publish(ctxWith(owner, true), "mine", "shared-name", workflow.VisibilityPublic, "")
	require.Error(t, err, "Upsert failure must surface")
	assert.ErrorIs(t, err, pubs.upsertErr)

	exists, _ := repo.Exists(context.Background(), workflow.Shared(), "shared-name")
	assert.False(t, exists, "fresh-publish Upsert failure must roll back (delete) the copied shared tree")
}

// Failure (overwrite path): when recording the publication (Upsert) fails on a
// re-publish by the same publisher, the error must surface loudly and the
// previously-good shared copy must NOT be deleted (no data loss).
func TestService_Publish_OverwriteUpsertFailure_DoesNotDelete(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	svc := workflow.NewWorkflowService(repo, pubs)
	owner := uuid.New()
	require.NoError(t, repo.Create(context.Background(), workflow.Private(owner), "mine", wf("mine")))

	// First publish succeeds (records owner as publisher).
	require.NoError(t, svc.Publish(ctxWith(owner, true), "mine", "shared-name", workflow.VisibilityPublic, ""))

	// Re-publish by the same owner, but Upsert now fails.
	pubs.upsertErr = errors.New("db write failed")
	err := svc.Publish(ctxWith(owner, true), "mine", "shared-name", workflow.VisibilityPublic, "")
	require.Error(t, err, "overwrite Upsert failure must surface loudly")
	assert.ErrorIs(t, err, pubs.upsertErr)

	exists, _ := repo.Exists(context.Background(), workflow.Shared(), "shared-name")
	assert.True(t, exists, "overwrite-publish Upsert failure must NOT delete the previously-good shared copy")
}

// --- Phase 2: visibility gating (Task 3) ---

func TestService_List_FiltersByVisibility(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	publisher := uuid.MustParse("00000000-0000-0000-0000-0000000000b1")
	viewer := uuid.MustParse("00000000-0000-0000-0000-0000000000b2")

	repo.bucket(workflow.Shared())["pub-wf"] = wf("pub-wf")
	repo.bucket(workflow.Shared())["eng-wf"] = wf("eng-wf")
	repo.bucket(workflow.Shared())["sales-wf"] = wf("sales-wf")
	_ = pubs.Upsert(context.Background(), "pub-wf", publisher, "src", workflow.VisibilityPublic, "")
	_ = pubs.Upsert(context.Background(), "eng-wf", publisher, "src", workflow.VisibilityDepartment, "engineering")
	_ = pubs.Upsert(context.Background(), "sales-wf", publisher, "src", workflow.VisibilityDepartment, "sales")

	svc := workflow.NewWorkflowService(repo, pubs)

	got, err := svc.List(ctxWithDepts(viewer, false, "engineering"))
	require.NoError(t, err)
	names := map[string]bool{}
	for _, s := range got {
		names[s.Name] = true
	}
	assert.True(t, names["pub-wf"])
	assert.True(t, names["eng-wf"])
	assert.False(t, names["sales-wf"])
}

func TestService_List_CarriesVisibilityAndDepartment(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	publisher := uuid.MustParse("00000000-0000-0000-0000-0000000000b3")
	repo.bucket(workflow.Shared())["eng-wf"] = wf("eng-wf")
	_ = pubs.Upsert(context.Background(), "eng-wf", publisher, "src", workflow.VisibilityDepartment, "engineering")

	svc := workflow.NewWorkflowService(repo, pubs)
	got, err := svc.List(ctxWithDepts(publisher, false, "engineering"))
	require.NoError(t, err)

	var eng *workflow.WorkflowSummary
	for i := range got {
		if got[i].Name == "eng-wf" {
			eng = &got[i]
		}
	}
	require.NotNil(t, eng)
	assert.Equal(t, string(workflow.VisibilityDepartment), eng.Visibility)
	assert.Equal(t, "engineering", eng.Department)
}

func TestService_Load_DeptWorkflow_HiddenFromOutsider_NotFound(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	publisher := uuid.MustParse("00000000-0000-0000-0000-0000000000c1")
	outsider := uuid.MustParse("00000000-0000-0000-0000-0000000000c2")
	repo.bucket(workflow.Shared())["eng-wf"] = wf("eng-wf")
	_ = pubs.Upsert(context.Background(), "eng-wf", publisher, "src", workflow.VisibilityDepartment, "engineering")
	svc := workflow.NewWorkflowService(repo, pubs)

	_, err := svc.Load(ctxWithDepts(outsider, false, "sales"), "eng-wf")
	assert.ErrorIs(t, err, workflow.ErrNotFound)
}

func TestService_Load_DeptWorkflow_VisibleToMember(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	publisher := uuid.MustParse("00000000-0000-0000-0000-0000000000c3")
	member := uuid.MustParse("00000000-0000-0000-0000-0000000000c4")
	repo.bucket(workflow.Shared())["eng-wf"] = wf("eng-wf")
	_ = pubs.Upsert(context.Background(), "eng-wf", publisher, "src", workflow.VisibilityDepartment, "engineering")
	svc := workflow.NewWorkflowService(repo, pubs)

	got, err := svc.Load(ctxWithDepts(member, false, "engineering"), "eng-wf")
	require.NoError(t, err)
	assert.Equal(t, "eng-wf", got.Name)
}

func TestService_Copy_UnseeableDept_NotFound(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	publisher := uuid.MustParse("00000000-0000-0000-0000-0000000000c5")
	outsider := uuid.MustParse("00000000-0000-0000-0000-0000000000c6")
	repo.bucket(workflow.Shared())["eng-wf"] = wf("eng-wf")
	_ = pubs.Upsert(context.Background(), "eng-wf", publisher, "src", workflow.VisibilityDepartment, "engineering")
	svc := workflow.NewWorkflowService(repo, pubs)

	err := svc.Copy(ctxWithDepts(outsider, false, "sales"), "eng-wf", "my-copy")
	assert.ErrorIs(t, err, workflow.ErrNotFound)
}

func TestService_Load_PrivateUnaffected(t *testing.T) {
	repo := newFakeRepo()
	owner := uuid.MustParse("00000000-0000-0000-0000-0000000000c7")
	repo.bucket(workflow.Private(owner))["mine"] = wf("mine")
	svc := workflow.NewWorkflowService(repo, newFakePublishers())

	got, err := svc.Load(ctxWithDepts(owner, false), "mine")
	require.NoError(t, err)
	assert.Equal(t, "mine", got.Name)
}

// --- Phase 2: role-gated publish + SetVisibility (Task 4) ---

// ctxWithRoles builds a context identity with departments AND led departments.
func ctxWithRoles(user uuid.UUID, admin bool, depts, led []string) context.Context {
	role := identity.RoleUser
	if admin {
		role = identity.RoleAdmin
	}
	return identity.WithIdentity(context.Background(), identity.Identity{
		UserID: user, Username: "u", Role: role,
		Departments: depts, LedDepartments: led,
	})
}

func seedPrivate(repo *fakeRepo, owner uuid.UUID, name string) {
	repo.bucket(workflow.Private(owner))[name] = wf(name)
}

func TestService_Publish_MemberToOwnDepartment_OK(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	owner := uuid.MustParse("00000000-0000-0000-0000-0000000000d1")
	seedPrivate(repo, owner, "src")
	svc := workflow.NewWorkflowService(repo, pubs)

	err := svc.Publish(ctxWithRoles(owner, false, []string{"engineering"}, nil), "src", "shared", workflow.VisibilityDepartment, "engineering")
	require.NoError(t, err)

	pub, ok, _ := pubs.PublicationOf(context.Background(), "shared")
	require.True(t, ok)
	assert.Equal(t, workflow.VisibilityDepartment, pub.Visibility)
	assert.Equal(t, "engineering", pub.Department)
}

func TestService_Publish_MemberToOtherDepartment_Rejected(t *testing.T) {
	repo := newFakeRepo()
	owner := uuid.MustParse("00000000-0000-0000-0000-0000000000d2")
	seedPrivate(repo, owner, "src")
	svc := workflow.NewWorkflowService(repo, newFakePublishers())

	err := svc.Publish(ctxWithRoles(owner, false, []string{"engineering"}, nil), "src", "shared", workflow.VisibilityDepartment, "sales")
	assert.ErrorIs(t, err, workflow.ErrUnauthorized)
}

func TestService_Publish_MemberToPublic_Rejected(t *testing.T) {
	repo := newFakeRepo()
	owner := uuid.MustParse("00000000-0000-0000-0000-0000000000d3")
	seedPrivate(repo, owner, "src")
	svc := workflow.NewWorkflowService(repo, newFakePublishers())

	err := svc.Publish(ctxWithRoles(owner, false, []string{"engineering"}, nil), "src", "shared", workflow.VisibilityPublic, "")
	assert.ErrorIs(t, err, workflow.ErrUnauthorized)
}

func TestService_Publish_LeadToPublic_OK(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	owner := uuid.MustParse("00000000-0000-0000-0000-0000000000d4")
	seedPrivate(repo, owner, "src")
	svc := workflow.NewWorkflowService(repo, pubs)

	err := svc.Publish(ctxWithRoles(owner, false, []string{"engineering"}, []string{"engineering"}), "src", "shared", workflow.VisibilityPublic, "")
	require.NoError(t, err)
	pub, ok, _ := pubs.PublicationOf(context.Background(), "shared")
	require.True(t, ok)
	assert.Equal(t, workflow.VisibilityPublic, pub.Visibility)
}

func TestService_Publish_AdminToPublic_OK(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	owner := uuid.MustParse("00000000-0000-0000-0000-0000000000d5")
	seedPrivate(repo, owner, "src")
	svc := workflow.NewWorkflowService(repo, pubs)

	err := svc.Publish(ctxWithRoles(owner, true, nil, nil), "src", "shared", workflow.VisibilityPublic, "")
	require.NoError(t, err)
}

func TestService_Publish_DepartmentLevelMissingDepartment_Rejected(t *testing.T) {
	repo := newFakeRepo()
	owner := uuid.MustParse("00000000-0000-0000-0000-0000000000d6")
	seedPrivate(repo, owner, "src")
	svc := workflow.NewWorkflowService(repo, newFakePublishers())

	err := svc.Publish(ctxWithRoles(owner, false, []string{"engineering"}, nil), "src", "shared", workflow.VisibilityDepartment, "")
	assert.ErrorIs(t, err, workflow.ErrInvalidVisibility)
}

func TestService_Publish_PrivateVisibility_Rejected(t *testing.T) {
	repo := newFakeRepo()
	owner := uuid.MustParse("00000000-0000-0000-0000-0000000000d7")
	seedPrivate(repo, owner, "src")
	svc := workflow.NewWorkflowService(repo, newFakePublishers())

	err := svc.Publish(ctxWithRoles(owner, false, []string{"engineering"}, nil), "src", "shared", workflow.VisibilityPrivate, "")
	assert.ErrorIs(t, err, workflow.ErrInvalidVisibility)
}

func TestService_SetVisibility_LeadPromotesDeptToPublic(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	owner := uuid.MustParse("00000000-0000-0000-0000-0000000000e1")
	_ = pubs.Upsert(context.Background(), "shared", owner, "src", workflow.VisibilityDepartment, "engineering")
	repo.bucket(workflow.Shared())["shared"] = wf("shared")
	svc := workflow.NewWorkflowService(repo, pubs)

	err := svc.SetVisibility(ctxWithRoles(owner, false, []string{"engineering"}, []string{"engineering"}), "shared", workflow.VisibilityPublic, "")
	require.NoError(t, err)
	pub, _, _ := pubs.PublicationOf(context.Background(), "shared")
	assert.Equal(t, workflow.VisibilityPublic, pub.Visibility)
}

func TestService_SetVisibility_MemberCannotPromoteToPublic(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	owner := uuid.MustParse("00000000-0000-0000-0000-0000000000e2")
	_ = pubs.Upsert(context.Background(), "shared", owner, "src", workflow.VisibilityDepartment, "engineering")
	svc := workflow.NewWorkflowService(repo, pubs)

	err := svc.SetVisibility(ctxWithRoles(owner, false, []string{"engineering"}, nil), "shared", workflow.VisibilityPublic, "")
	assert.ErrorIs(t, err, workflow.ErrUnauthorized)
}

func TestService_SetVisibility_DemotePublicToDepartment(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	owner := uuid.MustParse("00000000-0000-0000-0000-0000000000e3")
	_ = pubs.Upsert(context.Background(), "shared", owner, "src", workflow.VisibilityPublic, "")
	svc := workflow.NewWorkflowService(repo, pubs)

	err := svc.SetVisibility(ctxWithRoles(owner, false, []string{"engineering"}, nil), "shared", workflow.VisibilityDepartment, "engineering")
	require.NoError(t, err)
	pub, _, _ := pubs.PublicationOf(context.Background(), "shared")
	assert.Equal(t, workflow.VisibilityDepartment, pub.Visibility)
	assert.Equal(t, "engineering", pub.Department)
	assert.Equal(t, owner, pub.Publisher, "publisher preserved through the visibility flip")
}

func TestService_SetVisibility_UnknownPublication_NotFound(t *testing.T) {
	svc := workflow.NewWorkflowService(newFakeRepo(), newFakePublishers())
	owner := uuid.MustParse("00000000-0000-0000-0000-0000000000e4")
	err := svc.SetVisibility(ctxWithRoles(owner, false, []string{"engineering"}, nil), "missing", workflow.VisibilityDepartment, "engineering")
	assert.ErrorIs(t, err, workflow.ErrNotFound)
}

func TestService_SetVisibility_DifferentPublisher_Unauthorized(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	publisher := uuid.MustParse("00000000-0000-0000-0000-0000000000e5")
	other := uuid.MustParse("00000000-0000-0000-0000-0000000000e6")
	_ = pubs.Upsert(context.Background(), "shared", publisher, "src", workflow.VisibilityDepartment, "engineering")
	svc := workflow.NewWorkflowService(repo, pubs)

	err := svc.SetVisibility(ctxWithRoles(other, false, []string{"engineering"}, nil), "shared", workflow.VisibilityDepartment, "engineering")
	assert.ErrorIs(t, err, workflow.ErrUnauthorized)
}
