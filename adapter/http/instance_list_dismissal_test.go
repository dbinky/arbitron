package http_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// listItem mirrors the JSON the list endpoint returns, including the new flag.
type listItem struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	Dismissed bool   `json:"dismissed"`
}

func getList(t *testing.T, url string) []listItem {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	var items []listItem
	require.NoError(t, json.Unmarshal(body, &items))
	return items
}

func TestList_OmitsDismissedByDefault(t *testing.T) {
	user := uuid.New()
	ctx := context.Background()
	instRepo := newMockInstanceRepo()
	keep := uuid.New()
	gone := uuid.New()
	require.NoError(t, instRepo.Create(ctx, &instance.Instance{ID: keep, OwnerID: user, WorkflowName: "wf", State: instance.StateFailed}))
	require.NoError(t, instRepo.Create(ctx, &instance.Instance{ID: gone, OwnerID: user, WorkflowName: "wf", State: instance.StateFailed}))
	dis := newMockDismissalRepo()
	require.NoError(t, dis.Dismiss(ctx, user, gone))

	srv := newDismissalTestServer(t, instRepo, dis, &mockUserRepo{id: user})
	defer srv.Close()

	items := getList(t, srv.URL+"/api/v1/instances?state=failed")
	ids := map[string]bool{}
	for _, it := range items {
		ids[it.ID] = true
		assert.False(t, it.Dismissed, "default list never returns dismissed=true items")
	}
	assert.True(t, ids[keep.String()])
	assert.False(t, ids[gone.String()], "dismissed item must be omitted by default")
}

func TestList_IncludeDismissed_MarksAndKeepsOrder(t *testing.T) {
	user := uuid.New()
	ctx := context.Background()
	instRepo := newMockInstanceRepo()
	a := uuid.New()
	b := uuid.New()
	require.NoError(t, instRepo.Create(ctx, &instance.Instance{ID: a, OwnerID: user, WorkflowName: "wf", State: instance.StateFailed}))
	require.NoError(t, instRepo.Create(ctx, &instance.Instance{ID: b, OwnerID: user, WorkflowName: "wf", State: instance.StateFailed}))
	dis := newMockDismissalRepo()
	require.NoError(t, dis.Dismiss(ctx, user, a))

	srv := newDismissalTestServer(t, instRepo, dis, &mockUserRepo{id: user})
	defer srv.Close()

	items := getList(t, srv.URL+"/api/v1/instances?state=failed&includeDismissed=true")
	require.Len(t, items, 2, "includeDismissed=true returns all items in the window")
	flags := map[string]bool{}
	for _, it := range items {
		flags[it.ID] = it.Dismissed
	}
	assert.True(t, flags[a.String()], "dismissed item must be marked dismissed=true")
	assert.False(t, flags[b.String()], "non-dismissed item marked dismissed=false")
}

// Edge: when every item in the window is dismissed, the response is [] (not null).
// The handler uses make([]summaryResponse, 0, …) which encodes as [] even after
// an all-skip loop. Frontend code doing .map() or .length on the result must not
// receive null.
func TestList_AllDismissed_ReturnsEmptyArray(t *testing.T) {
	user := uuid.New()
	ctx := context.Background()
	instRepo := newMockInstanceRepo()
	a := uuid.New()
	b := uuid.New()
	require.NoError(t, instRepo.Create(ctx, &instance.Instance{ID: a, OwnerID: user, WorkflowName: "wf", State: instance.StateFailed}))
	require.NoError(t, instRepo.Create(ctx, &instance.Instance{ID: b, OwnerID: user, WorkflowName: "wf", State: instance.StateFailed}))
	dis := newMockDismissalRepo()
	require.NoError(t, dis.Dismiss(ctx, user, a))
	require.NoError(t, dis.Dismiss(ctx, user, b))

	srv := newDismissalTestServer(t, instRepo, dis, &mockUserRepo{id: user})
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/instances?state=failed")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	var items []listItem
	require.NoError(t, json.Unmarshal(body, &items))
	assert.NotNil(t, items, "all-dismissed list must serialize as [] not null")
	assert.Empty(t, items, "all dismissed items must be omitted by default")
}

// Error: DismissedIDs repo error causes List to return 500.
func TestList_DismissalRepoError_500(t *testing.T) {
	srv := newDismissalTestServer(t, newMockInstanceRepo(), &errDismissalRepo{err: fmt.Errorf("db down")}, &mockUserRepo{id: uuid.New()})
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/instances")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, 500, resp.StatusCode)
}

// Error: user resolution error in List returns 500 before DismissedIDs is called.
func TestList_UserResolutionError_500(t *testing.T) {
	srv := newDismissalTestServer(t, newMockInstanceRepo(), newMockDismissalRepo(), &errUserRepo{err: fmt.Errorf("auth error")})
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/instances")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, 500, resp.StatusCode)
}

// Seam: engine.Retry calls dismissalRepo.ClearForInstance, which clears the
// dismissal for every user. The HTTP List handler then calls DismissedIDs on
// the same repo and must return the item as undismissed. This test pins the
// "clear → resurface" seam: after ClearForInstance the item must reappear in
// the default list (not filtered out as dismissed).
func TestList_ResurfacesAfterClearForInstance(t *testing.T) {
	user := uuid.New()
	ctx := context.Background()
	instRepo := newMockInstanceRepo()
	id := uuid.New()
	require.NoError(t, instRepo.Create(ctx, &instance.Instance{ID: id, OwnerID: user, WorkflowName: "wf", State: instance.StateFailed}))
	dis := newMockDismissalRepo()
	require.NoError(t, dis.Dismiss(ctx, user, id))

	srv := newDismissalTestServer(t, instRepo, dis, &mockUserRepo{id: user})
	defer srv.Close()

	// Step 1: item is dismissed → omitted by default.
	items := getList(t, srv.URL+"/api/v1/instances?state=failed")
	assert.Empty(t, items, "dismissed item must be omitted before clear")

	// Step 2: engine.Retry calls ClearForInstance on the same repo.
	require.NoError(t, dis.ClearForInstance(ctx, id))

	// Step 3: item resurfaces — dismissed flag is gone.
	items = getList(t, srv.URL+"/api/v1/instances?state=failed")
	require.Len(t, items, 1, "item must resurface after ClearForInstance")
	assert.False(t, items[0].Dismissed, "resurfaced item must not be marked dismissed")
}

func TestList_DismissalScopedToActingUser(t *testing.T) {
	userA := uuid.New()
	userB := uuid.New()
	ctx := context.Background()
	instRepo := newMockInstanceRepo()
	id := uuid.New()
	require.NoError(t, instRepo.Create(ctx, &instance.Instance{ID: id, OwnerID: userA, WorkflowName: "wf", State: instance.StateCompleted}))
	dis := newMockDismissalRepo()
	require.NoError(t, dis.Dismiss(ctx, userA, id)) // only A dismisses

	// Acting as B: the item is still visible (B never dismissed it).
	srv := newDismissalTestServer(t, instRepo, dis, &mockUserRepo{id: userB})
	defer srv.Close()

	items := getList(t, srv.URL+"/api/v1/instances?state=completed")
	require.Len(t, items, 1)
	assert.False(t, items[0].Dismissed, "B sees the item as not dismissed")
}
