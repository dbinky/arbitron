package main

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestLayoutMCPE2E_CancellationAndConcealmentPreserveStore(t *testing.T) {
	e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
	ds, _ := e.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	const name = "context-owner-only"
	require.NoError(t, ds.workflowSvc.Create(ctx, name, queryE2EWorkflow(name, "Private saved input")))
	revision := manifestRevisionOnDisk(t, ds, name)
	before := layoutE2EWaitTerminal(t, ds, revision)
	require.Equal(t, layout.StateFailed, before.State)
	id, _, err := ds.eng.Trigger(ctx, name, nil, "normal")
	require.NoError(t, err)
	require.Equal(t, revision, snapshotRevision(t, ds, id))
	srv := newLayoutE2EMCPServer(t, ds)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	outsider := identity.WithIdentity(context.Background(), identity.Identity{UserID: uuid.New(), Role: identity.RoleUser, Username: "unrelated"})
	for _, caller := range []struct {
		name string
		ctx  context.Context
	}{{"canceled", canceled}, {"outsider", outsider}} {
		t.Run(caller.name, func(t *testing.T) {
			for _, tool := range []string{"workflow_get", "workflow_layout_retry", "instance_layout_retry"} {
				res := callMCPToolE2E(t, srv, caller.ctx, tool, map[string]any{"name": name, "id": id.String(), "expectedRevision": revision})
				require.True(t, res.IsError, "%s: %s", tool, mcpE2EText(t, res))
				require.NotContains(t, mcpE2EText(t, res), "Private saved input")
				require.NotContains(t, mcpE2EText(t, res), "jobGeneration")
				if caller.name == "canceled" {
					require.Contains(t, mcpE2EText(t, res), context.Canceled.Error())
				}
				after, err := ds.layout.store.Get(ctx, revision)
				require.NoError(t, err)
				require.Equal(t, before, after, "rejected %s must preserve the entire authoritative row", tool)
			}
		})
	}
	// The real owner must still succeed at the same sink with the same token.
	res := callMCPToolE2E(t, srv, ctx, "workflow_layout_retry", map[string]any{"name": name, "expectedRevision": revision})
	require.False(t, res.IsError, mcpE2EText(t, res))
	after := layoutE2EWaitTerminal(t, ds, revision)
	require.Equal(t, before.JobGeneration+1, after.JobGeneration)
	require.Equal(t, before.Manifest, after.Manifest)
}
