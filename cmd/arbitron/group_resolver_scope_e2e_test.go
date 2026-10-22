package main

import (
	"errors"
	"github.com/arbitron/arbitron/domain/workflow"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/engine"
)

// Seam Area 11 ↔ Area 3: resolverPromptFile is validated when authored and read
// again by the resolver at runtime in the RUN OWNER's scope. An admin editing a
// shared workflow in place is authoring for other runners, so a prompt file only
// the admin's private partition holds (here: a manifest-less private folder,
// where workflow_write_file lands because it checks the directory, not the
// manifest) must be refused, not accepted and then fail every other user's
// conflict resolution. Real filesystem, real FileReader, MCP through initDomain.
func TestGroupAuthoringE2E_SharedEditPromptMustBeVisibleToEveryRunner(t *testing.T) {
	ds, workspaceDir := bootGroupAuthoringStack(t)
	ctx := adminCtxForLocalUser(t, ds)
	srv := newGroupMCPServer(ds)

	sharedDir := filepath.Join(workspaceDir, "shared", "shared-pf")
	require.NoError(t, os.MkdirAll(sharedDir, 0o700))
	defPath := filepath.Join(sharedDir, "workflow.json")
	require.NoError(t, os.WriteFile(defPath, []byte(`{"name":"shared-pf","description":"shared","author":"e2e","inputs":[],
 "nodes":[
  {"id":"a1","type":"transform","config":{"operations":[{"variable":"a","operation":"set","value":1}]}},
  {"id":"done","type":"transform","entrypoint":true,"config":{"operations":[{"variable":"ok","operation":"set","value":true}]}},
  {"id":"failed","type":"transform","config":{"operations":[{"variable":"ok","operation":"set","value":false}]}}
 ],"edges":[]}`), 0o600))
	// A folder written straight to disk after boot has no registry identity, and
	// revision-stamped writes resolve their target through the registry. Run the
	// same reconciliation boot runs so the shared workflow is addressable.
	// The stack's deliberately invalid fixtures are reported, never registered.
	if err := ds.workflowSvc.BackfillRegistry(ctx); err != nil {
		require.ErrorIs(t, err, workflow.ErrGroupInvalid)
	}
	// A real shared workflow is published; revision-stamped shared edits verify
	// that publication record.
	require.NoError(t, ds.publishedRepo.Upsert(ctx, "shared-pf", localDefaultUser, "shared-pf", workflow.VisibilityPublic, ""))
	require.NoError(t, os.MkdirAll(privateDir(workspaceDir, "shared-pf"), 0o700))
	before, err := os.ReadFile(defPath)
	require.NoError(t, err)

	group := `{"nodeId":"fan","entrypoint":true,
 "config":{"form":"branches","tasks":[{"key":"a","entry":"a1"}],
   "completionRule":"all_succeed","unfinishedBehavior":"cancel_unfinished","gatherMode":"results_and_files",
   "resolverProfile":"default-agent","resolverPromptFile":"prompts/resolve.md"},
 "successTarget":"done","failureTarget":"failed"}`

	// The admin writes the prompt; it lands in the admin's private partition.
	res := callMCPTool(t, srv, ctx, "workflow_write_file", map[string]any{"name": "shared-pf", "path": "prompts/resolve.md", "content": "resolve conflicts"})
	require.False(t, res.IsError, mcpText(t, res))
	require.FileExists(t, filepath.Join(privateDir(workspaceDir, "shared-pf"), "prompts", "resolve.md"))

	// Runtime truth for that file: the admin's resolver reads it, another
	// runner's resolver (run owner = that user) cannot.
	reader := filesystem.NewFileReader(workspaceDir)
	data := engine.GroupResolverPromptData{TaskKey: "a", WorkDir: t.TempDir(), ConflictPaths: []string{"f.txt"}}
	_, err = engine.LoadGroupResolverPrompt(reader, localDefaultUser, "shared-pf", "prompts/resolve.md", data)
	require.NoError(t, err)
	_, err = engine.LoadGroupResolverPrompt(reader, groupOtherUser, "shared-pf", "prompts/resolve.md", data)
	require.True(t, errors.Is(err, engine.ErrGroupResolverPrompt), "another runner fails closed, never falls back: %v", err)

	// So the shared edit is refused with authoring feedback, nothing persisted.
	res = callMCPTool(t, srv, ctx, "workflow_group_upsert", map[string]any{"name": "shared-pf", "group": group})
	require.True(t, res.IsError, "admin-private prompt must not validate a shared edit")
	assert.Contains(t, groupCodes(decodeGroupValidation(t, mcpText(t, res)).Errors), "GROUP_MISSING_PROMPT_FILE")
	after, err := os.ReadFile(defPath)
	require.NoError(t, err)
	assert.Equal(t, before, after)

	// Once the prompt is in the shared folder every runner reads it, and the
	// same edit is accepted into the shared definition.
	require.NoError(t, os.MkdirAll(filepath.Join(sharedDir, "prompts"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(sharedDir, "prompts", "resolve.md"), []byte("shared resolve"), 0o600))
	res = callMCPTool(t, srv, ctx, "workflow_group_upsert", map[string]any{"name": "shared-pf", "group": group})
	require.False(t, res.IsError, mcpText(t, res))
	stored, err := os.ReadFile(defPath)
	require.NoError(t, err)
	assert.Contains(t, string(stored), `"resolverPromptFile": "prompts/resolve.md"`)
	prompt, err := engine.LoadGroupResolverPrompt(reader, groupOtherUser, "shared-pf", "prompts/resolve.md", data)
	require.NoError(t, err)
	assert.Contains(t, prompt, "shared resolve")
}
