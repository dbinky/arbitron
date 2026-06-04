package filesystem_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testScope is the default Private scope used across most adapter tests.
var testOwner = uuid.MustParse("11111111-1111-1111-1111-111111111111")

func testScope() workflow.Scope { return workflow.Private(testOwner) }

func testdataPath(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("testdata")
	require.NoError(t, err)
	return dir
}

// repoWithTestdata builds a repo over a fresh tempdir and copies the flat
// testdata fixtures into the given scope's root so the read-oriented tests
// (which rely on the canned fixtures) work against the scope-aware layout.
func repoWithTestdata(t *testing.T, scope workflow.Scope) *filesystem.WorkflowRepo {
	t.Helper()
	base := t.TempDir()
	repo := filesystem.NewWorkflowRepo(base)

	root := scopeRootFor(t, base, scope)
	require.NoError(t, os.MkdirAll(root, 0o755))
	copyTree(t, testdataPath(t), root)
	return repo
}

func scopeRootFor(t *testing.T, base string, scope workflow.Scope) string {
	t.Helper()
	switch scope.Kind {
	case workflow.ScopePrivate:
		return filepath.Join(base, "users", scope.OwnerID.String())
	case workflow.ScopeShared:
		return filepath.Join(base, "shared")
	default:
		t.Fatalf("unknown scope kind %v", scope.Kind)
		return ""
	}
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	entries, err := os.ReadDir(src)
	require.NoError(t, err)
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		if e.IsDir() {
			require.NoError(t, os.MkdirAll(d, 0o755))
			copyTree(t, s, d)
			continue
		}
		in, err := os.Open(s)
		require.NoError(t, err)
		out, err := os.Create(d)
		require.NoError(t, err)
		_, err = io.Copy(out, in)
		require.NoError(t, err)
		require.NoError(t, in.Close())
		require.NoError(t, out.Close())
	}
}

func TestWorkflowRepo_Load_ValidWorkflow(t *testing.T) {
	repo := repoWithTestdata(t, testScope())

	wf, err := repo.Load(context.Background(), testScope(), "valid-workflow")
	require.NoError(t, err)
	require.NotNil(t, wf)

	assert.Equal(t, "valid-workflow", wf.Name)
	assert.Equal(t, "A test workflow with one transform node", wf.Description)
	assert.Equal(t, "test-author", wf.Author)

	assert.Equal(t, "retry", wf.DefaultErrorBehavior.Type)
	assert.Equal(t, 3, wf.DefaultErrorBehavior.Retries)
	assert.Equal(t, 5, wf.DefaultErrorBehavior.RetryDelaySeconds)

	require.Len(t, wf.Inputs, 1)
	assert.Equal(t, "inputText", wf.Inputs[0].Name)
	assert.Equal(t, "string", wf.Inputs[0].Type)
	assert.Equal(t, "The text to transform", wf.Inputs[0].Description)
	assert.True(t, wf.Inputs[0].Required)

	require.Len(t, wf.Nodes, 1)
	node := wf.Nodes[0]
	assert.Equal(t, "transform-1", node.ID)
	assert.Equal(t, workflow.NodeTypeTransform, node.Type)
	assert.Equal(t, "Uppercase Transform", node.Label)
	assert.Equal(t, "Transforms input to uppercase", node.Description)
	assert.True(t, node.Entrypoint)

	require.Len(t, node.Config.Operations, 1)
	op := node.Config.Operations[0]
	assert.Equal(t, "outputText", op.Variable)
	assert.Equal(t, "uppercase", op.Operation)
	assert.Equal(t, "{{ inputText }}", op.Value)

	assert.Empty(t, wf.Edges)
}

func TestWorkflowRepo_Load_MissingDirectory(t *testing.T) {
	repo := repoWithTestdata(t, testScope())

	wf, err := repo.Load(context.Background(), testScope(), "nonexistent-workflow")
	assert.Nil(t, wf)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nonexistent-workflow")
}

func TestWorkflowRepo_Load_MissingManifest(t *testing.T) {
	repo := repoWithTestdata(t, testScope())

	wf, err := repo.Load(context.Background(), testScope(), "empty-dir")
	assert.Nil(t, wf)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty-dir")
}

func TestWorkflowRepo_Load_MalformedJSON(t *testing.T) {
	repo := repoWithTestdata(t, testScope())

	wf, err := repo.Load(context.Background(), testScope(), "malformed-json")
	assert.Nil(t, wf)
	assert.Error(t, err)
}

func TestWorkflowRepo_List(t *testing.T) {
	repo := repoWithTestdata(t, testScope())

	summaries, err := repo.List(context.Background(), testScope())
	require.NoError(t, err)

	require.Len(t, summaries, 2)

	names := make([]string, len(summaries))
	for i, s := range summaries {
		names[i] = s.Name
	}
	assert.Contains(t, names, "valid-workflow")
	assert.Contains(t, names, "all-node-types")
}

func TestWorkflowRepo_Load_AllNodeTypes(t *testing.T) {
	repo := repoWithTestdata(t, testScope())

	wf, err := repo.Load(context.Background(), testScope(), "all-node-types")
	require.NoError(t, err)
	require.NotNil(t, wf)
	require.Len(t, wf.Nodes, 5)

	byID := make(map[string]workflow.Node, 5)
	for _, n := range wf.Nodes {
		byID[n.ID] = n
	}

	llm := byID["llm-node"]
	assert.Equal(t, workflow.NodeTypeLLMCall, llm.Type)
	assert.Equal(t, "prompt.md", llm.Config.TemplateFile)
	assert.Equal(t, "default-chat", llm.Config.ModelProfile)
	assert.Equal(t, 1000, llm.Config.MaxTokens)
	assert.Equal(t, 0.7, llm.Config.Temperature)
	assert.NotNil(t, llm.Config.OutputSchema)

	script := byID["script-node"]
	assert.Equal(t, workflow.NodeTypeScript, script.Type)
	assert.Equal(t, "run.sh", script.Config.ScriptFile)
	assert.Equal(t, "bash", script.Config.Interpreter)
	assert.Equal(t, []string{"--input", "{{llm-node.result}}"}, script.Config.Args)
	assert.Equal(t, 30, script.Config.TimeoutSeconds)

	tool := byID["tool-node"]
	assert.Equal(t, workflow.NodeTypeToolCall, tool.Type)
	assert.Equal(t, "git", tool.Config.Command)
	assert.Equal(t, []string{"status"}, tool.Config.Args)
	assert.Equal(t, 10, tool.Config.TimeoutSeconds)

	cond := byID["cond-node"]
	assert.Equal(t, workflow.NodeTypeConditional, cond.Type)
	require.Len(t, cond.Config.Conditions, 1)
	assert.Equal(t, "score", cond.Config.Conditions[0].Field)
	assert.Equal(t, "gt", cond.Config.Conditions[0].Operator)
	assert.Equal(t, "high", cond.Config.Conditions[0].Output)
	assert.Equal(t, "low", cond.Config.DefaultOutput)

	transform := byID["transform-node"]
	assert.Equal(t, workflow.NodeTypeTransform, transform.Type)
	require.Len(t, transform.Config.Operations, 1)
	assert.Equal(t, "result", transform.Config.Operations[0].Variable)
	assert.Equal(t, "set", transform.Config.Operations[0].Operation)
}

func TestWorkflowRepo_List_EmptyWorkspace(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)

	summaries, err := repo.List(context.Background(), testScope())
	require.NoError(t, err)
	assert.Empty(t, summaries)
}

func TestWorkflowRepo_Load_PathTraversalRejected(t *testing.T) {
	repo := repoWithTestdata(t, testScope())

	_, err := repo.Load(context.Background(), testScope(), "../sibling")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// ---------------------------------------------------------------------------
// Scope-specific tests
// ---------------------------------------------------------------------------

func TestWorkflowRepo_Scope_PrivateAndSharedRoots(t *testing.T) {
	base := t.TempDir()
	repo := filesystem.NewWorkflowRepo(base)
	ctx := context.Background()

	owner := uuid.New()
	require.NoError(t, repo.Create(ctx, workflow.Private(owner), "p", sampleWorkflow("p")))
	require.NoError(t, repo.Create(ctx, workflow.Shared(), "s", sampleWorkflow("s")))

	// Private lands under users/<owner>/p
	_, err := os.Stat(filepath.Join(base, "users", owner.String(), "p", "workflow.json"))
	require.NoError(t, err)
	// Shared lands under shared/s
	_, err = os.Stat(filepath.Join(base, "shared", "s", "workflow.json"))
	require.NoError(t, err)
}

func TestWorkflowRepo_Scope_PrivateRequiresOwner(t *testing.T) {
	base := t.TempDir()
	repo := filesystem.NewWorkflowRepo(base)
	ctx := context.Background()

	err := repo.Create(ctx, workflow.Private(uuid.Nil), "x", sampleWorkflow("x"))
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrInvalidName)
}

func TestWorkflowRepo_Scope_UnknownKindRejected(t *testing.T) {
	base := t.TempDir()
	repo := filesystem.NewWorkflowRepo(base)
	ctx := context.Background()

	bad := workflow.Scope{Kind: workflow.ScopeKind(99), OwnerID: uuid.New()}
	err := repo.Create(ctx, bad, "x", sampleWorkflow("x"))
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrInvalidName)
}

func TestWorkflowRepo_List_IsolatedPerOwner(t *testing.T) {
	base := t.TempDir()
	repo := filesystem.NewWorkflowRepo(base)
	ctx := context.Background()

	a := uuid.New()
	b := uuid.New()
	require.NoError(t, repo.Create(ctx, workflow.Private(a), "a-only", sampleWorkflow("a-only")))
	require.NoError(t, repo.Create(ctx, workflow.Private(b), "b-only", sampleWorkflow("b-only")))

	listA, err := repo.List(ctx, workflow.Private(a))
	require.NoError(t, err)
	require.Len(t, listA, 1)
	assert.Equal(t, "a-only", listA[0].Name)
}

func TestWorkflowRepo_List_NeverCreatedScopeRootIsEmpty(t *testing.T) {
	base := t.TempDir()
	repo := filesystem.NewWorkflowRepo(base)
	ctx := context.Background()

	// No private workflows ever created for this owner: the users/<id> dir
	// does not exist. List must return an empty slice, not an error.
	out, err := repo.List(ctx, workflow.Private(uuid.New()))
	require.NoError(t, err)
	assert.NotNil(t, out)
	assert.Empty(t, out)
}

func TestWorkflowRepo_Exists(t *testing.T) {
	base := t.TempDir()
	repo := filesystem.NewWorkflowRepo(base)
	ctx := context.Background()

	exists, err := repo.Exists(ctx, testScope(), "ghost")
	require.NoError(t, err)
	assert.False(t, exists)

	require.NoError(t, repo.Create(ctx, testScope(), "ghost", sampleWorkflow("ghost")))

	exists, err = repo.Exists(ctx, testScope(), "ghost")
	require.NoError(t, err)
	assert.True(t, exists)
}

func TestWorkflowRepo_Create_UserDirIs0700(t *testing.T) {
	base := t.TempDir()
	repo := filesystem.NewWorkflowRepo(base)
	ctx := context.Background()

	owner := uuid.New()
	require.NoError(t, repo.Create(ctx, workflow.Private(owner), "w", sampleWorkflow("w")))

	userDir := filepath.Join(base, "users", owner.String())
	usersParent := filepath.Join(base, "users")
	wfDir := filepath.Join(userDir, "w")

	uinfo, err := os.Stat(userDir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), uinfo.Mode().Perm(), "per-user dir must be 0o700")

	pinfo, err := os.Stat(usersParent)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), pinfo.Mode().Perm(), "users/ parent must be 0o700")

	winfo, err := os.Stat(wfDir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), winfo.Mode().Perm(), "workflow dir itself is 0o755")
}

func TestWorkflowRepo_Scope_CrossScopeEscapeRejected(t *testing.T) {
	base := t.TempDir()
	repo := filesystem.NewWorkflowRepo(base)
	ctx := context.Background()

	a := uuid.New()
	other := uuid.New()

	// Cannot reach another user's partition from a Private scope.
	err := repo.Create(ctx, workflow.Private(a), "../"+other.String()+"/secret", sampleWorkflow("x"))
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrInvalidName)

	// Cannot reach shared/ from a Private scope.
	err = repo.Create(ctx, workflow.Private(a), "../../shared/x", sampleWorkflow("x"))
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrInvalidName)

	// Cannot reach a user partition from the Shared scope.
	err = repo.Create(ctx, workflow.Shared(), "../users/"+a.String()+"/x", sampleWorkflow("x"))
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrInvalidName)
}

// ---------------------------------------------------------------------------
// CopyTree
// ---------------------------------------------------------------------------

func TestWorkflowRepo_CopyTree_PrivateToShared(t *testing.T) {
	base := t.TempDir()
	repo := filesystem.NewWorkflowRepo(base)
	ctx := context.Background()
	owner := uuid.New()

	require.NoError(t, repo.Create(ctx, workflow.Private(owner), "src", sampleWorkflow("src")))
	require.NoError(t, repo.WriteFile(ctx, workflow.Private(owner), "src", "extra.txt", []byte("hello")))

	require.NoError(t, repo.CopyTree(ctx, workflow.Private(owner), "src", workflow.Shared(), "pub", false))

	mf, err := os.ReadFile(filepath.Join(base, "shared", "pub", "workflow.json"))
	require.NoError(t, err)
	assert.Contains(t, string(mf), "\"name\"")
	extra, err := os.ReadFile(filepath.Join(base, "shared", "pub", "extra.txt"))
	require.NoError(t, err)
	assert.Equal(t, "hello", string(extra))
}

func TestWorkflowRepo_CopyTree_SourceNotFound(t *testing.T) {
	base := t.TempDir()
	repo := filesystem.NewWorkflowRepo(base)
	ctx := context.Background()
	owner := uuid.New()

	err := repo.CopyTree(ctx, workflow.Private(owner), "ghost", workflow.Shared(), "pub", false)
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrNotFound)
}

func TestWorkflowRepo_CopyTree_DestExistsNoOverwrite(t *testing.T) {
	base := t.TempDir()
	repo := filesystem.NewWorkflowRepo(base)
	ctx := context.Background()
	owner := uuid.New()

	require.NoError(t, repo.Create(ctx, workflow.Private(owner), "src", sampleWorkflow("src")))
	require.NoError(t, repo.Create(ctx, workflow.Shared(), "pub", sampleWorkflow("pub")))

	err := repo.CopyTree(ctx, workflow.Private(owner), "src", workflow.Shared(), "pub", false)
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrAlreadyExists)
}

func TestWorkflowRepo_CopyTree_OverwriteReplaces(t *testing.T) {
	base := t.TempDir()
	repo := filesystem.NewWorkflowRepo(base)
	ctx := context.Background()
	owner := uuid.New()

	require.NoError(t, repo.Create(ctx, workflow.Private(owner), "src", sampleWorkflow("src-v2")))
	require.NoError(t, repo.WriteFile(ctx, workflow.Private(owner), "src", "f.txt", []byte("v2")))

	require.NoError(t, repo.Create(ctx, workflow.Shared(), "pub", sampleWorkflow("pub-old")))
	require.NoError(t, repo.WriteFile(ctx, workflow.Shared(), "pub", "stale.txt", []byte("old")))

	require.NoError(t, repo.CopyTree(ctx, workflow.Private(owner), "src", workflow.Shared(), "pub", true))

	f, err := os.ReadFile(filepath.Join(base, "shared", "pub", "f.txt"))
	require.NoError(t, err)
	assert.Equal(t, "v2", string(f))
	_, err = os.Stat(filepath.Join(base, "shared", "pub", "stale.txt"))
	assert.True(t, os.IsNotExist(err), "overwrite must not retain old folder's files")

	entries, err := os.ReadDir(filepath.Join(base, "shared"))
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), ".tmp-")
		assert.NotContains(t, e.Name(), ".old-")
	}
}

func TestWorkflowRepo_CopyTree_IndependentCopy(t *testing.T) {
	base := t.TempDir()
	repo := filesystem.NewWorkflowRepo(base)
	ctx := context.Background()
	owner := uuid.New()

	require.NoError(t, repo.Create(ctx, workflow.Shared(), "orig", sampleWorkflow("orig")))
	require.NoError(t, repo.WriteFile(ctx, workflow.Shared(), "orig", "f.txt", []byte("shared-content")))

	require.NoError(t, repo.CopyTree(ctx, workflow.Shared(), "orig", workflow.Private(owner), "mine", false))

	require.NoError(t, repo.WriteFile(ctx, workflow.Private(owner), "mine", "f.txt", []byte("edited")))

	shared, err := os.ReadFile(filepath.Join(base, "shared", "orig", "f.txt"))
	require.NoError(t, err)
	assert.Equal(t, "shared-content", string(shared), "editing the copy must not touch the shared original")
}

func TestWorkflowRepo_CopyTree_CrossPartitionEscapeRejected(t *testing.T) {
	base := t.TempDir()
	repo := filesystem.NewWorkflowRepo(base)
	ctx := context.Background()
	a := uuid.New()
	other := uuid.New()

	require.NoError(t, repo.Create(ctx, workflow.Private(a), "src", sampleWorkflow("src")))

	err := repo.CopyTree(ctx, workflow.Private(a), "src", workflow.Private(a), "../"+other.String()+"/stolen", false)
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrInvalidName)

	err = repo.CopyTree(ctx, workflow.Private(a), "../"+other.String()+"/secret", workflow.Shared(), "pub", false)
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrInvalidName)
}

// ---------------------------------------------------------------------------
// Helper: sample workflow
// ---------------------------------------------------------------------------

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
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	wf := sampleWorkflow("my-workflow")
	err := repo.Create(ctx, testScope(), "my-workflow", wf)
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(scopeRootFor(t, dir, testScope()), "my-workflow", "workflow.json"))
	require.NoError(t, err)

	var loaded workflow.Workflow
	require.NoError(t, json.Unmarshal(data, &loaded))
	assert.Equal(t, "my-workflow", loaded.Name)
	assert.Equal(t, "test workflow", loaded.Description)
	assert.Equal(t, "tester", loaded.Author)
	assert.Len(t, loaded.Nodes, 1)
}

func TestWorkflowRepo_Create_AlreadyExists(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	wf := sampleWorkflow("existing")
	require.NoError(t, repo.Create(ctx, testScope(), "existing", wf))

	err := repo.Create(ctx, testScope(), "existing", wf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestWorkflowRepo_Create_PathTraversalRejected(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	wf := sampleWorkflow("evil")
	err := repo.Create(ctx, testScope(), "../escape", wf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid workflow name")
}

func TestWorkflowRepo_Create_LoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	wf := sampleWorkflow("roundtrip")
	require.NoError(t, repo.Create(ctx, testScope(), "roundtrip", wf))

	loaded, err := repo.Load(ctx, testScope(), "roundtrip")
	require.NoError(t, err)
	assert.Equal(t, wf.Name, loaded.Name)
	assert.Equal(t, wf.Description, loaded.Description)
	assert.Equal(t, wf.Author, loaded.Author)
	assert.Len(t, loaded.Nodes, len(wf.Nodes))
}

func TestWorkflowRepo_Create_SharedRoundTrip(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	wf := sampleWorkflow("shared-rt")
	require.NoError(t, repo.Create(ctx, workflow.Shared(), "shared-rt", wf))

	loaded, err := repo.Load(ctx, workflow.Shared(), "shared-rt")
	require.NoError(t, err)
	assert.Equal(t, "shared-rt", loaded.Name)
}

func TestWorkflowRepo_Create_SpecialCharsInName(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	wf := sampleWorkflow("my_workflow-2")
	err := repo.Create(ctx, testScope(), "my_workflow-2", wf)
	require.NoError(t, err)

	loaded, err := repo.Load(ctx, testScope(), "my_workflow-2")
	require.NoError(t, err)
	assert.Equal(t, "my_workflow-2", loaded.Name)
}

func TestWorkflowRepo_Create_AtomicManifestWrite(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	wf := sampleWorkflow("atomic-create")
	require.NoError(t, repo.Create(ctx, testScope(), "atomic-create", wf))

	entries, err := os.ReadDir(filepath.Join(scopeRootFor(t, dir, testScope()), "atomic-create"))
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, filepath.Ext(e.Name()) == ".tmp", "temp file should not remain: %s", e.Name())
	}

	require.Len(t, entries, 1)
	assert.Equal(t, "workflow.json", entries[0].Name())

	loaded, err := repo.Load(ctx, testScope(), "atomic-create")
	require.NoError(t, err)
	assert.Equal(t, "atomic-create", loaded.Name)
}

// ---------------------------------------------------------------------------
// Tests: Update
// ---------------------------------------------------------------------------

func TestWorkflowRepo_Update_HappyPath(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	wf := sampleWorkflow("updatable")
	require.NoError(t, repo.Create(ctx, testScope(), "updatable", wf))

	wf.Description = "updated description"
	wf.Author = "new-author"
	err := repo.Update(ctx, testScope(), "updatable", wf)
	require.NoError(t, err)

	loaded, err := repo.Load(ctx, testScope(), "updatable")
	require.NoError(t, err)
	assert.Equal(t, "updated description", loaded.Description)
	assert.Equal(t, "new-author", loaded.Author)
}

func TestWorkflowRepo_Update_NotFound(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	wf := sampleWorkflow("ghost")
	err := repo.Update(ctx, testScope(), "ghost", wf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestWorkflowRepo_Update_PathTraversalRejected(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	wf := sampleWorkflow("evil")
	err := repo.Update(ctx, testScope(), "../escape", wf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid workflow name")
}

func TestWorkflowRepo_Update_PreservesOtherFiles(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, testScope(), "with-files", sampleWorkflow("with-files")))
	require.NoError(t, repo.WriteFile(ctx, testScope(), "with-files", "script.sh", []byte("#!/bin/bash")))
	require.NoError(t, repo.WriteFile(ctx, testScope(), "with-files", "templates/prompt.md", []byte("# prompt")))

	updated := sampleWorkflow("with-files")
	updated.Description = "updated"
	require.NoError(t, repo.Update(ctx, testScope(), "with-files", updated))

	loaded, err := repo.Load(ctx, testScope(), "with-files")
	require.NoError(t, err)
	assert.Equal(t, "updated", loaded.Description)

	files, err := repo.ListFiles(ctx, testScope(), "with-files")
	require.NoError(t, err)
	assert.Contains(t, files, "script.sh")
	assert.Contains(t, files, "templates/prompt.md")
}

func TestWorkflowRepo_Update_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	wf := sampleWorkflow("atomic")
	require.NoError(t, repo.Create(ctx, testScope(), "atomic", wf))

	wf.Description = "atomically updated"
	require.NoError(t, repo.Update(ctx, testScope(), "atomic", wf))

	entries, err := os.ReadDir(filepath.Join(scopeRootFor(t, dir, testScope()), "atomic"))
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, filepath.Ext(e.Name()) == ".tmp", "temp file should not remain: %s", e.Name())
	}

	loaded, err := repo.Load(ctx, testScope(), "atomic")
	require.NoError(t, err)
	assert.Equal(t, "atomically updated", loaded.Description)
}

// ---------------------------------------------------------------------------
// Tests: Delete
// ---------------------------------------------------------------------------

func TestWorkflowRepo_Delete_HappyPath(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	wf := sampleWorkflow("doomed")
	require.NoError(t, repo.Create(ctx, testScope(), "doomed", wf))

	_, err := repo.Load(ctx, testScope(), "doomed")
	require.NoError(t, err)

	err = repo.Delete(ctx, testScope(), "doomed")
	require.NoError(t, err)

	_, err = repo.Load(ctx, testScope(), "doomed")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	_, err = os.Stat(filepath.Join(scopeRootFor(t, dir, testScope()), "doomed"))
	assert.True(t, os.IsNotExist(err))
}

func TestWorkflowRepo_Delete_NotFound(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	err := repo.Delete(ctx, testScope(), "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestWorkflowRepo_Delete_PathTraversalRejected(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	err := repo.Delete(ctx, testScope(), "../escape")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid workflow name")
}

func TestWorkflowRepo_Delete_EmptyNameRejected(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	err := repo.Delete(ctx, testScope(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid workflow name")

	_, statErr := os.Stat(dir)
	require.NoError(t, statErr, "basePath must not be removed by Delete with empty name")
}

func TestWorkflowRepo_Delete_DotNameRejected(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	err := repo.Delete(ctx, testScope(), ".")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid workflow name")

	_, statErr := os.Stat(dir)
	require.NoError(t, statErr, "basePath must not be removed by Delete with dot name")
}

func TestWorkflowRepo_Delete_WithExtraFiles(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	wf := sampleWorkflow("with-files")
	require.NoError(t, repo.Create(ctx, testScope(), "with-files", wf))

	wfDir := filepath.Join(scopeRootFor(t, dir, testScope()), "with-files")
	require.NoError(t, os.WriteFile(filepath.Join(wfDir, "script.sh"), []byte("#!/bin/bash"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(wfDir, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(wfDir, "sub", "data.txt"), []byte("data"), 0o644))

	err := repo.Delete(ctx, testScope(), "with-files")
	require.NoError(t, err)

	_, err = os.Stat(wfDir)
	assert.True(t, os.IsNotExist(err))
}

// ---------------------------------------------------------------------------
// Tests: WriteFile
// ---------------------------------------------------------------------------

func TestWorkflowRepo_WriteFile_HappyPath(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, testScope(), "wf1", sampleWorkflow("wf1")))

	err := repo.WriteFile(ctx, testScope(), "wf1", "script.sh", []byte("#!/bin/bash\necho hi"))
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(scopeRootFor(t, dir, testScope()), "wf1", "script.sh"))
	require.NoError(t, err)
	assert.Equal(t, "#!/bin/bash\necho hi", string(data))
}

func TestWorkflowRepo_WriteFile_NestedDirs(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, testScope(), "wf2", sampleWorkflow("wf2")))

	err := repo.WriteFile(ctx, testScope(), "wf2", "templates/prompt.md", []byte("# Hello"))
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(scopeRootFor(t, dir, testScope()), "wf2", "templates", "prompt.md"))
	require.NoError(t, err)
	assert.Equal(t, "# Hello", string(data))
}

func TestWorkflowRepo_WriteFile_EmptyContent(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, testScope(), "wf3", sampleWorkflow("wf3")))

	err := repo.WriteFile(ctx, testScope(), "wf3", "empty.txt", []byte{})
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(scopeRootFor(t, dir, testScope()), "wf3", "empty.txt"))
	require.NoError(t, err)
	assert.Empty(t, data)
}

func TestWorkflowRepo_WriteFile_WorkflowNotFound(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	err := repo.WriteFile(ctx, testScope(), "nonexistent", "file.txt", []byte("data"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestWorkflowRepo_WriteFile_PathTraversalWorkflowName(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	err := repo.WriteFile(ctx, testScope(), "../escape", "file.txt", []byte("data"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid workflow name")
}

func TestWorkflowRepo_WriteFile_PathTraversalRelativePath(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, testScope(), "wf4", sampleWorkflow("wf4")))

	err := repo.WriteFile(ctx, testScope(), "wf4", "../escape.txt", []byte("evil"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid file path")

	err = repo.WriteFile(ctx, testScope(), "wf4", "sub/../../escape.txt", []byte("evil"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid file path")
}

func TestWorkflowRepo_WriteFile_OverwriteExisting(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, testScope(), "wf5", sampleWorkflow("wf5")))

	require.NoError(t, repo.WriteFile(ctx, testScope(), "wf5", "data.txt", []byte("v1")))
	require.NoError(t, repo.WriteFile(ctx, testScope(), "wf5", "data.txt", []byte("v2")))

	data, err := os.ReadFile(filepath.Join(scopeRootFor(t, dir, testScope()), "wf5", "data.txt"))
	require.NoError(t, err)
	assert.Equal(t, "v2", string(data))
}

func TestWorkflowRepo_WriteFile_ManifestProtected(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, testScope(), "wf-guard", sampleWorkflow("wf-guard")))

	err := repo.WriteFile(ctx, testScope(), "wf-guard", "workflow.json", []byte(`{"name":"evil"}`))
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrInvalidFilePath, "WriteFile must not overwrite the manifest")

	// Verify the manifest is unchanged.
	loaded, loadErr := repo.Load(ctx, testScope(), "wf-guard")
	require.NoError(t, loadErr)
	assert.Equal(t, "wf-guard", loaded.Name, "manifest must be intact after rejected write")
}

// ---------------------------------------------------------------------------
// Tests: DeleteFile
// ---------------------------------------------------------------------------

func TestWorkflowRepo_DeleteFile_HappyPath(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, testScope(), "wf-del", sampleWorkflow("wf-del")))
	require.NoError(t, repo.WriteFile(ctx, testScope(), "wf-del", "script.sh", []byte("#!/bin/bash")))

	err := repo.DeleteFile(ctx, testScope(), "wf-del", "script.sh")
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(scopeRootFor(t, dir, testScope()), "wf-del", "script.sh"))
	assert.True(t, os.IsNotExist(err))
}

func TestWorkflowRepo_DeleteFile_FileNotFound(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, testScope(), "wf-del2", sampleWorkflow("wf-del2")))

	err := repo.DeleteFile(ctx, testScope(), "wf-del2", "nonexistent.txt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestWorkflowRepo_DeleteFile_WorkflowNotFound(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	err := repo.DeleteFile(ctx, testScope(), "ghost-wf", "file.txt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestWorkflowRepo_DeleteFile_PathTraversalWorkflowName(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	err := repo.DeleteFile(ctx, testScope(), "../escape", "file.txt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid workflow name")
}

func TestWorkflowRepo_DeleteFile_PathTraversalRelativePath(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, testScope(), "wf-del3", sampleWorkflow("wf-del3")))

	err := repo.DeleteFile(ctx, testScope(), "wf-del3", "../workflow.json")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid file path")
}

func TestWorkflowRepo_DeleteFile_LeavesManifestIntact(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, testScope(), "wf-del4", sampleWorkflow("wf-del4")))
	require.NoError(t, repo.WriteFile(ctx, testScope(), "wf-del4", "only-file.txt", []byte("content")))

	require.NoError(t, repo.DeleteFile(ctx, testScope(), "wf-del4", "only-file.txt"))

	_, err := repo.Load(ctx, testScope(), "wf-del4")
	require.NoError(t, err)
}

func TestWorkflowRepo_DeleteFile_ManifestProtected(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, testScope(), "wf-del5", sampleWorkflow("wf-del5")))

	err := repo.DeleteFile(ctx, testScope(), "wf-del5", "workflow.json")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrInvalidFilePath, "DeleteFile must not delete the manifest")

	// Verify the workflow is still loadable.
	loaded, loadErr := repo.Load(ctx, testScope(), "wf-del5")
	require.NoError(t, loadErr)
	assert.Equal(t, "wf-del5", loaded.Name, "manifest must be intact after rejected delete")
}

// ---------------------------------------------------------------------------
// Tests: ListFiles
// ---------------------------------------------------------------------------

func TestWorkflowRepo_ListFiles_HappyPath(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, testScope(), "wf-list", sampleWorkflow("wf-list")))
	require.NoError(t, repo.WriteFile(ctx, testScope(), "wf-list", "script.sh", []byte("#!/bin/bash")))
	require.NoError(t, repo.WriteFile(ctx, testScope(), "wf-list", "templates/prompt.md", []byte("# Hi")))

	files, err := repo.ListFiles(ctx, testScope(), "wf-list")
	require.NoError(t, err)

	assert.Contains(t, files, "script.sh")
	assert.Contains(t, files, "templates/prompt.md")
	assert.NotContains(t, files, "workflow.json")
	assert.Len(t, files, 2)
}

func TestWorkflowRepo_ListFiles_ExcludesWorkflowJSON(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, testScope(), "wf-list2", sampleWorkflow("wf-list2")))

	files, err := repo.ListFiles(ctx, testScope(), "wf-list2")
	require.NoError(t, err)
	assert.Empty(t, files)
}

func TestWorkflowRepo_ListFiles_WorkflowNotFound(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	_, err := repo.ListFiles(ctx, testScope(), "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestWorkflowRepo_ListFiles_PathTraversalRejected(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	_, err := repo.ListFiles(ctx, testScope(), "../escape")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid workflow name")
}

func TestWorkflowRepo_ListFiles_ForwardSlashes(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, testScope(), "wf-list3", sampleWorkflow("wf-list3")))
	require.NoError(t, repo.WriteFile(ctx, testScope(), "wf-list3", "a/b/c/deep.txt", []byte("deep")))

	files, err := repo.ListFiles(ctx, testScope(), "wf-list3")
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "a/b/c/deep.txt", files[0])
}

func TestWorkflowRepo_WriteFile_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, testScope(), "wf-atomic", sampleWorkflow("wf-atomic")))

	require.NoError(t, repo.WriteFile(ctx, testScope(), "wf-atomic", "data.txt", []byte("v1")))
	require.NoError(t, repo.WriteFile(ctx, testScope(), "wf-atomic", "data.txt", []byte("v2")))

	entries, err := os.ReadDir(filepath.Join(scopeRootFor(t, dir, testScope()), "wf-atomic"))
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, filepath.Ext(e.Name()) == ".tmp", "temp file should not remain: %s", e.Name())
	}

	data, err := os.ReadFile(filepath.Join(scopeRootFor(t, dir, testScope()), "wf-atomic", "data.txt"))
	require.NoError(t, err)
	assert.Equal(t, "v2", string(data))
}

func TestWorkflowRepo_ListFiles_NestedDirs(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, testScope(), "wf-list4", sampleWorkflow("wf-list4")))
	require.NoError(t, repo.WriteFile(ctx, testScope(), "wf-list4", "scripts/run.sh", []byte("run")))
	require.NoError(t, repo.WriteFile(ctx, testScope(), "wf-list4", "scripts/setup.sh", []byte("setup")))
	require.NoError(t, repo.WriteFile(ctx, testScope(), "wf-list4", "templates/main.md", []byte("main")))

	files, err := repo.ListFiles(ctx, testScope(), "wf-list4")
	require.NoError(t, err)
	assert.Len(t, files, 3)
	assert.Contains(t, files, "scripts/run.sh")
	assert.Contains(t, files, "scripts/setup.sh")
	assert.Contains(t, files, "templates/main.md")
}

// ---------------------------------------------------------------------------
// Tests: SaveNodePositions
// ---------------------------------------------------------------------------

// Happy path: positions for matching node IDs are persisted atomically.
func TestWorkflowRepo_SaveNodePositions_HappyPath(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	wf := &workflow.Workflow{
		Name:   "pos-test",
		Author: "tester",
		Nodes: []workflow.Node{
			{ID: "A", Type: workflow.NodeTypeScript, Entrypoint: true, Config: workflow.NodeConfig{ScriptFile: "run.sh"}},
			{ID: "B", Type: workflow.NodeTypeLLMCall},
		},
	}
	require.NoError(t, repo.Create(ctx, testScope(), "pos-test", wf))

	positions := map[string]workflow.NodePosition{
		"A": {X: 100, Y: 200},
		"B": {X: 300, Y: 400},
	}
	require.NoError(t, repo.SaveNodePositions(ctx, testScope(), "pos-test", positions))

	loaded, err := repo.Load(ctx, testScope(), "pos-test")
	require.NoError(t, err)
	byID := make(map[string]workflow.Node, len(loaded.Nodes))
	for _, n := range loaded.Nodes {
		byID[n.ID] = n
	}
	require.NotNil(t, byID["A"].Position, "position for node A must be set")
	assert.Equal(t, 100.0, byID["A"].Position.X)
	assert.Equal(t, 200.0, byID["A"].Position.Y)
	require.NotNil(t, byID["B"].Position, "position for node B must be set")
	assert.Equal(t, 300.0, byID["B"].Position.X)
	assert.Equal(t, 400.0, byID["B"].Position.Y)

	// No .tmp artifacts must remain after an atomic write.
	entries, err := os.ReadDir(filepath.Join(scopeRootFor(t, dir, testScope()), "pos-test"))
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, strings.HasSuffix(e.Name(), ".tmp"), "temp file must not remain: %s", e.Name())
	}
}

// Failure: saving positions for a non-existent workflow returns ErrNotFound.
func TestWorkflowRepo_SaveNodePositions_NotFound(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	err := repo.SaveNodePositions(ctx, testScope(), "ghost", map[string]workflow.NodePosition{
		"A": {X: 1, Y: 2},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrNotFound)
}

// Error: path traversal in the workflow name is rejected (mapped to ErrNotFound
// by SaveNodePositions, consistent with Load's behaviour for invalid names).
func TestWorkflowRepo_SaveNodePositions_PathTraversalRejected(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	err := repo.SaveNodePositions(ctx, testScope(), "../escape", map[string]workflow.NodePosition{})
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrNotFound)
}

// Edge: an empty positions map is a no-op; the manifest must be left intact.
func TestWorkflowRepo_SaveNodePositions_EmptyPositionsMap(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, testScope(), "empty-pos", sampleWorkflow("empty-pos")))

	require.NoError(t, repo.SaveNodePositions(ctx, testScope(), "empty-pos", map[string]workflow.NodePosition{}))

	loaded, err := repo.Load(ctx, testScope(), "empty-pos")
	require.NoError(t, err)
	assert.Equal(t, "empty-pos", loaded.Name, "manifest must be intact after empty-positions save")
}

// Edge: positions whose IDs don't match any node in the workflow are silently
// ignored; the manifest remains valid and unchanged.
func TestWorkflowRepo_SaveNodePositions_UnknownNodeIDsIgnored(t *testing.T) {
	dir := t.TempDir()
	repo := filesystem.NewWorkflowRepo(dir)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, testScope(), "no-match", sampleWorkflow("no-match")))

	err := repo.SaveNodePositions(ctx, testScope(), "no-match", map[string]workflow.NodePosition{
		"nonexistent-id": {X: 9, Y: 9},
	})
	require.NoError(t, err, "unknown node IDs must not cause an error")

	loaded, err := repo.Load(ctx, testScope(), "no-match")
	require.NoError(t, err)
	assert.Equal(t, "no-match", loaded.Name, "manifest must be intact when no node IDs match")
	// The single node from sampleWorkflow (ID "A") must have no position set.
	require.Len(t, loaded.Nodes, 1)
	assert.Nil(t, loaded.Nodes[0].Position, "unmatched node must retain nil position")
}
