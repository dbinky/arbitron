package main

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
)

// Paired row 13 (Area 4 relocation/delete ↔ Area 5 layout read). SINK = the
// HTTP bodies and the MCP instance_get result. A historical instance on
// revision A must read exactly A's view after its workflow is renamed (new
// revision B elsewhere), the old name is recreated (revision C at A's former
// locator), the renamed workflow is transferred, and both are deleted. Fails if
// the instance read resolves through the workflow's current name or destination
// (not-found after rename, C after recreate) or if relocation drops A's row.
func TestLayoutRelocationRead_HistoricalInstanceKeepsSnapshotRevision(t *testing.T) {
	t.Parallel()
	ds := bootQueryStack(t)
	router := newLayoutE2ERouter(t, ds)
	srv := newLayoutE2EMCPServer(t, ds)
	ctx := adminCtxForLocalUser(t, ds)
	const name, renamed = "reloc-read", "reloc-read-renamed"
	require.NoError(t, ds.workflowSvc.Create(ctx, name, queryE2EWorkflow(name, "Start")))
	revA := manifestRevisionOnDisk(t, ds, name)
	id, _, err := ds.eng.Trigger(ctx, name, map[string]any{}, "normal")
	require.NoError(t, err)
	require.Equal(t, revA, snapshotRevision(t, ds, id))
	require.Eventually(t, func() bool {
		inst, err := ds.instanceRepo.Get(ctx, id)
		return err == nil && inst.State.IsTerminal()
	}, 30*time.Second, 20*time.Millisecond, "delete requires the run to finish")
	waitTerminalWorkflowView(t, ds, ctx, name, revA)

	read := func(step string) (map[string]any, map[string]any) {
		t.Helper()
		status, dedicated := layoutE2EDo(t, router, http.MethodGet, "/api/v1/instances/"+id.String()+"/layout", "")
		require.Equal(t, http.StatusOK, status, "%s: %v", step, dedicated)
		status, body := layoutE2EDo(t, router, http.MethodGet, "/api/v1/instances/"+id.String(), "")
		require.Equal(t, http.StatusOK, status, "%s: %v", step, body)
		embedded, ok := body["layout"].(map[string]any)
		require.True(t, ok, "%s: GET /instances/{id} must embed the layout", step)
		res := callMCPToolE2E(t, srv, ctx, "instance_get", map[string]any{"id": id.String()})
		require.False(t, res.IsError, "%s: %s", step, mcpE2EText(t, res))
		var inst struct {
			WorkflowSnapshot struct {
				Presentation struct {
					Revision string `json:"revision"`
				} `json:"presentation"`
			} `json:"workflowSnapshot"`
		}
		require.NoError(t, json.Unmarshal([]byte(mcpE2EText(t, res)), &inst))
		assert.Equal(t, revA, inst.WorkflowSnapshot.Presentation.Revision, "%s: MCP instance_get keeps the snapshot revision", step)
		return dedicated, embedded
	}
	baseline, embedded := read("before relocation")
	require.Equal(t, revA, baseline["savedRevision"])
	require.NotContains(t, []any{string(layout.DisplayLegacy), string(layout.DisplayUnavailable)}, baseline["displayStatus"], "baseline: %v", baseline)
	require.NotEqual(t, false, baseline["available"])
	assert.Equal(t, baseline, embedded, "dedicated and embedded instance reads are one view")
	same := func(step string, avoid ...string) {
		t.Helper()
		dedicated, embedded := read(step)
		assert.Equal(t, baseline, dedicated, "%s: dedicated instance layout must stay on revision A", step)
		assert.Equal(t, baseline, embedded, "%s: embedded instance layout must stay on revision A", step)
		for _, rev := range avoid {
			assert.NotEqual(t, rev, dedicated["savedRevision"], "%s: must not adopt a later destination revision", step)
		}
		row, err := ds.layout.store.Get(ctx, revA)
		require.NoError(t, err, "%s: revision A's history row must survive", step)
		assert.Equal(t, revA, row.Revision)
	}

	require.NoError(t, ds.workflowSvc.Rename(ctx, name, renamed))
	revB := manifestRevisionOnDisk(t, ds, renamed)
	require.NotEqual(t, revA, revB)
	same("after rename", revB)

	require.NoError(t, ds.workflowSvc.Create(ctx, name, queryE2EWorkflow(name, "Recreated")))
	revC := manifestRevisionOnDisk(t, ds, name)
	require.NotContains(t, []string{revA, revB}, revC)
	same("after recreating the old name", revB, revC)

	owner := uuid.New()
	require.NoError(t, ds.workflowSvc.TransferOwnership(ctx, renamed, owner))
	movedCtx := identity.WithIdentity(context.Background(), identity.Identity{UserID: owner, Role: identity.RoleAdmin})
	require.FileExists(t, filepath.Join(ds.cfg.Workspace.Path, "users", owner.String(), renamed, "workflow.json"))
	same("after ownership transfer", revB, revC)

	require.NoError(t, ds.workflowSvc.Delete(movedCtx, renamed))
	require.NoError(t, ds.workflowSvc.Delete(ctx, name))
	_, err = ds.layout.query.WorkflowLayout(ctx, name)
	require.ErrorIs(t, err, workflow.ErrNotFound)
	same("after deleting both destinations", revB, revC)
	_, err = ds.layout.store.Get(ctx, revB)
	assert.NoError(t, err, "delete keeps the renamed revision's history too")
}
