package http_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/adapter/eventbus"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/instance"
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

// newDismissalTestServer builds a router with the dismissal deps wired and
// returns an httptest server. It reuses the http_test mock constructors.
func newDismissalTestServer(t *testing.T, instRepo *mockInstanceRepo, dis *mockDismissalRepo, users *mockUserRepo) *httptest.Server {
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
