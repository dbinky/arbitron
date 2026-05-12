package filesystem_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testdataPath(t *testing.T) string {
	t.Helper()
	// testdata is relative to this test file
	dir, err := filepath.Abs("testdata")
	require.NoError(t, err)
	return dir
}

func TestWorkflowRepo_Load_ValidWorkflow(t *testing.T) {
	repo := filesystem.NewWorkflowRepo(testdataPath(t))

	wf, err := repo.Load(context.Background(),"valid-workflow")
	require.NoError(t, err)
	require.NotNil(t, wf)

	// Top-level fields
	assert.Equal(t, "valid-workflow", wf.Name)
	assert.Equal(t, "A test workflow with one transform node", wf.Description)
	assert.Equal(t, "test-author", wf.Author)

	// Default error behavior
	assert.Equal(t, "retry", wf.DefaultErrorBehavior.Type)
	assert.Equal(t, 3, wf.DefaultErrorBehavior.Retries)
	assert.Equal(t, 5, wf.DefaultErrorBehavior.RetryDelaySeconds)

	// Inputs
	require.Len(t, wf.Inputs, 1)
	assert.Equal(t, "inputText", wf.Inputs[0].Name)
	assert.Equal(t, "string", wf.Inputs[0].Type)
	assert.Equal(t, "The text to transform", wf.Inputs[0].Description)
	assert.True(t, wf.Inputs[0].Required)

	// Nodes
	require.Len(t, wf.Nodes, 1)
	node := wf.Nodes[0]
	assert.Equal(t, "transform-1", node.ID)
	assert.Equal(t, workflow.NodeTypeTransform, node.Type)
	assert.Equal(t, "Uppercase Transform", node.Label)
	assert.Equal(t, "Transforms input to uppercase", node.Description)
	assert.True(t, node.Entrypoint)

	// Node config operations
	require.Len(t, node.Config.Operations, 1)
	op := node.Config.Operations[0]
	assert.Equal(t, "outputText", op.Variable)
	assert.Equal(t, "uppercase", op.Operation)
	assert.Equal(t, "{{ inputText }}", op.Value)

	// Edges
	assert.Empty(t, wf.Edges)
}

func TestWorkflowRepo_Load_MissingDirectory(t *testing.T) {
	repo := filesystem.NewWorkflowRepo(testdataPath(t))

	wf, err := repo.Load(context.Background(),"nonexistent-workflow")
	assert.Nil(t, wf)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nonexistent-workflow")
}

func TestWorkflowRepo_Load_MissingManifest(t *testing.T) {
	repo := filesystem.NewWorkflowRepo(testdataPath(t))

	wf, err := repo.Load(context.Background(),"empty-dir")
	assert.Nil(t, wf)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty-dir")
}

func TestWorkflowRepo_Load_MalformedJSON(t *testing.T) {
	repo := filesystem.NewWorkflowRepo(testdataPath(t))

	wf, err := repo.Load(context.Background(),"malformed-json")
	assert.Nil(t, wf)
	assert.Error(t, err)
}

func TestWorkflowRepo_List(t *testing.T) {
	repo := filesystem.NewWorkflowRepo(testdataPath(t))

	summaries, err := repo.List(context.Background())
	require.NoError(t, err)

	// Should find all-node-types and valid-workflow; skip empty-dir and malformed-json.
	require.Len(t, summaries, 2)

	names := make([]string, len(summaries))
	for i, s := range summaries {
		names[i] = s.Name
	}
	assert.Contains(t, names, "valid-workflow")
	assert.Contains(t, names, "all-node-types")
}

func TestWorkflowRepo_Load_AllNodeTypes(t *testing.T) {
	repo := filesystem.NewWorkflowRepo(testdataPath(t))

	wf, err := repo.Load(context.Background(),"all-node-types")
	require.NoError(t, err)
	require.NotNil(t, wf)
	require.Len(t, wf.Nodes, 5)

	byID := make(map[string]workflow.Node, 5)
	for _, n := range wf.Nodes {
		byID[n.ID] = n
	}

	// LLM node
	llm := byID["llm-node"]
	assert.Equal(t, workflow.NodeTypeLLMCall, llm.Type)
	assert.Equal(t, "prompt.md", llm.Config.TemplateFile)
	assert.Equal(t, "claude-sonnet-4-6", llm.Config.Model)
	assert.Equal(t, 1000, llm.Config.MaxTokens)
	assert.Equal(t, 0.7, llm.Config.Temperature)
	assert.NotNil(t, llm.Config.OutputSchema)

	// Script node
	script := byID["script-node"]
	assert.Equal(t, workflow.NodeTypeScript, script.Type)
	assert.Equal(t, "run.sh", script.Config.ScriptFile)
	assert.Equal(t, "bash", script.Config.Interpreter)
	assert.Equal(t, []string{"--input", "{{llm-node.result}}"}, script.Config.Args)
	assert.Equal(t, 30, script.Config.TimeoutSeconds)

	// Tool call node
	tool := byID["tool-node"]
	assert.Equal(t, workflow.NodeTypeToolCall, tool.Type)
	assert.Equal(t, "git", tool.Config.Command)
	assert.Equal(t, []string{"status"}, tool.Config.Args)
	assert.Equal(t, 10, tool.Config.TimeoutSeconds)

	// Conditional node
	cond := byID["cond-node"]
	assert.Equal(t, workflow.NodeTypeConditional, cond.Type)
	require.Len(t, cond.Config.Conditions, 1)
	assert.Equal(t, "score", cond.Config.Conditions[0].Field)
	assert.Equal(t, "gt", cond.Config.Conditions[0].Operator)
	assert.Equal(t, "high", cond.Config.Conditions[0].Output)
	assert.Equal(t, "low", cond.Config.DefaultOutput)

	// Transform node
	transform := byID["transform-node"]
	assert.Equal(t, workflow.NodeTypeTransform, transform.Type)
	require.Len(t, transform.Config.Operations, 1)
	assert.Equal(t, "result", transform.Config.Operations[0].Variable)
	assert.Equal(t, "set", transform.Config.Operations[0].Operation)
}

func TestWorkflowRepo_List_EmptyWorkspace(t *testing.T) {
	// Create a temporary empty directory
	tmpDir, err := os.MkdirTemp("", "arbitron-test-empty-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	repo := filesystem.NewWorkflowRepo(tmpDir)

	summaries, err := repo.List(context.Background())
	require.NoError(t, err)
	assert.Empty(t, summaries)
}

func TestWorkflowRepo_Load_PathTraversalRejected(t *testing.T) {
	repo := filesystem.NewWorkflowRepo(testdataPath(t))

	// A name like "../sibling" could escape the workspace root.
	_, err := repo.Load(context.Background(),"../sibling")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// ---------------------------------------------------------------------------
// Helper: create a temp dir for write tests
// ---------------------------------------------------------------------------

func tmpWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return dir
}

func sampleWorkflow(name string) *workflow.Workflow {
	return &workflow.Workflow{
		Name:        name,
		Description: "test workflow",
		Author:      "tester",
		Nodes: []workflow.Node{
			{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true, Config: workflow.NodeConfig{ScriptFile: "run.sh"}},
		},
	}
}

// ---------------------------------------------------------------------------
// Tests: Create
// ---------------------------------------------------------------------------

func TestWorkflowRepo_Create_HappyPath(t *testing.T) {
	dir := tmpWorkspace(t)
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	wf := sampleWorkflow("my-workflow")
	err := repo.Create(ctx, "my-workflow", wf)
	require.NoError(t, err)

	// Verify the workflow.json was written
	data, err := os.ReadFile(filepath.Join(dir, "my-workflow", "workflow.json"))
	require.NoError(t, err)

	var loaded workflow.Workflow
	require.NoError(t, json.Unmarshal(data, &loaded))
	assert.Equal(t, "my-workflow", loaded.Name)
	assert.Equal(t, "test workflow", loaded.Description)
	assert.Equal(t, "tester", loaded.Author)
	assert.Len(t, loaded.Nodes, 1)
}

func TestWorkflowRepo_Create_AlreadyExists(t *testing.T) {
	dir := tmpWorkspace(t)
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	wf := sampleWorkflow("existing")
	require.NoError(t, repo.Create(ctx, "existing", wf))

	// Creating again should fail
	err := repo.Create(ctx, "existing", wf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestWorkflowRepo_Create_PathTraversalRejected(t *testing.T) {
	dir := tmpWorkspace(t)
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	wf := sampleWorkflow("evil")
	err := repo.Create(ctx, "../escape", wf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid workflow name")
}

func TestWorkflowRepo_Create_LoadRoundTrip(t *testing.T) {
	dir := tmpWorkspace(t)
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	wf := sampleWorkflow("roundtrip")
	require.NoError(t, repo.Create(ctx, "roundtrip", wf))

	loaded, err := repo.Load(ctx, "roundtrip")
	require.NoError(t, err)
	assert.Equal(t, wf.Name, loaded.Name)
	assert.Equal(t, wf.Description, loaded.Description)
	assert.Equal(t, wf.Author, loaded.Author)
	assert.Len(t, loaded.Nodes, len(wf.Nodes))
}

func TestWorkflowRepo_Create_SpecialCharsInName(t *testing.T) {
	dir := tmpWorkspace(t)
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	// Workflow names with dashes, numbers, underscores should be fine
	wf := sampleWorkflow("my_workflow-2")
	err := repo.Create(ctx, "my_workflow-2", wf)
	require.NoError(t, err)

	loaded, err := repo.Load(ctx, "my_workflow-2")
	require.NoError(t, err)
	assert.Equal(t, "my_workflow-2", loaded.Name)
}

// ---------------------------------------------------------------------------
// Tests: Update
// ---------------------------------------------------------------------------

func TestWorkflowRepo_Update_HappyPath(t *testing.T) {
	dir := tmpWorkspace(t)
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	wf := sampleWorkflow("updatable")
	require.NoError(t, repo.Create(ctx, "updatable", wf))

	// Modify the workflow
	wf.Description = "updated description"
	wf.Author = "new-author"
	err := repo.Update(ctx, "updatable", wf)
	require.NoError(t, err)

	// Verify changes persisted
	loaded, err := repo.Load(ctx, "updatable")
	require.NoError(t, err)
	assert.Equal(t, "updated description", loaded.Description)
	assert.Equal(t, "new-author", loaded.Author)
}

func TestWorkflowRepo_Update_NotFound(t *testing.T) {
	dir := tmpWorkspace(t)
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	wf := sampleWorkflow("ghost")
	err := repo.Update(ctx, "ghost", wf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestWorkflowRepo_Update_PathTraversalRejected(t *testing.T) {
	dir := tmpWorkspace(t)
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	wf := sampleWorkflow("evil")
	err := repo.Update(ctx, "../escape", wf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid workflow name")
}

func TestWorkflowRepo_Update_AtomicWrite(t *testing.T) {
	dir := tmpWorkspace(t)
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	wf := sampleWorkflow("atomic")
	require.NoError(t, repo.Create(ctx, "atomic", wf))

	// Update should not leave temp files behind
	wf.Description = "atomically updated"
	require.NoError(t, repo.Update(ctx, "atomic", wf))

	// Verify no .tmp files remain
	entries, err := os.ReadDir(filepath.Join(dir, "atomic"))
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, filepath.Ext(e.Name()) == ".tmp", "temp file should not remain: %s", e.Name())
	}

	// Verify the update landed
	loaded, err := repo.Load(ctx, "atomic")
	require.NoError(t, err)
	assert.Equal(t, "atomically updated", loaded.Description)
}

// ---------------------------------------------------------------------------
// Tests: Delete
// ---------------------------------------------------------------------------

func TestWorkflowRepo_Delete_HappyPath(t *testing.T) {
	dir := tmpWorkspace(t)
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	wf := sampleWorkflow("doomed")
	require.NoError(t, repo.Create(ctx, "doomed", wf))

	// Verify it exists
	_, err := repo.Load(ctx, "doomed")
	require.NoError(t, err)

	// Delete
	err = repo.Delete(ctx, "doomed")
	require.NoError(t, err)

	// Verify it's gone
	_, err = repo.Load(ctx, "doomed")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	// Verify directory is removed
	_, err = os.Stat(filepath.Join(dir, "doomed"))
	assert.True(t, os.IsNotExist(err))
}

func TestWorkflowRepo_Delete_NotFound(t *testing.T) {
	dir := tmpWorkspace(t)
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	err := repo.Delete(ctx, "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestWorkflowRepo_Delete_PathTraversalRejected(t *testing.T) {
	dir := tmpWorkspace(t)
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	err := repo.Delete(ctx, "../escape")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid workflow name")
}

func TestWorkflowRepo_Delete_WithExtraFiles(t *testing.T) {
	dir := tmpWorkspace(t)
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	wf := sampleWorkflow("with-files")
	require.NoError(t, repo.Create(ctx, "with-files", wf))

	// Add extra files
	require.NoError(t, os.WriteFile(filepath.Join(dir, "with-files", "script.sh"), []byte("#!/bin/bash"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "with-files", "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "with-files", "sub", "data.txt"), []byte("data"), 0o644))

	// Delete should remove everything
	err := repo.Delete(ctx, "with-files")
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(dir, "with-files"))
	assert.True(t, os.IsNotExist(err))
}

// ---------------------------------------------------------------------------
// Tests: WriteFile
// ---------------------------------------------------------------------------

func TestWorkflowRepo_WriteFile_HappyPath(t *testing.T) {
	dir := tmpWorkspace(t)
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, "wf1", sampleWorkflow("wf1")))

	err := repo.WriteFile(ctx, "wf1", "script.sh", []byte("#!/bin/bash\necho hi"))
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, "wf1", "script.sh"))
	require.NoError(t, err)
	assert.Equal(t, "#!/bin/bash\necho hi", string(data))
}

func TestWorkflowRepo_WriteFile_NestedDirs(t *testing.T) {
	dir := tmpWorkspace(t)
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, "wf2", sampleWorkflow("wf2")))

	err := repo.WriteFile(ctx, "wf2", "templates/prompt.md", []byte("# Hello"))
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, "wf2", "templates", "prompt.md"))
	require.NoError(t, err)
	assert.Equal(t, "# Hello", string(data))
}

func TestWorkflowRepo_WriteFile_EmptyContent(t *testing.T) {
	dir := tmpWorkspace(t)
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, "wf3", sampleWorkflow("wf3")))

	err := repo.WriteFile(ctx, "wf3", "empty.txt", []byte{})
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, "wf3", "empty.txt"))
	require.NoError(t, err)
	assert.Empty(t, data)
}

func TestWorkflowRepo_WriteFile_WorkflowNotFound(t *testing.T) {
	dir := tmpWorkspace(t)
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	err := repo.WriteFile(ctx, "nonexistent", "file.txt", []byte("data"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestWorkflowRepo_WriteFile_PathTraversalWorkflowName(t *testing.T) {
	dir := tmpWorkspace(t)
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	err := repo.WriteFile(ctx, "../escape", "file.txt", []byte("data"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid workflow name")
}

func TestWorkflowRepo_WriteFile_PathTraversalRelativePath(t *testing.T) {
	dir := tmpWorkspace(t)
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, "wf4", sampleWorkflow("wf4")))

	err := repo.WriteFile(ctx, "wf4", "../escape.txt", []byte("evil"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid file path")

	// Also try double traversal
	err = repo.WriteFile(ctx, "wf4", "sub/../../escape.txt", []byte("evil"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid file path")
}

func TestWorkflowRepo_WriteFile_OverwriteExisting(t *testing.T) {
	dir := tmpWorkspace(t)
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, "wf5", sampleWorkflow("wf5")))

	// Write once
	require.NoError(t, repo.WriteFile(ctx, "wf5", "data.txt", []byte("v1")))

	// Overwrite
	require.NoError(t, repo.WriteFile(ctx, "wf5", "data.txt", []byte("v2")))

	data, err := os.ReadFile(filepath.Join(dir, "wf5", "data.txt"))
	require.NoError(t, err)
	assert.Equal(t, "v2", string(data))
}

// ---------------------------------------------------------------------------
// Tests: DeleteFile
// ---------------------------------------------------------------------------

func TestWorkflowRepo_DeleteFile_HappyPath(t *testing.T) {
	dir := tmpWorkspace(t)
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, "wf-del", sampleWorkflow("wf-del")))
	require.NoError(t, repo.WriteFile(ctx, "wf-del", "script.sh", []byte("#!/bin/bash")))

	err := repo.DeleteFile(ctx, "wf-del", "script.sh")
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(dir, "wf-del", "script.sh"))
	assert.True(t, os.IsNotExist(err))
}

func TestWorkflowRepo_DeleteFile_FileNotFound(t *testing.T) {
	dir := tmpWorkspace(t)
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, "wf-del2", sampleWorkflow("wf-del2")))

	err := repo.DeleteFile(ctx, "wf-del2", "nonexistent.txt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestWorkflowRepo_DeleteFile_WorkflowNotFound(t *testing.T) {
	dir := tmpWorkspace(t)
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	err := repo.DeleteFile(ctx, "ghost-wf", "file.txt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestWorkflowRepo_DeleteFile_PathTraversalWorkflowName(t *testing.T) {
	dir := tmpWorkspace(t)
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	err := repo.DeleteFile(ctx, "../escape", "file.txt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid workflow name")
}

func TestWorkflowRepo_DeleteFile_PathTraversalRelativePath(t *testing.T) {
	dir := tmpWorkspace(t)
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, "wf-del3", sampleWorkflow("wf-del3")))

	err := repo.DeleteFile(ctx, "wf-del3", "../workflow.json")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid file path")
}

func TestWorkflowRepo_DeleteFile_LeavesManifestIntact(t *testing.T) {
	dir := tmpWorkspace(t)
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, "wf-del4", sampleWorkflow("wf-del4")))
	require.NoError(t, repo.WriteFile(ctx, "wf-del4", "only-file.txt", []byte("content")))

	// Delete the only non-manifest file
	require.NoError(t, repo.DeleteFile(ctx, "wf-del4", "only-file.txt"))

	// Manifest should still be there
	_, err := repo.Load(ctx, "wf-del4")
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Tests: ListFiles
// ---------------------------------------------------------------------------

func TestWorkflowRepo_ListFiles_HappyPath(t *testing.T) {
	dir := tmpWorkspace(t)
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, "wf-list", sampleWorkflow("wf-list")))
	require.NoError(t, repo.WriteFile(ctx, "wf-list", "script.sh", []byte("#!/bin/bash")))
	require.NoError(t, repo.WriteFile(ctx, "wf-list", "templates/prompt.md", []byte("# Hi")))

	files, err := repo.ListFiles(ctx, "wf-list")
	require.NoError(t, err)

	// Should include script.sh and templates/prompt.md but NOT workflow.json
	assert.Contains(t, files, "script.sh")
	assert.Contains(t, files, "templates/prompt.md")
	assert.NotContains(t, files, "workflow.json")
	assert.Len(t, files, 2)
}

func TestWorkflowRepo_ListFiles_ExcludesWorkflowJSON(t *testing.T) {
	dir := tmpWorkspace(t)
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, "wf-list2", sampleWorkflow("wf-list2")))

	// Only workflow.json exists
	files, err := repo.ListFiles(ctx, "wf-list2")
	require.NoError(t, err)
	assert.Empty(t, files)
}

func TestWorkflowRepo_ListFiles_WorkflowNotFound(t *testing.T) {
	dir := tmpWorkspace(t)
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	_, err := repo.ListFiles(ctx, "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestWorkflowRepo_ListFiles_PathTraversalRejected(t *testing.T) {
	dir := tmpWorkspace(t)
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	_, err := repo.ListFiles(ctx, "../escape")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid workflow name")
}

func TestWorkflowRepo_ListFiles_ForwardSlashes(t *testing.T) {
	dir := tmpWorkspace(t)
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, "wf-list3", sampleWorkflow("wf-list3")))
	require.NoError(t, repo.WriteFile(ctx, "wf-list3", "a/b/c/deep.txt", []byte("deep")))

	files, err := repo.ListFiles(ctx, "wf-list3")
	require.NoError(t, err)
	require.Len(t, files, 1)
	// Paths must use forward slashes regardless of OS
	assert.Equal(t, "a/b/c/deep.txt", files[0])
}

func TestWorkflowRepo_WriteFile_AtomicWrite(t *testing.T) {
	dir := tmpWorkspace(t)
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, "wf-atomic", sampleWorkflow("wf-atomic")))

	// Overwrite to trigger the atomic path
	require.NoError(t, repo.WriteFile(ctx, "wf-atomic", "data.txt", []byte("v1")))
	require.NoError(t, repo.WriteFile(ctx, "wf-atomic", "data.txt", []byte("v2")))

	// No .tmp files should remain after a successful write
	entries, err := os.ReadDir(filepath.Join(dir, "wf-atomic"))
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, filepath.Ext(e.Name()) == ".tmp", "temp file should not remain: %s", e.Name())
	}

	data, err := os.ReadFile(filepath.Join(dir, "wf-atomic", "data.txt"))
	require.NoError(t, err)
	assert.Equal(t, "v2", string(data))
}

func TestWorkflowRepo_ListFiles_NestedDirs(t *testing.T) {
	dir := tmpWorkspace(t)
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, "wf-list4", sampleWorkflow("wf-list4")))
	require.NoError(t, repo.WriteFile(ctx, "wf-list4", "scripts/run.sh", []byte("run")))
	require.NoError(t, repo.WriteFile(ctx, "wf-list4", "scripts/setup.sh", []byte("setup")))
	require.NoError(t, repo.WriteFile(ctx, "wf-list4", "templates/main.md", []byte("main")))

	files, err := repo.ListFiles(ctx, "wf-list4")
	require.NoError(t, err)
	assert.Len(t, files, 3)
	assert.Contains(t, files, "scripts/run.sh")
	assert.Contains(t, files, "scripts/setup.sh")
	assert.Contains(t, files, "templates/main.md")
}
