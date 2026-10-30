package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/instance"
)

// This real-store response is also consumed at the rendered queue sink in
// QueueView.ancestors.test.tsx. Regenerate with UPDATE_QUEUE_ANCESTORS_WIRE=1.
func TestQueueAncestorsE2E_WirePagesPast500WithTimestampTies(t *testing.T) {
	t.Parallel()
	ds := bootGraphStateStack(t)
	srv := gsServeAPI(t, ds)
	ctx := context.Background()
	owner := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	// Discovery still crosses a full 500-row page, but most terminal ancestors
	// share a root. The UI sink needs the last page's parent and child rendered,
	// not 501 unrelated roots competing with the 100-child pagination fixture.
	root := uuid.MustParse("00000000-0000-0000-0000-000000000501")
	for i := 1; i <= 501; i++ {
		parent := uuid.MustParse(fmt.Sprintf("00000000-0000-0000-0000-%012d", i))
		child := uuid.MustParse(fmt.Sprintf("10000000-0000-0000-0000-%012d", i))
		node := "spawn-1"
		for _, inst := range []*instance.Instance{
			{ID: parent, OwnerID: owner, WorkflowName: "gs-ada-parent", State: instance.StateCompleted,
				Priority: instance.PriorityNormal, WorkflowSnapshot: map[string]any{}, Variables: map[string]any{}},
			{ID: child, OwnerID: owner, WorkflowName: "gs-ada-child", State: instance.StatePaused,
				Priority: instance.PriorityNormal, WorkflowSnapshot: map[string]any{}, Variables: map[string]any{},
				ParentInstanceID: &parent, ParentNodeID: &node, SpawnMode: instance.SpawnModeFireAndForget},
		} {
			if inst.ID == parent && i > 1 && i < 501 {
				inst.ParentInstanceID, inst.ParentNodeID, inst.SpawnMode = &root, &node, instance.SpawnModeFireAndForget
			}
			inst.ModelPins, inst.ModelPinsVersion = map[string]string{}, instance.ModelPinsVersionSnapshot
			require.NoError(t, ds.instanceRepo.Create(ctx, inst))
		}
	}
	// Force ties; relying on elapsed wall time would miss unstable page boundaries.
	_, err := ds.db.Exec(`UPDATE instances SET created_at = '2026-10-01T00:00:00Z', updated_at = '2026-10-01T00:00:00Z'`)
	require.NoError(t, err)
	get := func(query string) []map[string]any {
		t.Helper()
		resp, err := http.Get(srv.URL + "/api/v1/instances?" + query)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var rows []map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&rows))
		return rows
	}
	query := "activeDescendantAncestors=true&includeDismissed=true&limit=500"
	first, second := get(query), get(query+"&offset=500")
	require.Len(t, first, 500)
	require.Len(t, second, 1)
	require.Equal(t, "00000000-0000-0000-0000-000000000501", first[0]["id"], "ties need a deterministic secondary order")
	require.Equal(t, "00000000-0000-0000-0000-000000000002", first[499]["id"])
	require.Equal(t, "00000000-0000-0000-0000-000000000001", second[0]["id"])
	require.EqualValues(t, 500, first[0]["childCount"])
	ids := map[string]bool{}
	for _, page := range [][]map[string]any{first, second} {
		for _, row := range page {
			id := row["id"].(string)
			require.False(t, ids[id], "discovery pages must not overlap")
			ids[id] = true
			if id != root.String() && id != second[0]["id"] {
				require.Equal(t, root.String(), row["parentInstanceId"])
			}
		}
	}
	require.Len(t, ids, 501)
	children := get("parent=00000000-0000-0000-0000-000000000001&includeDismissed=true&limit=100")
	require.Len(t, children, 1)
	got, err := json.Marshal(map[string]any{"pages": []any{first, second}, "children": children})
	require.NoError(t, err)
	const fixture = "../../dashboard/src/api/fixtures/queue-ancestors.wire.json"
	if os.Getenv("UPDATE_QUEUE_ANCESTORS_WIRE") == "1" {
		require.NoError(t, os.WriteFile(fixture, append(got, '\n'), 0o644))
	}
	want, err := os.ReadFile(fixture)
	require.NoError(t, err, "regenerate with UPDATE_QUEUE_ANCESTORS_WIRE=1 go test ./cmd/arbitron -run TestQueueAncestorsE2E_WirePagesPast500")
	require.JSONEq(t, string(want), string(got))
}
