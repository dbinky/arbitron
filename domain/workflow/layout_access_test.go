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

// writeCountingRepo wraps the in-memory fakeRepo and counts every MUTATING
// repository call, so the layout seams can be proven side-effect free. Seeding
// through repo.bucket(...) bypasses the counter on purpose. loadErr, when set,
// is returned by Load for the PRIVATE scope only (an infrastructure failure).
type writeCountingRepo struct {
	*fakeRepo
	writes  int
	loadErr error
}

func newWriteCountingRepo() *writeCountingRepo {
	return &writeCountingRepo{fakeRepo: newFakeRepo()}
}

func (r *writeCountingRepo) Load(ctx context.Context, s workflow.Scope, name string) (*workflow.Workflow, error) {
	if r.loadErr != nil && s.Kind == workflow.ScopePrivate {
		return nil, r.loadErr
	}
	return r.fakeRepo.Load(ctx, s, name)
}

func (r *writeCountingRepo) CopyTreeStamped(ctx context.Context, src workflow.Scope, srcName string, dst workflow.Scope, dstName string, overwrite bool, stamp func(*workflow.Workflow) error) error {
	r.writes++
	return r.fakeRepo.CopyTreeStamped(ctx, src, srcName, dst, dstName, overwrite, stamp)
}

func (r *writeCountingRepo) Create(ctx context.Context, s workflow.Scope, name string, w *workflow.Workflow) error {
	r.writes++
	return r.fakeRepo.Create(ctx, s, name, w)
}

func (r *writeCountingRepo) Update(ctx context.Context, s workflow.Scope, name string, w *workflow.Workflow) error {
	r.writes++
	return r.fakeRepo.Update(ctx, s, name, w)
}

func (r *writeCountingRepo) Delete(ctx context.Context, s workflow.Scope, name string) error {
	r.writes++
	return r.fakeRepo.Delete(ctx, s, name)
}

func (r *writeCountingRepo) WriteFile(ctx context.Context, s workflow.Scope, name, rel string, content []byte) error {
	r.writes++
	return r.fakeRepo.WriteFile(ctx, s, name, rel, content)
}

func (r *writeCountingRepo) DeleteFile(ctx context.Context, s workflow.Scope, name, rel string) error {
	r.writes++
	return r.fakeRepo.DeleteFile(ctx, s, name, rel)
}

func (r *writeCountingRepo) CopyTree(ctx context.Context, src workflow.Scope, srcName string, dst workflow.Scope, dstName string, overwrite bool) error {
	r.writes++
	return r.fakeRepo.CopyTree(ctx, src, srcName, dst, dstName, overwrite)
}

var _ workflow.Repository = (*writeCountingRepo)(nil)

// layoutWF builds a stamped workflow carrying a saved revision.
func layoutWF(name, rev string) *workflow.Workflow {
	w := wf(name)
	w.Presentation = &workflow.Presentation{
		Revision: rev,
		Reason:   workflow.SaveReasonUpdate,
		Scope:    workflow.ScopeLabelPrivate,
		Name:     name,
		Retained: workflow.RetainedLayout{Source: workflow.RetainedNone},
	}
	return w
}

// ---------------------------------------------------------------------------
// LoadForLayout
// ---------------------------------------------------------------------------

// Happy path: an owner's private workflow resolves with the private scope.
func TestLoadForLayout_OwnerPrivate_ReturnsPrivateScope(t *testing.T) {
	repo := newWriteCountingRepo()
	owner := uuid.New()
	repo.bucket(workflow.Private(owner))["mine"] = layoutWF("mine", "rev-p")
	svc := workflow.NewWorkflowService(repo, newFakePublishers())

	got, scope, err := svc.LoadForLayout(ctxWith(owner, false), "mine")
	require.NoError(t, err)
	assert.Equal(t, "rev-p", got.SavedRevision())
	assert.Equal(t, workflow.Private(owner), scope)
}

// Success: a visible (public) shared workflow resolves with the shared scope.
func TestLoadForLayout_VisibleShared_ReturnsSharedScope(t *testing.T) {
	repo := newWriteCountingRepo()
	pubs := newFakePublishers()
	repo.bucket(workflow.Shared())["pub-wf"] = layoutWF("pub-wf", "rev-s")
	require.NoError(t, pubs.Upsert(context.Background(), "pub-wf", uuid.New(), "src", workflow.VisibilityPublic, ""))
	svc := workflow.NewWorkflowService(repo, pubs)

	got, scope, err := svc.LoadForLayout(ctxWith(uuid.New(), false), "pub-wf")
	require.NoError(t, err)
	assert.Equal(t, "rev-s", got.SavedRevision())
	assert.Equal(t, workflow.Shared(), scope)
}

// Error: a department workflow hidden from an outsider is ErrNotFound — the
// same concealment Load applies, never ErrSharedReadOnly or a revision.
func TestLoadForLayout_DepartmentHiddenShared_NotFound(t *testing.T) {
	repo := newWriteCountingRepo()
	pubs := newFakePublishers()
	repo.bucket(workflow.Shared())["eng-wf"] = layoutWF("eng-wf", "rev-eng")
	require.NoError(t, pubs.Upsert(context.Background(), "eng-wf", uuid.New(), "src", workflow.VisibilityDepartment, "engineering"))
	svc := workflow.NewWorkflowService(repo, pubs)

	got, _, err := svc.LoadForLayout(ctxWithDepts(uuid.New(), false, "sales"), "eng-wf")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrNotFound)
	assert.NotErrorIs(t, err, workflow.ErrSharedReadOnly)
	assert.Nil(t, got, "no definition (and so no revision) may leak to an outsider")

	// A department member sees the same workflow.
	got, scope, err := svc.LoadForLayout(ctxWithDepts(uuid.New(), false, "engineering"), "eng-wf")
	require.NoError(t, err)
	assert.Equal(t, "rev-eng", got.SavedRevision())
	assert.Equal(t, workflow.Shared(), scope)
}

// Edge: unlike Load, LoadForLayout never resolves/allocates a registry id for an
// id-less legacy definition — a polled layout read has no id side effects.
func TestLoadForLayout_DoesNotAllocateID(t *testing.T) {
	repo := newWriteCountingRepo()
	reg := newFakeRegistry()
	owner := uuid.New()
	repo.bucket(workflow.Private(owner))["legacy"] = layoutWF("legacy", "rev-l")
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetRegistry(reg)

	loaded, err := svc.Load(ctxWith(owner, false), "legacy")
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, loaded.ID, "precondition: Load allocates an in-memory id when a registry is wired")
	require.Equal(t, uuid.Nil, repo.bucket(workflow.Private(owner))["legacy"].ID)

	got, _, err := svc.LoadForLayout(ctxWith(owner, false), "legacy")
	require.NoError(t, err)
	assert.Equal(t, uuid.Nil, got.ID, "LoadForLayout must not allocate an id")
	assert.Empty(t, reg.byName, "no registry row may be written by a layout read")
	assert.Zero(t, repo.writes)
}

// Error: no identity is rejected as unauthenticated.
func TestLoadForLayout_NoIdentity_Unauthenticated(t *testing.T) {
	svc := workflow.NewWorkflowService(newWriteCountingRepo(), nil)
	_, _, err := svc.LoadForLayout(context.Background(), "x")
	assert.ErrorIs(t, err, identity.ErrUnauthenticated)
}

// Edge: a missing workflow is ErrNotFound and nothing is written.
func TestLoadForLayout_Missing_NotFoundWithoutWrites(t *testing.T) {
	repo := newWriteCountingRepo()
	svc := workflow.NewWorkflowService(repo, newFakePublishers())

	_, _, err := svc.LoadForLayout(ctxWith(uuid.New(), false), "ghost")
	assert.ErrorIs(t, err, workflow.ErrNotFound)
	assert.Zero(t, repo.writes)
}

// ---------------------------------------------------------------------------
// AuthorizeLayoutRetry
// ---------------------------------------------------------------------------

// Happy path: the owner of a private workflow may retry; scope is private.
func TestAuthorizeLayoutRetry_OwnerPrivate(t *testing.T) {
	repo := newWriteCountingRepo()
	owner := uuid.New()
	repo.bucket(workflow.Private(owner))["mine"] = layoutWF("mine", "rev-p")
	svc := workflow.NewWorkflowService(repo, nil)

	got, scope, err := svc.AuthorizeLayoutRetry(ctxWith(owner, false), "mine")
	require.NoError(t, err)
	assert.Equal(t, "rev-p", got.SavedRevision())
	assert.Equal(t, workflow.Private(owner), scope)
	assert.Zero(t, repo.writes, "authorizing a retry must not write the manifest")
}

// Success: an admin with no private copy may retry a shared-only workflow in
// place (Update's admin edit-in-place policy) and gets the shared scope.
func TestAuthorizeLayoutRetry_AdminSharedOnly_SharedScope(t *testing.T) {
	repo := newWriteCountingRepo()
	repo.bucket(workflow.Shared())["shared-wf"] = layoutWF("shared-wf", "rev-s")
	svc := workflow.NewWorkflowService(repo, nil)

	got, scope, err := svc.AuthorizeLayoutRetry(ctxWith(uuid.New(), true), "shared-wf")
	require.NoError(t, err)
	assert.Equal(t, "rev-s", got.SavedRevision())
	assert.Equal(t, workflow.Shared(), scope)
	assert.Zero(t, repo.writes)
}

// Edge: an admin who owns a private copy of the same name targets the private
// copy (the shared branch applies only when no private copy exists).
func TestAuthorizeLayoutRetry_AdminPrefersOwnPrivateCopy(t *testing.T) {
	repo := newWriteCountingRepo()
	admin := uuid.New()
	repo.bucket(workflow.Shared())["dup"] = layoutWF("dup", "rev-shared")
	repo.bucket(workflow.Private(admin))["dup"] = layoutWF("dup", "rev-private")
	svc := workflow.NewWorkflowService(repo, nil)

	got, scope, err := svc.AuthorizeLayoutRetry(ctxWith(admin, true), "dup")
	require.NoError(t, err)
	assert.Equal(t, "rev-private", got.SavedRevision())
	assert.Equal(t, workflow.Private(admin), scope)
}

// Failure: a non-admin cannot retry a shared-only workflow (read-only).
func TestAuthorizeLayoutRetry_NonAdminSharedOnly_SharedReadOnly(t *testing.T) {
	repo := newWriteCountingRepo()
	repo.bucket(workflow.Shared())["shared-wf"] = layoutWF("shared-wf", "rev-s")
	svc := workflow.NewWorkflowService(repo, nil)

	got, _, err := svc.AuthorizeLayoutRetry(ctxWith(uuid.New(), false), "shared-wf")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrSharedReadOnly)
	assert.Nil(t, got)
	assert.Zero(t, repo.writes)
}

// Failure: a workflow in neither scope is ErrNotFound.
func TestAuthorizeLayoutRetry_Missing_NotFound(t *testing.T) {
	repo := newWriteCountingRepo()
	svc := workflow.NewWorkflowService(repo, nil)

	_, _, err := svc.AuthorizeLayoutRetry(ctxWith(uuid.New(), true), "ghost")
	assert.ErrorIs(t, err, workflow.ErrNotFound)
}

// Edge: another user's private workflow is never reachable (not found).
func TestAuthorizeLayoutRetry_OtherUsersPrivate_NotFound(t *testing.T) {
	repo := newWriteCountingRepo()
	a, b := uuid.New(), uuid.New()
	repo.bucket(workflow.Private(a))["a-wf"] = layoutWF("a-wf", "rev-a")
	svc := workflow.NewWorkflowService(repo, nil)

	_, _, err := svc.AuthorizeLayoutRetry(ctxWith(b, false), "a-wf")
	assert.ErrorIs(t, err, workflow.ErrNotFound)
}

// Error: a private-scope infrastructure failure propagates verbatim (it is not
// collapsed into not-found and never falls through to the shared copy).
func TestAuthorizeLayoutRetry_PrivateLoadError_Propagates(t *testing.T) {
	repo := newWriteCountingRepo()
	repo.loadErr = errors.New("disk unreadable")
	repo.bucket(workflow.Shared())["shared-wf"] = layoutWF("shared-wf", "rev-s")
	svc := workflow.NewWorkflowService(repo, nil)

	_, _, err := svc.AuthorizeLayoutRetry(ctxWith(uuid.New(), true), "shared-wf")
	require.Error(t, err)
	assert.EqualError(t, err, "disk unreadable")
	assert.NotErrorIs(t, err, workflow.ErrNotFound)
}

// Error: no identity is rejected as unauthenticated.
func TestAuthorizeLayoutRetry_NoIdentity_Unauthenticated(t *testing.T) {
	svc := workflow.NewWorkflowService(newWriteCountingRepo(), nil)
	_, _, err := svc.AuthorizeLayoutRetry(context.Background(), "x")
	assert.ErrorIs(t, err, identity.ErrUnauthenticated)
}

// Policy parity: for every scenario AuthorizeLayoutRetry reaches the same
// allow/deny decision as Update (run against an independent repo), so the
// retry seam cannot drift from the write policy it mirrors.
func TestAuthorizeLayoutRetry_MatchesUpdatePolicy(t *testing.T) {
	owner := uuid.New()
	cases := []struct {
		name    string
		seed    func(r *writeCountingRepo)
		ctx     context.Context
		target  string
		wantErr error // nil = allowed
	}{
		{"owner private", func(r *writeCountingRepo) { r.bucket(workflow.Private(owner))["w"] = layoutWF("w", "r") }, ctxWith(owner, false), "w", nil},
		{"admin shared-only", func(r *writeCountingRepo) { r.bucket(workflow.Shared())["w"] = layoutWF("w", "r") }, ctxWith(owner, true), "w", nil},
		{"non-admin shared-only", func(r *writeCountingRepo) { r.bucket(workflow.Shared())["w"] = layoutWF("w", "r") }, ctxWith(owner, false), "w", workflow.ErrSharedReadOnly},
		{"missing", func(r *writeCountingRepo) {}, ctxWith(owner, true), "w", workflow.ErrNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			retryRepo := newWriteCountingRepo()
			tc.seed(retryRepo)
			_, _, retryErr := workflow.NewWorkflowService(retryRepo, nil).AuthorizeLayoutRetry(tc.ctx, tc.target)

			updateRepo := newWriteCountingRepo()
			tc.seed(updateRepo)
			updateErr := workflow.NewWorkflowService(updateRepo, nil).Update(tc.ctx, tc.target, layoutWF(tc.target, "r2"))

			if tc.wantErr == nil {
				assert.NoError(t, retryErr)
				assert.NoError(t, updateErr)
			} else {
				assert.ErrorIs(t, retryErr, tc.wantErr)
				assert.ErrorIs(t, updateErr, tc.wantErr)
			}
			assert.Zero(t, retryRepo.writes, "AuthorizeLayoutRetry must never write")
		})
	}
}

func TestLayoutRetryTargets_PrivateThenSharedForCaller(t *testing.T) {
	svc := workflow.NewWorkflowService(newWriteCountingRepo(), nil)
	user := uuid.New()
	got, err := svc.LayoutRetryTargets(ctxWith(user, false), "wf")
	require.NoError(t, err)
	assert.Equal(t, []workflow.MutationTarget{
		{Scope: workflow.Private(user), Name: "wf"},
		{Scope: workflow.Shared(), Name: "wf"},
	}, got)

	_, err = svc.LayoutRetryTargets(context.Background(), "wf")
	assert.Error(t, err, "no identity, no lock set")
}
