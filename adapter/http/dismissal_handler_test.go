package http_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/eventbus"
	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockUserRepo implements instance.UserRepository.
type mockUserRepo struct{ id uuid.UUID }

func (m *mockUserRepo) GetDefaultUser(_ context.Context) (uuid.UUID, error) { return m.id, nil }

func TestCurrentUser_ResolvesDefaultUser(t *testing.T) {
	want := uuid.New()
	resolver := adapthttp.NewCurrentUserResolver(&mockUserRepo{id: want})
	r := httptest.NewRequest("GET", "/", nil)

	got, err := resolver.CurrentUser(r)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// failClosedResolver is a fake CurrentUserResolver that always fails to resolve
// a principal. It models the Phase-6 fail-closed contract: when no
// authenticated user is on the request, the resolver returns an error rather
// than silently falling back to a default user.
type failClosedResolver struct{ err error }

func (f *failClosedResolver) CurrentUser(_ *http.Request) (uuid.UUID, error) {
	return uuid.UUID{}, f.err
}

var _ adapthttp.CurrentUserResolver = (*failClosedResolver)(nil)

// recordingResolver is a fake CurrentUserResolver that returns a fixed acting
// user, proving the handler resolves the acting user through the injected seam
// and not through any concrete resolver.
type recordingResolver struct{ id uuid.UUID }

func (r *recordingResolver) CurrentUser(_ *http.Request) (uuid.UUID, error) { return r.id, nil }

var _ adapthttp.CurrentUserResolver = (*recordingResolver)(nil)

// Security (HIGH): the acting-user seam MUST fail closed. When the injected
// CurrentUserResolver cannot resolve a principal (the Phase-6 unauthenticated
// case), Dismiss must surface a 500 and MUST NOT record any dismissal — there
// is no silent default-user fallback. This is the contract the entire per-user
// authorization model rests on, pinned here via a fake resolver injected
// through the CurrentUserResolver interface.
func TestDismiss_ResolverFailsClosed_NoDefaultUserFallback(t *testing.T) {
	instRepo := newMockInstanceRepo()
	id := uuid.New()
	require.NoError(t, instRepo.Create(context.Background(), &instance.Instance{
		ID: id, OwnerID: uuid.New(), WorkflowName: "wf", State: instance.StateFailed,
	}))
	dis := newMockDismissalRepo()

	h := adapthttp.NewInstanceHandler(instRepo, newMockNodeExecRepo(), newMockSnapshotRepo(), &routerMockService{})
	h.SetDismissals(dis, &failClosedResolver{err: fmt.Errorf("no principal on request")})

	router := chi.NewRouter()
	router.Post("/api/v1/instances/{id}/dismiss", h.Dismiss)
	srv := httptest.NewServer(router)
	defer srv.Close()

	resp := doPOST(t, srv.URL+"/api/v1/instances/"+id.String()+"/dismiss")
	defer resp.Body.Close()

	assert.Equal(t, 500, resp.StatusCode, "unresolved acting user must fail closed, not fall back")
	for u := range dis.dismissed {
		assert.Empty(t, dis.dismissed[u], "no dismissal may be recorded for any user when the resolver fails closed")
	}
}

// Seam: the handler resolves the acting user through the injected
// CurrentUserResolver interface (not a concrete resolver). A fake resolver
// returning a fixed user must drive which per-user row the dismissal lands on.
func TestDismiss_ResolvesActingUserThroughInjectedSeam(t *testing.T) {
	actor := uuid.New()
	instRepo := newMockInstanceRepo()
	id := uuid.New()
	require.NoError(t, instRepo.Create(context.Background(), &instance.Instance{
		ID: id, OwnerID: uuid.New(), WorkflowName: "wf", State: instance.StateFailed,
	}))
	dis := newMockDismissalRepo()

	h := adapthttp.NewInstanceHandler(instRepo, newMockNodeExecRepo(), newMockSnapshotRepo(), &routerMockService{})
	h.SetDismissals(dis, &recordingResolver{id: actor})

	router := chi.NewRouter()
	router.Post("/api/v1/instances/{id}/dismiss", h.Dismiss)
	srv := httptest.NewServer(router)
	defer srv.Close()

	resp := doPOST(t, srv.URL+"/api/v1/instances/"+id.String()+"/dismiss")
	defer resp.Body.Close()

	assert.Equal(t, 200, resp.StatusCode)
	assert.True(t, dis.dismissed[actor][id], "dismissal must land on the acting user from the injected seam")
}

// mockDismissalRepo implements instance.DismissalRepository in memory.
type mockDismissalRepo struct {
	// dismissed[userID][instanceID] = true
	dismissed map[uuid.UUID]map[uuid.UUID]bool
}

func newMockDismissalRepo() *mockDismissalRepo {
	return &mockDismissalRepo{dismissed: map[uuid.UUID]map[uuid.UUID]bool{}}
}
func (m *mockDismissalRepo) Dismiss(_ context.Context, u, i uuid.UUID) error {
	if m.dismissed[u] == nil {
		m.dismissed[u] = map[uuid.UUID]bool{}
	}
	m.dismissed[u][i] = true
	return nil
}
func (m *mockDismissalRepo) DismissMany(_ context.Context, u uuid.UUID, ids []uuid.UUID) error {
	if m.dismissed[u] == nil {
		m.dismissed[u] = map[uuid.UUID]bool{}
	}
	for _, i := range ids {
		m.dismissed[u][i] = true
	}
	return nil
}
func (m *mockDismissalRepo) Restore(_ context.Context, u, i uuid.UUID) error {
	if m.dismissed[u] != nil {
		delete(m.dismissed[u], i)
	}
	return nil
}
func (m *mockDismissalRepo) DismissedIDs(_ context.Context, u uuid.UUID) (map[uuid.UUID]bool, error) {
	out := map[uuid.UUID]bool{}
	for i := range m.dismissed[u] {
		out[i] = true
	}
	return out, nil
}
func (m *mockDismissalRepo) ClearForInstance(_ context.Context, i uuid.UUID) error {
	for u := range m.dismissed {
		delete(m.dismissed[u], i)
	}
	return nil
}

var _ instance.DismissalRepository = (*mockDismissalRepo)(nil)

// errDismissalRepo always returns a generic error for every write operation.
type errDismissalRepo struct{ err error }

func (e *errDismissalRepo) Dismiss(_ context.Context, _, _ uuid.UUID) error { return e.err }
func (e *errDismissalRepo) DismissMany(_ context.Context, _ uuid.UUID, _ []uuid.UUID) error {
	return e.err
}
func (e *errDismissalRepo) Restore(_ context.Context, _, _ uuid.UUID) error { return e.err }
func (e *errDismissalRepo) DismissedIDs(_ context.Context, _ uuid.UUID) (map[uuid.UUID]bool, error) {
	return nil, e.err
}
func (e *errDismissalRepo) ClearForInstance(_ context.Context, _ uuid.UUID) error { return e.err }

var _ instance.DismissalRepository = (*errDismissalRepo)(nil)

// errUserRepo always returns an error from GetDefaultUser.
type errUserRepo struct{ err error }

func (e *errUserRepo) GetDefaultUser(_ context.Context) (uuid.UUID, error) {
	return uuid.UUID{}, e.err
}

var _ instance.UserRepository = (*errUserRepo)(nil)

// newDismissalTestServer builds a router with the dismissal deps wired and
// returns an httptest server. It reuses the http_test mock constructors.
// dis accepts any DismissalRepository implementation (including error mocks).
// users accepts any UserRepository implementation (including error mocks).
func newDismissalTestServer(t *testing.T, instRepo *mockInstanceRepo, dis instance.DismissalRepository, users instance.UserRepository) *httptest.Server {
	t.Helper()
	router := adapthttp.NewRouterFromDeps(adapthttp.RouterDeps{
		Service:    &routerMockService{},
		Workflows:  newMockWorkflowRepo(),
		Instances:  instRepo,
		NodeExecs:  newMockNodeExecRepo(),
		Snapshots:  newMockSnapshotRepo(),
		EventBus:   eventbus.NewNoOp(),
		Config:     &config.Config{},
		Dismissals: dis,
		Users:      users,
	})
	srv := httptest.NewServer(router)
	return srv
}

func doPOST(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Post(url, "application/json", nil)
	require.NoError(t, err)
	return resp
}

func TestDismiss_TerminalInstance_OK(t *testing.T) {
	user := uuid.New()
	instRepo := newMockInstanceRepo()
	id := uuid.New()
	require.NoError(t, instRepo.Create(context.Background(), &instance.Instance{
		ID: id, OwnerID: user, WorkflowName: "wf", State: instance.StateFailed,
	}))
	dis := newMockDismissalRepo()

	srv := newDismissalTestServer(t, instRepo, dis, &mockUserRepo{id: user})
	defer srv.Close()

	resp := doPOST(t, srv.URL+"/api/v1/instances/"+id.String()+"/dismiss")
	defer resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)
	assert.True(t, dis.dismissed[user][id], "dismiss must record the per-user row")
}

// Seam: the Dismiss handler must scope the dismissal to the ACTING user
// (from currentUser.CurrentUser), not to the instance's OwnerID. When the
// acting user and the owner are the same UUID, a bug using inst.OwnerID
// instead of the resolved user would silently pass. Using distinct UUIDs
// catches that regression.
func TestDismiss_ScopedToActingUserNotOwner(t *testing.T) {
	owner := uuid.New() // instance owner
	actor := uuid.New() // the user who is acting (different from owner)
	instRepo := newMockInstanceRepo()
	id := uuid.New()
	require.NoError(t, instRepo.Create(context.Background(), &instance.Instance{
		ID: id, OwnerID: owner, WorkflowName: "wf", State: instance.StateFailed,
	}))
	dis := newMockDismissalRepo()

	// Acting as 'actor', not the instance's owner.
	srv := newDismissalTestServer(t, instRepo, dis, &mockUserRepo{id: actor})
	defer srv.Close()

	resp := doPOST(t, srv.URL+"/api/v1/instances/"+id.String()+"/dismiss")
	defer resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)
	assert.True(t, dis.dismissed[actor][id], "dismissal must be recorded for the acting user")
	assert.False(t, dis.dismissed[owner][id], "owner's dismissal record must not be created by actor's action")
}

// Seam: DismissAll must also scope each per-item dismissal to the acting user,
// not to each instance's individual owner. Same regression risk as Dismiss.
func TestDismissAll_ScopedToActingUserNotOwner(t *testing.T) {
	owner := uuid.New()
	actor := uuid.New()
	instRepo := newMockInstanceRepo()
	ctx := context.Background()
	id := uuid.New()
	require.NoError(t, instRepo.Create(ctx, &instance.Instance{
		ID: id, OwnerID: owner, WorkflowName: "wf", State: instance.StateFailed,
	}))
	dis := newMockDismissalRepo()

	srv := newDismissalTestServer(t, instRepo, dis, &mockUserRepo{id: actor})
	defer srv.Close()

	resp := doPOST(t, srv.URL+"/api/v1/instances/dismiss-all?state=failed")
	defer resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)
	assert.True(t, dis.dismissed[actor][id], "dismiss-all must record for the acting user")
	assert.False(t, dis.dismissed[owner][id], "owner's dismissal record must not be created by actor")
}

// Success: dismissing a completed instance also returns 200 — isDismissibleState
// accepts both failed and completed, but only the failed arm was tested.
func TestDismiss_CompletedInstance_OK(t *testing.T) {
	user := uuid.New()
	instRepo := newMockInstanceRepo()
	id := uuid.New()
	require.NoError(t, instRepo.Create(context.Background(), &instance.Instance{
		ID: id, OwnerID: user, WorkflowName: "wf", State: instance.StateCompleted,
	}))
	dis := newMockDismissalRepo()

	srv := newDismissalTestServer(t, instRepo, dis, &mockUserRepo{id: user})
	defer srv.Close()

	resp := doPOST(t, srv.URL+"/api/v1/instances/"+id.String()+"/dismiss")
	defer resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)
	assert.True(t, dis.dismissed[user][id], "completed instance must be recorded as dismissed")
}

func TestDismiss_NonTerminal_409(t *testing.T) {
	user := uuid.New()
	instRepo := newMockInstanceRepo()
	id := uuid.New()
	require.NoError(t, instRepo.Create(context.Background(), &instance.Instance{
		ID: id, OwnerID: user, WorkflowName: "wf", State: instance.StateRunning,
	}))
	srv := newDismissalTestServer(t, instRepo, newMockDismissalRepo(), &mockUserRepo{id: user})
	defer srv.Close()

	resp := doPOST(t, srv.URL+"/api/v1/instances/"+id.String()+"/dismiss")
	defer resp.Body.Close()
	assert.Equal(t, 409, resp.StatusCode)
}

// Edge: a waiting (interactive) instance cannot be dismissed.
// The spec explicitly names "waiting (interactive)" alongside "active" and "cancelled"
// as non-dismissible states. isDismissibleState is completed||failed only.
func TestDismiss_WaitingInstance_409(t *testing.T) {
	user := uuid.New()
	instRepo := newMockInstanceRepo()
	id := uuid.New()
	require.NoError(t, instRepo.Create(context.Background(), &instance.Instance{
		ID: id, OwnerID: user, WorkflowName: "wf", State: instance.StateWaiting,
	}))
	srv := newDismissalTestServer(t, instRepo, newMockDismissalRepo(), &mockUserRepo{id: user})
	defer srv.Close()

	resp := doPOST(t, srv.URL+"/api/v1/instances/"+id.String()+"/dismiss")
	defer resp.Body.Close()
	assert.Equal(t, 409, resp.StatusCode)
}

// Edge: a paused instance cannot be dismissed — same guard as waiting and running.
func TestDismiss_PausedInstance_409(t *testing.T) {
	user := uuid.New()
	instRepo := newMockInstanceRepo()
	id := uuid.New()
	require.NoError(t, instRepo.Create(context.Background(), &instance.Instance{
		ID: id, OwnerID: user, WorkflowName: "wf", State: instance.StatePaused,
	}))
	srv := newDismissalTestServer(t, instRepo, newMockDismissalRepo(), &mockUserRepo{id: user})
	defer srv.Close()

	resp := doPOST(t, srv.URL+"/api/v1/instances/"+id.String()+"/dismiss")
	defer resp.Body.Close()
	assert.Equal(t, 409, resp.StatusCode)
}

// Edge: a cancelled instance is terminal in the stream handler's sense but
// isDismissibleState is narrower — only completed/failed can be dismissed.
// A widening of that guard (to include cancelled) must not silently pass.
func TestDismiss_CancelledInstance_409(t *testing.T) {
	user := uuid.New()
	instRepo := newMockInstanceRepo()
	id := uuid.New()
	require.NoError(t, instRepo.Create(context.Background(), &instance.Instance{
		ID: id, OwnerID: user, WorkflowName: "wf", State: instance.StateCancelled,
	}))
	srv := newDismissalTestServer(t, instRepo, newMockDismissalRepo(), &mockUserRepo{id: user})
	defer srv.Close()

	resp := doPOST(t, srv.URL+"/api/v1/instances/"+id.String()+"/dismiss")
	defer resp.Body.Close()
	assert.Equal(t, 409, resp.StatusCode)
}

func TestDismiss_NotFound_404(t *testing.T) {
	srv := newDismissalTestServer(t, newMockInstanceRepo(), newMockDismissalRepo(), &mockUserRepo{id: uuid.New()})
	defer srv.Close()

	resp := doPOST(t, srv.URL+"/api/v1/instances/"+uuid.New().String()+"/dismiss")
	defer resp.Body.Close()
	assert.Equal(t, 404, resp.StatusCode)
}

func TestRestore_RemovesDismissal_OK(t *testing.T) {
	user := uuid.New()
	instRepo := newMockInstanceRepo()
	id := uuid.New()
	require.NoError(t, instRepo.Create(context.Background(), &instance.Instance{
		ID: id, OwnerID: user, WorkflowName: "wf", State: instance.StateCompleted,
	}))
	dis := newMockDismissalRepo()
	require.NoError(t, dis.Dismiss(context.Background(), user, id))
	srv := newDismissalTestServer(t, instRepo, dis, &mockUserRepo{id: user})
	defer srv.Close()

	resp := doPOST(t, srv.URL+"/api/v1/instances/"+id.String()+"/restore")
	defer resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)
	assert.False(t, dis.dismissed[user][id])
}

func TestRestore_NonDismissed_OK_NoOp(t *testing.T) {
	user := uuid.New()
	instRepo := newMockInstanceRepo()
	id := uuid.New()
	require.NoError(t, instRepo.Create(context.Background(), &instance.Instance{
		ID: id, OwnerID: user, WorkflowName: "wf", State: instance.StateCompleted,
	}))
	srv := newDismissalTestServer(t, instRepo, newMockDismissalRepo(), &mockUserRepo{id: user})
	defer srv.Close()

	resp := doPOST(t, srv.URL+"/api/v1/instances/"+id.String()+"/restore")
	defer resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode) // no-op success
}

// Seam: Restore must scope to the ACTING user only — it must not clear
// dismissals for other users who independently dismissed the same instance.
// A bug that called ClearForInstance (cross-user) instead of per-user Restore,
// or that passed the wrong user ID, would incorrectly un-dismiss the instance
// for unrelated users. Using two distinct users pins this scoping contract.
func TestRestore_ScopedToActingUserNotOtherUsers(t *testing.T) {
	userA := uuid.New() // acting user (restores their own dismissal)
	userB := uuid.New() // another user who dismissed independently
	ctx := context.Background()
	instRepo := newMockInstanceRepo()
	id := uuid.New()
	require.NoError(t, instRepo.Create(ctx, &instance.Instance{
		ID: id, OwnerID: userA, WorkflowName: "wf", State: instance.StateCompleted,
	}))
	dis := newMockDismissalRepo()
	require.NoError(t, dis.Dismiss(ctx, userA, id))
	require.NoError(t, dis.Dismiss(ctx, userB, id))

	// Acting as userA: restore must remove only A's dismissal.
	srv := newDismissalTestServer(t, instRepo, dis, &mockUserRepo{id: userA})
	defer srv.Close()

	resp := doPOST(t, srv.URL+"/api/v1/instances/"+id.String()+"/restore")
	defer resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)
	assert.False(t, dis.dismissed[userA][id], "acting user's dismissal must be removed by restore")
	assert.True(t, dis.dismissed[userB][id], "other user's dismissal must not be affected by A's restore")
}

func TestDismissAll_DismissesEveryListedInCategory(t *testing.T) {
	user := uuid.New()
	instRepo := newMockInstanceRepo()
	ctx := context.Background()
	a := uuid.New()
	b := uuid.New()
	other := uuid.New()
	require.NoError(t, instRepo.Create(ctx, &instance.Instance{ID: a, OwnerID: user, WorkflowName: "wf", State: instance.StateFailed}))
	require.NoError(t, instRepo.Create(ctx, &instance.Instance{ID: b, OwnerID: user, WorkflowName: "wf", State: instance.StateFailed}))
	require.NoError(t, instRepo.Create(ctx, &instance.Instance{ID: other, OwnerID: user, WorkflowName: "wf", State: instance.StateCompleted}))
	dis := newMockDismissalRepo()
	srv := newDismissalTestServer(t, instRepo, dis, &mockUserRepo{id: user})
	defer srv.Close()

	resp := doPOST(t, srv.URL+"/api/v1/instances/dismiss-all?state=failed")
	defer resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)
	assert.True(t, dis.dismissed[user][a])
	assert.True(t, dis.dismissed[user][b])
	assert.False(t, dis.dismissed[user][other], "completed item must not be dismissed by state=failed")
}

// Success: DismissAll with state=completed dismisses completed instances and
// leaves failed instances untouched — mirrors DismissesEveryListedInCategory
// for the other valid state value.
func TestDismissAll_CompletedCategory_DismissesAll(t *testing.T) {
	user := uuid.New()
	instRepo := newMockInstanceRepo()
	ctx := context.Background()
	a := uuid.New()
	b := uuid.New()
	other := uuid.New()
	require.NoError(t, instRepo.Create(ctx, &instance.Instance{ID: a, OwnerID: user, WorkflowName: "wf", State: instance.StateCompleted}))
	require.NoError(t, instRepo.Create(ctx, &instance.Instance{ID: b, OwnerID: user, WorkflowName: "wf", State: instance.StateCompleted}))
	require.NoError(t, instRepo.Create(ctx, &instance.Instance{ID: other, OwnerID: user, WorkflowName: "wf", State: instance.StateFailed}))
	dis := newMockDismissalRepo()
	srv := newDismissalTestServer(t, instRepo, dis, &mockUserRepo{id: user})
	defer srv.Close()

	resp := doPOST(t, srv.URL+"/api/v1/instances/dismiss-all?state=completed")
	defer resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)
	assert.True(t, dis.dismissed[user][a])
	assert.True(t, dis.dismissed[user][b])
	assert.False(t, dis.dismissed[user][other], "failed item must not be dismissed by state=completed")
}

// Robustness: DismissAll must pass the since filter to List so that only items
// in the displayed time window are dismissed. Without this filter, clicking
// "Dismiss All" in a section would dismiss items older than the dashboard window,
// which the user can't even see. A regression that drops filters.Since would
// dismiss the "old" instance here and fail this test.
func TestDismissAll_SinceFilterLimitsScope(t *testing.T) {
	user := uuid.New()
	instRepo := newMockInstanceRepo()
	ctx := context.Background()
	cutoff := time.Now().Add(-24 * time.Hour)

	old := uuid.New()
	recent := uuid.New()
	require.NoError(t, instRepo.Create(ctx, &instance.Instance{
		ID: old, OwnerID: user, WorkflowName: "wf", State: instance.StateFailed,
		CreatedAt: cutoff.Add(-time.Hour), // before the since cutoff
	}))
	require.NoError(t, instRepo.Create(ctx, &instance.Instance{
		ID: recent, OwnerID: user, WorkflowName: "wf", State: instance.StateFailed,
		CreatedAt: cutoff.Add(time.Hour), // after the since cutoff
	}))
	dis := newMockDismissalRepo()
	srv := newDismissalTestServer(t, instRepo, dis, &mockUserRepo{id: user})
	defer srv.Close()

	resp := doPOST(t, srv.URL+"/api/v1/instances/dismiss-all?state=failed&since="+cutoff.UTC().Format(time.RFC3339))
	defer resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)
	assert.True(t, dis.dismissed[user][recent], "recent item must be dismissed")
	assert.False(t, dis.dismissed[user][old], "old item (before since) must not be dismissed")
}

func TestDismissAll_BadState_400(t *testing.T) {
	srv := newDismissalTestServer(t, newMockInstanceRepo(), newMockDismissalRepo(), &mockUserRepo{id: uuid.New()})
	defer srv.Close()
	for _, s := range []string{"", "running", "queued", "bogus"} {
		resp := doPOST(t, srv.URL+"/api/v1/instances/dismiss-all?state="+s)
		assert.Equal(t, 400, resp.StatusCode, "state=%q must be rejected", s)
		resp.Body.Close()
	}
}

func TestDismissAll_EmptyCategory_OK(t *testing.T) {
	srv := newDismissalTestServer(t, newMockInstanceRepo(), newMockDismissalRepo(), &mockUserRepo{id: uuid.New()})
	defer srv.Close()
	resp := doPOST(t, srv.URL+"/api/v1/instances/dismiss-all?state=completed")
	defer resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode) // no-op success on empty category
}

func TestDismiss_InvalidID_400(t *testing.T) {
	srv := newDismissalTestServer(t, newMockInstanceRepo(), newMockDismissalRepo(), &mockUserRepo{id: uuid.New()})
	defer srv.Close()
	resp := doPOST(t, srv.URL+"/api/v1/instances/not-a-uuid/dismiss")
	defer resp.Body.Close()
	assert.Equal(t, 400, resp.StatusCode)
}

func TestRestore_InvalidID_400(t *testing.T) {
	srv := newDismissalTestServer(t, newMockInstanceRepo(), newMockDismissalRepo(), &mockUserRepo{id: uuid.New()})
	defer srv.Close()
	resp := doPOST(t, srv.URL+"/api/v1/instances/not-a-uuid/restore")
	defer resp.Body.Close()
	assert.Equal(t, 400, resp.StatusCode)
}

func TestDismiss_RepoError_500(t *testing.T) {
	user := uuid.New()
	instRepo := newMockInstanceRepo()
	id := uuid.New()
	require.NoError(t, instRepo.Create(context.Background(), &instance.Instance{
		ID: id, OwnerID: user, WorkflowName: "wf", State: instance.StateFailed,
	}))
	srv := newDismissalTestServer(t, instRepo, &errDismissalRepo{err: fmt.Errorf("db down")}, &mockUserRepo{id: user})
	defer srv.Close()

	resp := doPOST(t, srv.URL+"/api/v1/instances/"+id.String()+"/dismiss")
	defer resp.Body.Close()
	assert.Equal(t, 500, resp.StatusCode)
}

func TestRestore_RepoError_500(t *testing.T) {
	srv := newDismissalTestServer(t, newMockInstanceRepo(), &errDismissalRepo{err: fmt.Errorf("db down")}, &mockUserRepo{id: uuid.New()})
	defer srv.Close()

	resp := doPOST(t, srv.URL+"/api/v1/instances/"+uuid.New().String()+"/restore")
	defer resp.Body.Close()
	assert.Equal(t, 500, resp.StatusCode)
}

func TestDismissAll_ListError_500(t *testing.T) {
	// Use errInstanceRepo so List fails; wire a working dismissal repo.
	// Construct the server manually since newDismissalTestServer uses mockInstanceRepo.
	router := adapthttp.NewRouterFromDeps(adapthttp.RouterDeps{
		Service:    &routerMockService{},
		Workflows:  newMockWorkflowRepo(),
		Instances:  &errInstanceRepo{err: fmt.Errorf("db down")},
		NodeExecs:  newMockNodeExecRepo(),
		Snapshots:  newMockSnapshotRepo(),
		EventBus:   eventbus.NewNoOp(),
		Config:     &config.Config{},
		Dismissals: newMockDismissalRepo(),
		Users:      &mockUserRepo{id: uuid.New()},
	})
	srv := httptest.NewServer(router)
	defer srv.Close()

	resp := doPOST(t, srv.URL+"/api/v1/instances/dismiss-all?state=failed")
	defer resp.Body.Close()
	assert.Equal(t, 500, resp.StatusCode)
}

func TestDismiss_UserResolutionError_500(t *testing.T) {
	user := uuid.New()
	instRepo := newMockInstanceRepo()
	id := uuid.New()
	require.NoError(t, instRepo.Create(context.Background(), &instance.Instance{
		ID: id, OwnerID: user, WorkflowName: "wf", State: instance.StateFailed,
	}))
	srv := newDismissalTestServer(t, instRepo, newMockDismissalRepo(), &errUserRepo{err: fmt.Errorf("auth error")})
	defer srv.Close()

	resp := doPOST(t, srv.URL+"/api/v1/instances/"+id.String()+"/dismiss")
	defer resp.Body.Close()
	assert.Equal(t, 500, resp.StatusCode)
}

func TestRestore_UserResolutionError_500(t *testing.T) {
	srv := newDismissalTestServer(t, newMockInstanceRepo(), newMockDismissalRepo(), &errUserRepo{err: fmt.Errorf("auth error")})
	defer srv.Close()

	resp := doPOST(t, srv.URL+"/api/v1/instances/"+uuid.New().String()+"/restore")
	defer resp.Body.Close()
	assert.Equal(t, 500, resp.StatusCode)
}

func TestDismissAll_UserResolutionError_500(t *testing.T) {
	srv := newDismissalTestServer(t, newMockInstanceRepo(), newMockDismissalRepo(), &errUserRepo{err: fmt.Errorf("auth error")})
	defer srv.Close()

	resp := doPOST(t, srv.URL+"/api/v1/instances/dismiss-all?state=failed")
	defer resp.Body.Close()
	assert.Equal(t, 500, resp.StatusCode)
}

func TestDismissAll_DismissItemError_500(t *testing.T) {
	user := uuid.New()
	instRepo := newMockInstanceRepo()
	id := uuid.New()
	require.NoError(t, instRepo.Create(context.Background(), &instance.Instance{
		ID: id, OwnerID: user, WorkflowName: "wf", State: instance.StateFailed,
	}))
	// errDismissalRepo fails on the atomic DismissMany write that DismissAll uses.
	// instances.List succeeds (mockInstanceRepo) so the write is reached.
	srv := newDismissalTestServer(t, instRepo, &errDismissalRepo{err: fmt.Errorf("db down")}, &mockUserRepo{id: user})
	defer srv.Close()

	resp := doPOST(t, srv.URL+"/api/v1/instances/dismiss-all?state=failed")
	defer resp.Body.Close()
	assert.Equal(t, 500, resp.StatusCode)
}

// atomicDismissalRepo fails DismissMany as a unit and records nothing, modeling
// the all-or-nothing transaction in the sqlite repo. It panics if the handler
// ever falls back to the per-item Dismiss path (which would mean a
// partial-commit-then-500 could occur).
type atomicDismissalRepo struct {
	*mockDismissalRepo
	failMany bool
}

func (a *atomicDismissalRepo) Dismiss(_ context.Context, _, _ uuid.UUID) error {
	panic("DismissAll must use the atomic DismissMany path, not per-item Dismiss")
}
func (a *atomicDismissalRepo) DismissMany(ctx context.Context, u uuid.UUID, ids []uuid.UUID) error {
	if a.failMany {
		return fmt.Errorf("db down")
	}
	return a.mockDismissalRepo.DismissMany(ctx, u, ids)
}

// Atomicity: DismissAll routes through DismissMany (one atomic write), never the
// per-item loop. On a batch failure nothing is recorded — no partial commit.
func TestDismissAll_UsesAtomicDismissMany_NoPartialCommit(t *testing.T) {
	user := uuid.New()
	instRepo := newMockInstanceRepo()
	ctx := context.Background()
	a := uuid.New()
	b := uuid.New()
	require.NoError(t, instRepo.Create(ctx, &instance.Instance{ID: a, OwnerID: user, WorkflowName: "wf", State: instance.StateFailed}))
	require.NoError(t, instRepo.Create(ctx, &instance.Instance{ID: b, OwnerID: user, WorkflowName: "wf", State: instance.StateFailed}))

	dis := &atomicDismissalRepo{mockDismissalRepo: newMockDismissalRepo(), failMany: true}
	srv := newDismissalTestServer(t, instRepo, dis, &mockUserRepo{id: user})
	defer srv.Close()

	resp := doPOST(t, srv.URL+"/api/v1/instances/dismiss-all?state=failed")
	defer resp.Body.Close()
	assert.Equal(t, 500, resp.StatusCode)
	assert.Empty(t, dis.dismissed[user], "a failed atomic batch must record nothing (no partial commit)")
}
