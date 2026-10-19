package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Removing the static drive-prefix guard accepts these MCP writes. The real
// registry and manifest, rather than a write-counting fake, are the sinks.
func TestGroupAuthoringE2E_DrivePathsRefusedBeforePersistence(t *testing.T) {
	ds, workspaceDir := bootGroupAuthoringStack(t)
	ctx := adminCtxForLocalUser(t, ds)
	srv := newGroupMCPServer(ds)
	registry := adaptsqlite.NewWorkflowIdentityRepo(ds.db)
	for _, form := range []string{workflow.GroupFormBranches, workflow.GroupFormWorkflows} {
		t.Run(form, func(t *testing.T) {
			name := "paths-" + form
			wf := filesModeGroup(name, "default-agent")
			cfg := wf.Nodes[0].Config.Group
			if form == workflow.GroupFormWorkflows {
				cfg.Form = form
				cfg.Tasks = []workflow.GroupTask{{Key: "a", Workflow: "group-child", Inputs: map[string]any{"topic": "review"}}}
				wf.Nodes = []workflow.Node{wf.Nodes[0], wf.Nodes[3], wf.Nodes[4]}
				wf.Edges = wf.Edges[2:]
			}
			cfg.RepoPath = "{{repo_dir}}/app"
			manifest, err := json.Marshal(wf)
			require.NoError(t, err)
			res := callMCPTool(t, srv, ctx, "workflow_create", map[string]any{"manifest": string(manifest)})
			require.False(t, res.IsError, mcpText(t, res))
			defPath := filepath.Join(privateDir(workspaceDir, name), "workflow.json")
			before, err := os.ReadFile(defPath)
			require.NoError(t, err)
			stored, err := ds.workflowSvc.Load(ctx, name)
			require.NoError(t, err)
			beforeRegistry, err := registry.Resolve(ctx, name)
			require.NoError(t, err)
			assert.Equal(t, stored.ID, beforeRegistry.ID)

			for _, drive := range []struct{ label, path string }{{"absolute", `C:\repo`}, {"relative", `c:repo`}} {
				for _, tool := range []string{"workflow_create", "workflow_update", "workflow_group_upsert"} {
					t.Run(tool+"/"+drive.label, func(t *testing.T) {
						wf.Name = name
						if tool == "workflow_create" {
							wf.Name = name + "-bad-" + drive.label
						}
						cfg.RepoPath = drive.path
						raw, err := json.Marshal(wf)
						require.NoError(t, err)
						args := map[string]any{"name": name, "manifest": string(raw)}
						if tool == "workflow_group_upsert" {
							raw, err = json.Marshal(workflow.GroupUpsert{NodeID: "fanout", Config: *cfg, SuccessTarget: "done", FailureTarget: "failed"})
							require.NoError(t, err)
							args = map[string]any{"name": name, "group": string(raw)}
						}
						res := callMCPTool(t, srv, ctx, tool, args)
						require.True(t, res.IsError, mcpText(t, res))
						v := decodeGroupValidation(t, mcpText(t, res))
						assert.False(t, v.Valid)
						assert.Contains(t, groupCodes(v.Errors), "GROUP_INVALID_REPO_PATH")
						assert.NotContains(t, groupCodes(v.Errors), "GROUP_CHECKER_UNAVAILABLE")
						after, err := os.ReadFile(defPath)
						require.NoError(t, err)
						assert.Equal(t, before, after)
						afterRegistry, err := registry.Resolve(ctx, name)
						require.NoError(t, err)
						assert.Equal(t, beforeRegistry, afterRegistry)
						if tool == "workflow_create" {
							assert.Equal(t, 0, registryRows(t, ds, wf.Name))
							_, err := os.Stat(privateDir(workspaceDir, wf.Name))
							assert.True(t, os.IsNotExist(err), "refused create left a folder: %v", err)
						}
					})
				}
			}
		})
	}
}
