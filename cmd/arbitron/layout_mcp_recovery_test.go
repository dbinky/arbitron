package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLayoutMCPE2E_ImmediatePeerCrashReadAndSave(t *testing.T) {
	for _, boundary := range []string{"before-proof", "after-proof"} {
		t.Run(boundary, func(t *testing.T) {
			e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
			ds, _ := e.boot(t)
			ctx := adminCtxForLocalUser(t, ds)
			require.NoError(t, ds.workflowSvc.Create(ctx, "source", layoutE2EWorkflow("source", "Committed")))
			require.NoError(t, ds.workflowSvc.Publish(ctx, "source", "target", workflow.VisibilityPublic, ""))
			old := layoutE2ESavedRevision(t, e.sharedPath("target"))
			layoutE2EWaitTerminal(t, ds, old)
			ds.layout.worker.Stop() // only the surviving peer's tool read may settle this crash
			srv := newLayoutE2EMCPServer(t, ds)
			timeout, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(timeout, os.Args[0], "-test.run=^TestLayoutE2E_CrashProcess$")
			cmd.Env = append(os.Environ(), "ARBITRON_LAYOUT_CRASH_CONFIG="+e.configPath, "ARBITRON_LAYOUT_CRASH_STAGE=shared/"+boundary)
			cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
			require.NoError(t, cmd.Start())
			t.Cleanup(func() {
				if cmd.ProcessState == nil {
					cmd.Process.Kill()
					cmd.Wait()
				}
			})
			require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(e.root, "crash-ready")); return err == nil }, 10*time.Second, 10*time.Millisecond)
			raw, err := os.ReadFile(filepath.Join(e.root, "captured.json"))
			require.NoError(t, err)
			var candidate workflow.ArrangementRequest
			require.NoError(t, json.Unmarshal(raw, &candidate))
			require.NoError(t, cmd.Process.Kill())
			require.Error(t, cmd.Wait())
			want := old
			if boundary == "after-proof" {
				want = candidate.Revision
			}
			res := callMCPToolE2E(t, srv, ctx, "workflow_get", map[string]any{"name": "target"})
			require.False(t, res.IsError, mcpE2EText(t, res))
			var out map[string]any
			require.NoError(t, json.Unmarshal([]byte(mcpE2EText(t, res)), &out))
			assert.Equal(t, want, out["presentation"].(map[string]any)["revision"])
			assert.Equal(t, want, out["arrangement"].(map[string]any)["savedRevision"])
			assert.Equal(t, want, layoutE2ESavedRevision(t, e.sharedPath("target")))
			proof, ok, err := ds.publishedRepo.PublicationOf(ctx, "target")
			require.True(t, ok)
			require.NoError(t, err)
			assert.Equal(t, want, proof.Revision)
			entries, err := ds.layout.commits.CommittedRequests(ctx)
			require.NoError(t, err)
			found := false
			for _, entry := range entries {
				if entry.Revision == candidate.Revision {
					found = true
					assert.Equal(t, candidate.Manifest, entry.Manifest)
				}
			}
			assert.Equal(t, boundary == "after-proof", found)
			update := queryE2EWorkflow("target", "Peer save after settlement")
			res = callMCPToolE2E(t, srv, ctx, "workflow_update", map[string]any{"name": "target", "manifest": queryE2EJSON(t, update)})
			require.False(t, res.IsError, mcpE2EText(t, res))
			final := layoutE2ESavedRevision(t, e.sharedPath("target"))
			require.NotEqual(t, want, final)
			row, err := ds.layout.store.Get(ctx, final)
			require.NoError(t, err)
			var saved workflow.Workflow
			require.NoError(t, json.Unmarshal(row.Manifest, &saved))
			assert.Equal(t, "Peer save after settlement", saved.Nodes[0].Label)
			proof, ok, err = ds.publishedRepo.PublicationOf(ctx, "target")
			require.True(t, ok)
			require.NoError(t, err)
			assert.Equal(t, final, proof.Revision)
			if boundary == "before-proof" {
				_, err = ds.layout.store.Get(ctx, candidate.Revision)
				require.Error(t, err, "uncommitted candidate must never reach SQLite")
			}
		})
	}
}

func TestLayoutMCPE2E_PendingRelocationReadAndSave(t *testing.T) {
	e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
	ds, _ := e.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.workflowSvc.Create(ctx, "pending", queryE2EWorkflow("pending", "Committed")))
	rev := manifestRevisionOnDisk(t, ds, "pending")
	layoutE2EWaitTerminal(t, ds, rev)
	ds.layout.worker.Stop()
	saved, err := ds.workflowSvc.Load(ctx, "pending")
	require.NoError(t, err)
	_, err = ds.layout.journal.BeginRelocation(ctx, workflow.Private(localDefaultUser), "pending", workflow.Private(localDefaultUser), "destination", saved.ID)
	require.NoError(t, err)
	srv := newLayoutE2EMCPServer(t, ds)
	manifest := queryE2EJSON(t, queryE2EWorkflow("pending", "Must not commit"))
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"workflow_get", map[string]any{"name": "pending"}},
		{"workflow_create", map[string]any{"manifest": manifest}},
		{"workflow_update", map[string]any{"name": "pending", "manifest": manifest}},
		{"workflow_delete", map[string]any{"name": "pending"}},
		{"workflow_validate", map[string]any{"name": "pending"}},
		{"workflow_layout_retry", map[string]any{"name": "pending", "expectedRevision": rev}},
		{"workflow_publish", map[string]any{"name": "pending", "publishedName": "published"}},
		{"workflow_duplicate", map[string]any{"name": "pending", "newName": "duplicated"}},
		{"workflow_rename", map[string]any{"name": "pending", "newName": "renamed"}},
		{"workflow_archive", map[string]any{"name": "pending"}},
		{"workflow_unarchive", map[string]any{"name": "pending"}},
		{"workflow_transfer_ownership", map[string]any{"name": "pending", "newOwner": localDefaultUser.String()}},
		{"workflow_write_file", map[string]any{"name": "pending", "path": "support.txt", "content": "new"}},
		{"workflow_delete_file", map[string]any{"name": "pending", "path": "support.txt"}},
	} {
		res := callMCPToolE2E(t, srv, ctx, tc.tool, tc.args)
		assert.True(t, res.IsError, tc.tool)
		assert.Equal(t, "workflow destination is awaiting durable mutation recovery; retry shortly", mcpE2EText(t, res), tc.tool)
	}
	assert.Equal(t, rev, manifestRevisionOnDisk(t, ds, "pending"))
	assert.Equal(t, 1, layoutE2ECountRows(t, ds.db, "pending"))
	_, err = ds.layout.commits.CommittedRequests(ctx)
	require.NoError(t, err)
	res := callMCPToolE2E(t, srv, ctx, "workflow_get", map[string]any{"name": "pending"})
	require.False(t, res.IsError, mcpE2EText(t, res))
}

func TestLayoutMCPE2E_PendingSharedAuthoring(t *testing.T) {
	e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
	ds, _ := e.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.workflowSvc.Create(ctx, "source", queryE2EWorkflow("source", "Committed")))
	require.NoError(t, ds.workflowSvc.Publish(ctx, "source", "shared", workflow.VisibilityPublic, ""))
	wf := layoutE2EManifest(t, e.sharedPath("shared"))
	layoutE2EWaitTerminal(t, ds, wf.SavedRevision())
	ds.layout.worker.Stop()
	_, err := ds.layout.journal.BeginRelocation(ctx, workflow.Shared(), "shared", workflow.Shared(), "moved", wf.ID)
	require.NoError(t, err)
	srv := newLayoutE2EMCPServer(t, ds)
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"workflow_copy", map[string]any{"name": "shared", "newName": "copied"}},
		{"workflow_set_visibility", map[string]any{"name": "shared", "visibility": "public"}},
		{"workflow_unpublish", map[string]any{"name": "shared"}},
	} {
		res := callMCPToolE2E(t, srv, ctx, tc.tool, tc.args)
		assert.True(t, res.IsError, tc.tool)
		assert.Equal(t, "workflow destination is awaiting durable mutation recovery; retry shortly", mcpE2EText(t, res), tc.tool)
	}
	assert.Equal(t, wf.SavedRevision(), layoutE2ESavedRevision(t, e.sharedPath("shared")))
	require.NoFileExists(t, e.privatePath("copied"))
}
