package workflow_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// ReadScopes
// ---------------------------------------------------------------------------

func TestReadScopes_ValidOwner(t *testing.T) {
	owner := uuid.New()
	scopes := workflow.ReadScopes(owner)
	require.Len(t, scopes, 2, "must return exactly two scopes")
	assert.Equal(t, workflow.ScopePrivate, scopes[0].Kind, "first scope must be Private")
	assert.Equal(t, owner, scopes[0].OwnerID)
	assert.Equal(t, workflow.ScopeShared, scopes[1].Kind, "second scope must be Shared")
}

func TestReadScopes_PrivateAlwaysFirst(t *testing.T) {
	owner := uuid.New()
	scopes := workflow.ReadScopes(owner)
	assert.Equal(t, workflow.Private(owner), scopes[0], "Private scope must precede Shared to enforce private-first reads")
	assert.Equal(t, workflow.Shared(), scopes[1])
}

// ---------------------------------------------------------------------------
// LoadVisible
// ---------------------------------------------------------------------------

// Happy path: workflow exists in the owner's private scope — returns it and
// the Private scope identifier.
func TestLoadVisible_PrivateHit(t *testing.T) {
	repo := newFakeRepo()
	owner := uuid.New()
	w := wf("alpha")
	require.NoError(t, repo.Create(context.Background(), workflow.Private(owner), "alpha", w))

	got, scope, err := workflow.LoadVisible(context.Background(), repo, owner, "alpha")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "alpha", got.Name)
	assert.Equal(t, workflow.ScopePrivate, scope.Kind, "must report Private scope on private hit")
	assert.Equal(t, owner, scope.OwnerID)
}

// Success: workflow absent from private but present in shared — falls back and
// reports Shared scope.
func TestLoadVisible_SharedFallback(t *testing.T) {
	repo := newFakeRepo()
	owner := uuid.New()
	shared := wf("beta")
	require.NoError(t, repo.Create(context.Background(), workflow.Shared(), "beta", shared))

	got, scope, err := workflow.LoadVisible(context.Background(), repo, owner, "beta")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "beta", got.Name)
	assert.Equal(t, workflow.ScopeShared, scope.Kind, "must fall back to Shared when private misses")
}

// Failure: workflow absent from both scopes — returns ErrNotFound.
func TestLoadVisible_NeitherFound(t *testing.T) {
	repo := newFakeRepo()
	owner := uuid.New()

	_, _, err := workflow.LoadVisible(context.Background(), repo, owner, "ghost")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrNotFound)
}

// Error: a non-ErrNotFound error from the private probe aborts immediately
// without checking the shared scope. This ensures storage errors propagate
// rather than silently masking as "not found".
func TestLoadVisible_NonNotFoundErrorAbortsEarly(t *testing.T) {
	boom := errors.New("storage exploded")
	repo := &errOnPrivateLoadRepo{err: boom}

	_, _, err := workflow.LoadVisible(context.Background(), repo, uuid.New(), "any")
	require.Error(t, err)
	assert.ErrorIs(t, err, boom, "non-ErrNotFound error must propagate unchanged")
	assert.False(t, repo.sharedQueried, "shared scope must not be queried after a non-ErrNotFound error")
}

// Edge: private and shared both have the same name — private wins.
func TestLoadVisible_PrivatePrecedesShared(t *testing.T) {
	repo := newFakeRepo()
	owner := uuid.New()
	pvt := &workflow.Workflow{Name: "clash", Description: "private-copy"}
	shrd := &workflow.Workflow{Name: "clash", Description: "shared-copy"}
	require.NoError(t, repo.Create(context.Background(), workflow.Private(owner), "clash", pvt))
	require.NoError(t, repo.Create(context.Background(), workflow.Shared(), "clash", shrd))

	got, scope, err := workflow.LoadVisible(context.Background(), repo, owner, "clash")
	require.NoError(t, err)
	assert.Equal(t, "private-copy", got.Description, "private copy must win when both scopes have the name")
	assert.Equal(t, workflow.ScopePrivate, scope.Kind)
}

// ---------------------------------------------------------------------------
// ListFilesVisible
// ---------------------------------------------------------------------------

// Happy path: files exist in the owner's private scope.
func TestListFilesVisible_PrivateHit(t *testing.T) {
	repo := newFakeFilesRepo()
	owner := uuid.New()
	repo.addFiles(workflow.Private(owner), "wf", []string{"run.sh", "prompt.md"})

	files, scope, err := workflow.ListFilesVisible(context.Background(), repo, owner, "wf")
	require.NoError(t, err)
	assert.Equal(t, workflow.ScopePrivate, scope.Kind, "must report Private scope when found privately")
	assert.Equal(t, owner, scope.OwnerID)
	assert.ElementsMatch(t, []string{"run.sh", "prompt.md"}, files)
}

// Success: files absent from private but present in shared.
func TestListFilesVisible_SharedFallback(t *testing.T) {
	repo := newFakeFilesRepo()
	owner := uuid.New()
	repo.addFiles(workflow.Shared(), "pub-wf", []string{"shared.sh"})

	files, scope, err := workflow.ListFilesVisible(context.Background(), repo, owner, "pub-wf")
	require.NoError(t, err)
	assert.Equal(t, workflow.ScopeShared, scope.Kind, "must fall back to Shared scope")
	assert.ElementsMatch(t, []string{"shared.sh"}, files)
}

// Failure: workflow absent from both scopes — returns ErrNotFound.
func TestListFilesVisible_NeitherFound(t *testing.T) {
	repo := newFakeFilesRepo()
	owner := uuid.New()

	_, _, err := workflow.ListFilesVisible(context.Background(), repo, owner, "ghost")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrNotFound)
}

// Error: non-ErrNotFound from the private probe aborts without querying shared.
func TestListFilesVisible_NonNotFoundErrorAbortsEarly(t *testing.T) {
	boom := errors.New("disk failure")
	repo := &errOnPrivateListFilesRepo{err: boom}

	_, _, err := workflow.ListFilesVisible(context.Background(), repo, uuid.New(), "any")
	require.Error(t, err)
	assert.ErrorIs(t, err, boom)
	assert.False(t, repo.sharedQueried, "shared scope must not be reached after a non-ErrNotFound error")
}

// Edge: private and shared both have the same workflow name — private wins.
func TestListFilesVisible_PrivatePrecedesShared(t *testing.T) {
	repo := newFakeFilesRepo()
	owner := uuid.New()
	repo.addFiles(workflow.Private(owner), "dup", []string{"private.sh"})
	repo.addFiles(workflow.Shared(), "dup", []string{"shared.sh"})

	files, scope, err := workflow.ListFilesVisible(context.Background(), repo, owner, "dup")
	require.NoError(t, err)
	assert.Equal(t, workflow.ScopePrivate, scope.Kind, "private must win when both have the workflow")
	assert.ElementsMatch(t, []string{"private.sh"}, files)
}

// ---------------------------------------------------------------------------
// Helpers: minimal fakes for error-injection tests
// ---------------------------------------------------------------------------

// errOnPrivateLoadRepo causes Load to fail with a non-ErrNotFound error for
// the Private scope and tracks whether the Shared scope was ever queried.
type errOnPrivateLoadRepo struct {
	baseRepo
	err           error
	sharedQueried bool
}

func (r *errOnPrivateLoadRepo) Load(_ context.Context, s workflow.Scope, _ string) (*workflow.Workflow, error) {
	if s.Kind == workflow.ScopePrivate {
		return nil, r.err
	}
	r.sharedQueried = true
	return nil, workflow.ErrNotFound
}

// errOnPrivateListFilesRepo causes ListFiles to fail with a non-ErrNotFound
// error for the Private scope.
type errOnPrivateListFilesRepo struct {
	baseRepo
	err           error
	sharedQueried bool
}

func (r *errOnPrivateListFilesRepo) ListFiles(_ context.Context, s workflow.Scope, _ string) ([]string, error) {
	if s.Kind == workflow.ScopePrivate {
		return nil, r.err
	}
	r.sharedQueried = true
	return nil, workflow.ErrNotFound
}

// baseRepo is a no-op implementation of Repository embedded by error
// injection fakes so they only need to override the one method under test.
type baseRepo struct{}

func (baseRepo) Load(_ context.Context, _ workflow.Scope, _ string) (*workflow.Workflow, error) {
	return nil, workflow.ErrNotFound
}
func (baseRepo) List(_ context.Context, _ workflow.Scope) ([]workflow.WorkflowSummary, error) {
	return nil, nil
}
func (baseRepo) Create(_ context.Context, _ workflow.Scope, _ string, _ *workflow.Workflow) error {
	return nil
}
func (baseRepo) Update(_ context.Context, _ workflow.Scope, _ string, _ *workflow.Workflow) error {
	return workflow.ErrNotFound
}
func (baseRepo) Delete(_ context.Context, _ workflow.Scope, _ string) error {
	return workflow.ErrNotFound
}
func (baseRepo) WriteFile(_ context.Context, _ workflow.Scope, _ string, _ string, _ []byte) error {
	return nil
}
func (baseRepo) DeleteFile(_ context.Context, _ workflow.Scope, _ string, _ string) error {
	return nil
}
func (baseRepo) ListFiles(_ context.Context, _ workflow.Scope, _ string) ([]string, error) {
	return nil, workflow.ErrNotFound
}
func (baseRepo) Exists(_ context.Context, _ workflow.Scope, _ string) (bool, error) {
	return false, nil
}
func (baseRepo) CopyTree(_ context.Context, _ workflow.Scope, _ string, _ workflow.Scope, _ string, _ bool) error {
	return nil
}

// fakeFilesRepo is a minimal in-memory repo for ListFilesVisible tests that
// stores per-scope file lists.
type fakeFilesRepo struct {
	baseRepo
	files map[string][]string // scopeKey+":"+workflowName → file list
}

func newFakeFilesRepo() *fakeFilesRepo {
	return &fakeFilesRepo{files: map[string][]string{}}
}

func (r *fakeFilesRepo) addFiles(s workflow.Scope, name string, f []string) {
	r.files[scopeKey(s)+":"+name] = f
}

func (r *fakeFilesRepo) ListFiles(_ context.Context, s workflow.Scope, name string) ([]string, error) {
	if f, ok := r.files[scopeKey(s)+":"+name]; ok {
		return f, nil
	}
	return nil, workflow.ErrNotFound
}

func (r baseRepo) CopyTreeStamped(ctx context.Context, src workflow.Scope, sn string, dst workflow.Scope, dn string, overwrite bool, stamp func(*workflow.Workflow) error) error {
	return workflow.ErrNotFound
}
