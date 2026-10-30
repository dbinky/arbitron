package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/workflow"
)

// httpJSON sends body as JSON to the router and decodes a group-validation shaped
// response. It never calls require inside a goroutine.
func httpJSON(t *testing.T, method, url string, body any) (int, groupValidation) {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequest(method, url, bytes.NewReader(raw))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var v groupValidation
	_ = json.NewDecoder(resp.Body).Decode(&v)
	return resp.StatusCode, v
}

// Both validate endpoints (MCP + REST) read Deps.ProfileChecker/FileReader while
// the write gate reads the service's SetGroupCheckers pair. This proves the two
// sources agree end to end through the composition root: the same chat-kind
// resolver is refused with the same code by MCP validate, REST validate, MCP
// write, REST create and REST update — and an agent-kind resolver passes all of
// them. A drift (one side nil or stale) flips at least one assertion.
func TestGroupAuthoringE2E_ValidateAndWriteGatesAgreeOnCheckers(t *testing.T) {
	ds, workspaceDir := bootGroupAuthoringStack(t)
	ctx := adminCtxForLocalUser(t, ds)
	srv := newGroupMCPServer(ds)
	api := gsServeAPI(t, ds)

	// A valid agent-kind group is created through REST (the production router).
	status, _ := httpJSON(t, http.MethodPost, api.URL+"/api/v1/workflows", filesModeGroup("agree", "default-agent"))
	require.Equal(t, http.StatusCreated, status)

	// REST update to a chat-kind resolver: 422 with the profile-kind code, and
	// the stored definition is byte-identical afterwards.
	path := filepath.Join(privateDir(workspaceDir, "agree"), "workflow.json")
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	status, v := httpJSON(t, http.MethodPut, api.URL+"/api/v1/workflows/agree", filesModeGroup("agree", "default-chat"))
	assert.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Contains(t, groupCodes(v.Errors), "GROUP_RESOLVER_PROFILE_KIND")
	assert.NotContains(t, groupCodes(v.Errors), "GROUP_CHECKER_UNAVAILABLE")
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after, "a refused REST update leaves the folder untouched")

	// REST create of a chat-kind resolver: 422, nothing persisted.
	status, v = httpJSON(t, http.MethodPost, api.URL+"/api/v1/workflows", filesModeGroup("agree-chat", "default-chat"))
	assert.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Contains(t, groupCodes(v.Errors), "GROUP_RESOLVER_PROFILE_KIND")
	assert.Equal(t, 0, registryRows(t, ds, "agree-chat"))

	// A chat-kind definition that reached disk (bypassing the gate) is reported
	// identically by both validate endpoints.
	bad, err := json.Marshal(filesModeGroup("agree-bad", "default-chat"))
	require.NoError(t, err)
	writeWorkflow(t, workspaceDir, "agree-bad", string(bad))

	res := callMCPTool(t, srv, ctx, "workflow_validate", map[string]any{"name": "agree-bad"})
	require.False(t, res.IsError, mcpText(t, res))
	mcpV := decodeGroupValidation(t, mcpText(t, res))
	assert.False(t, mcpV.Valid)

	status, restV := httpJSON(t, http.MethodPost, api.URL+"/api/v1/workflows/agree-bad/validate", struct{}{})
	require.Equal(t, http.StatusOK, status)
	assert.False(t, restV.Valid)

	assert.Contains(t, groupCodes(mcpV.Errors), "GROUP_RESOLVER_PROFILE_KIND")
	assert.ElementsMatch(t, groupCodes(mcpV.Errors), groupCodes(restV.Errors), "MCP and REST validate agree")

	// ...and the write gate (the service's own checkers) gives the same group verdict.
	err = ds.workflowSvc.Update(ctx, "agree-bad", filesModeGroup("agree-bad", "default-chat"))
	var gve *workflow.GroupValidationError
	require.ErrorAs(t, err, &gve)
	assert.ElementsMatch(t, groupCodes(restV.Errors), groupCodes(gve.Errors), "write gate agrees with validate")
}

// resolverPromptFile is resolved through the REAL filesystem FileReader in the
// author's own workspace, and workflow_group_delete reaches the registry/folder
// sink. Authoring walk: create -> upsert with a missing prompt file (refused,
// definition untouched) -> write the file -> upsert accepted -> get -> delete.
func TestGroupAuthoringE2E_PromptFileAndDeleteThroughCompositionRoot(t *testing.T) {
	t.Parallel()
	ds, workspaceDir := bootGroupAuthoringStack(t)
	ctx := adminCtxForLocalUser(t, ds)
	srv := newGroupMCPServer(ds)

	base := `{"name":"pf","description":"prompt file","author":"e2e","inputs":[],
 "nodes":[
  {"id":"a1","type":"transform","config":{"operations":[{"variable":"a","operation":"set","value":1}]}},
  {"id":"done","type":"transform","entrypoint":true,"config":{"operations":[{"variable":"ok","operation":"set","value":true}]}},
  {"id":"failed","type":"transform","config":{"operations":[{"variable":"ok","operation":"set","value":false}]}}
 ],"edges":[]}`
	res := callMCPTool(t, srv, ctx, "workflow_create", map[string]any{"manifest": base})
	require.False(t, res.IsError, mcpText(t, res))

	group := func(promptFile string) string {
		return `{"nodeId":"fan","entrypoint":true,
 "config":{"form":"branches","tasks":[{"key":"a","entry":"a1"}],
   "completionRule":"all_succeed","unfinishedBehavior":"cancel_unfinished","gatherMode":"results_and_files",
   "resolverProfile":"default-agent","resolverPromptFile":"` + promptFile + `"},
 "successTarget":"done","failureTarget":"failed"}`
	}
	defPath := filepath.Join(privateDir(workspaceDir, "pf"), "workflow.json")
	before, err := os.ReadFile(defPath)
	require.NoError(t, err)

	// Missing prompt file: refused before the folder is written.
	res = callMCPTool(t, srv, ctx, "workflow_group_upsert", map[string]any{"name": "pf", "group": group("prompts/resolve.md")})
	assert.True(t, res.IsError)
	assert.Contains(t, groupCodes(decodeGroupValidation(t, mcpText(t, res)).Errors), "GROUP_MISSING_PROMPT_FILE")
	after, err := os.ReadFile(defPath)
	require.NoError(t, err)
	assert.Equal(t, before, after)

	// A path that escapes the workflow folder is refused even if the target exists.
	res = callMCPTool(t, srv, ctx, "workflow_group_upsert", map[string]any{"name": "pf", "group": group("../pf/workflow.json")})
	assert.True(t, res.IsError)
	assert.Contains(t, groupCodes(decodeGroupValidation(t, mcpText(t, res)).Errors), "GROUP_MISSING_PROMPT_FILE")

	// Write the file through MCP; the real FileReader now finds it.
	res = callMCPTool(t, srv, ctx, "workflow_write_file", map[string]any{"name": "pf", "path": "prompts/resolve.md", "content": "resolve conflicts"})
	require.False(t, res.IsError, mcpText(t, res))
	res = callMCPTool(t, srv, ctx, "workflow_group_upsert", map[string]any{"name": "pf", "group": group("prompts/resolve.md")})
	require.False(t, res.IsError, mcpText(t, res))

	res = callMCPTool(t, srv, ctx, "workflow_group_get", map[string]any{"name": "pf"})
	require.False(t, res.IsError, mcpText(t, res))
	var list []workflow.GroupView
	require.NoError(t, json.Unmarshal([]byte(mcpText(t, res)), &list))
	require.Len(t, list, 1)
	assert.Equal(t, "prompts/resolve.md", list[0].Config.ResolverPromptFile)

	// Delete removes the group node from the stored definition.
	res = callMCPTool(t, srv, ctx, "workflow_group_delete", map[string]any{"name": "pf", "nodeId": "fan"})
	require.False(t, res.IsError, mcpText(t, res))
	data, err := os.ReadFile(defPath)
	require.NoError(t, err)
	var stored workflow.Workflow
	require.NoError(t, json.Unmarshal(data, &stored))
	for _, n := range stored.Nodes {
		assert.NotEqual(t, "fan", n.ID, "group node removed from the stored definition")
	}
	assert.Empty(t, stored.Edges)
	res = callMCPTool(t, srv, ctx, "workflow_group_get", map[string]any{"name": "pf"})
	require.False(t, res.IsError, mcpText(t, res))
	list = nil
	require.NoError(t, json.Unmarshal([]byte(mcpText(t, res)), &list))
	assert.Empty(t, list)

	// Deleting a node that is not a group is refused and the definition is untouched.
	res = callMCPTool(t, srv, ctx, "workflow_group_delete", map[string]any{"name": "pf", "nodeId": "fan"})
	assert.True(t, res.IsError, "deleting an absent group is an error")
	assert.Equal(t, 1, registryRows(t, ds, "pf"))
}
