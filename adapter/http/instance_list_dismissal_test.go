package http_test

import (
	"context"
	"encoding/json"
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
