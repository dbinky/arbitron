package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

// Exercise the registered HTTP manifest and MCP group tools over initDomain's
// real service, stamper, filesystem journal and SQLite requester. Geometry/review
// of these immutable group inputs belongs to the group-format migration.
func TestServiceRevisionE2E_GroupAuthoringCapturedInput(t *testing.T) {
	for _, form := range []string{workflow.GroupFormBranches, workflow.GroupFormWorkflows} {
		for _, transport := range []string{"http", "mcp"} {
			t.Run(form+"/"+transport, func(t *testing.T) {
				e := newLayoutE2EEnv(t, layoutE2EOptions{})
				ds, _ := e.boot(t)
				ctx := adminCtxForLocalUser(t, ds)
				server := newGroupMCPServer(ds)
				api := gsServeAPI(t, ds)
				var child workflow.Workflow
				require.NoError(t, json.Unmarshal([]byte(groupEchoChildManifest), &child))
				require.NoError(t, ds.workflowSvc.Create(ctx, child.Name, &child))
				name := "group-authoring"
				original := filesModeGroup(name, "default-agent")
				groupIndex, success, failure := 0, "done", "failed"
				if form == workflow.GroupFormWorkflows {
					original = &workflow.Workflow{}
					require.NoError(t, json.Unmarshal([]byte(groupWorkflowsManifest), original))
					original.Name = name
					groupIndex, success, failure = 1, "onSuccess", "onFailure"
				}
				for i := range original.Nodes {
					original.Nodes[i].Position = &workflow.NodePosition{X: float64(100 + i*20), Y: float64(40 + i*30)}
				}
				encode := func(v any) string {
					t.Helper()
					b, err := json.Marshal(v)
					require.NoError(t, err)
					return string(b)
				}
				writeManifest := func(create bool, wf *workflow.Workflow, valid bool) {
					t.Helper()
					if transport == "mcp" {
						tool := "workflow_update"
						if create {
							tool = "workflow_create"
						}
						res := callMCPTool(t, server, ctx, tool, map[string]any{"name": name, "manifest": encode(wf)})
						require.Equal(t, !valid, res.IsError, mcpText(t, res))
					} else {
						method, url, want := http.MethodPut, api.URL+"/api/v1/workflows/"+name, http.StatusOK
						if create {
							method, url, want = http.MethodPost, api.URL+"/api/v1/workflows", http.StatusCreated
						}
						if !valid {
							want = http.StatusUnprocessableEntity
						}
						status, _ := httpJSON(t, method, url, wf)
						require.Equal(t, want, status)
					}
				}
				var previous *workflow.Workflow
				assertCaptured := func(expected *workflow.Workflow, count int) *workflow.Workflow {
					t.Helper()
					saved := layoutE2EManifest(t, e.privatePath(name))
					require.NotEmpty(t, saved.SavedRevision())
					require.Equal(t, expected.Nodes, saved.Nodes)
					require.Equal(t, expected.Edges, saved.Edges)
					require.Equal(t, expected.Description, saved.Description)
					rec := layoutE2EWaitTerminal(t, ds, saved.SavedRevision())
					require.JSONEq(t, encode(saved), string(rec.Manifest))
					require.Equal(t, saved.ID, rec.Destination.WorkflowID)
					require.Equal(t, workflow.Private(localDefaultUser), rec.Destination.Scope)
					require.Equal(t, name, rec.Destination.Name)
					require.Equal(t, count, layoutE2ECountRows(t, ds.db, name))
					positions := map[string]workflow.NodePosition{}
					for _, node := range saved.Nodes {
						if previous == nil {
							positions[node.ID] = *node.Position
						} else if pos, ok := previous.Presentation.Retained.Positions[node.ID]; ok {
							positions[node.ID] = pos
						}
					}
					require.Equal(t, positions, rec.Retained.Positions, "fallback must come from the previous saved display, filtered to surviving nodes")
					require.Equal(t, rec.Retained, saved.Presentation.Retained)
					if previous != nil {
						require.NotEqual(t, previous.SavedRevision(), saved.SavedRevision())
						require.Equal(t, previous.ID, saved.ID)
						require.Equal(t, workflow.SaveReasonUpdate, rec.Reason)
					} else {
						require.Equal(t, workflow.SaveReasonCreate, rec.Reason)
					}
					previous = saved
					return saved
				}

				writeManifest(true, original, true)
				assertCaptured(original, 1)
				var edited workflow.Workflow
				require.NoError(t, json.Unmarshal([]byte(encode(original)), &edited))
				group := &edited.Nodes[groupIndex]
				cfg := group.Config.Group
				cfg.Tasks[0], cfg.Tasks[1] = cfg.Tasks[1], cfg.Tasks[0] // author order is not key sort order
				cfg.CompletionRule = workflow.GroupRuleFirstSuccess
				group.Label = "并行 · ordered tasks"
				up := workflow.GroupUpsert{NodeID: group.ID, Label: group.Label, Config: *cfg, SuccessTarget: success, FailureTarget: failure}
				res := callMCPTool(t, server, ctx, "workflow_group_upsert", map[string]any{"name": name, "group": encode(up)})
				require.False(t, res.IsError, mcpText(t, res))
				// Upsert replaces outgoing edges: task edges in author order, then continuations.
				edges := []workflow.Edge{}
				for _, edge := range edited.Edges {
					if edge.Source != group.ID {
						edges = append(edges, edge)
					}
				}
				if form == workflow.GroupFormBranches {
					for _, task := range cfg.Tasks {
						edges = append(edges, workflow.Edge{Source: group.ID, Target: task.Entry, Output: workflow.GroupPortTask, Label: task.Key})
					}
				}
				edges = append(edges, workflow.Edge{Source: group.ID, Target: success, Output: workflow.GroupPortSuccess}, workflow.Edge{Source: group.ID, Target: failure, Output: workflow.GroupPortFailure})
				edited.Edges = edges
				assertCaptured(&edited, 2)

				res = callMCPTool(t, server, ctx, "workflow_group_delete", map[string]any{"name": name, "nodeId": group.ID})
				require.False(t, res.IsError, mcpText(t, res))
				groupID := group.ID
				edited.Nodes = append(edited.Nodes[:groupIndex], edited.Nodes[groupIndex+1:]...)
				edited.Edges = nil
				for _, edge := range edges {
					if edge.Source != groupID && edge.Target != groupID {
						edited.Edges = append(edited.Edges, edge)
					}
				}
				// Persisted JSON normalizes an empty edge array.
				if len(edited.Edges) == 0 {
					edited.Edges = []workflow.Edge{}
				}
				assertCaptured(&edited, 3)

				original.Description = "Full manifest import with exact group config"
				for i := range original.Nodes {
					original.Nodes[i].Position = &workflow.NodePosition{X: float64(900 + i), Y: float64(800 + i)}
				}
				writeManifest(false, original, true)
				assertCaptured(original, 4)
				before, err := os.ReadFile(e.privatePath(name))
				require.NoError(t, err)
				original.Nodes[groupIndex].Config.Group.Tasks = nil
				writeManifest(false, original, false)
				after, err := os.ReadFile(e.privatePath(name))
				require.NoError(t, err)
				require.Equal(t, before, after)
				require.Equal(t, 4, layoutE2ECountRows(t, ds.db, name))
				original.Name = "invalid-import"
				writeManifest(true, original, false)
				require.NoDirExists(t, filepath.Dir(e.privatePath(original.Name)))
				require.Zero(t, layoutE2ECountRows(t, ds.db, original.Name))
				entries, err := ds.layout.journal.Entries(ctx)
				require.NoError(t, err)
				require.Empty(t, entries, "rejected groups must leave no prepared revision")
			})
		}
	}
}
