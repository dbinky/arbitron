package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An invalid legacy group must stay untouched without blocking its valid
// neighbors or the next scope. Child contracts must not depend on backfill order.
func TestGroupAuthoringE2E_BackfillSkipsInvalidGroupsAndContinuesScopes(t *testing.T) {
	paths := writeGroupStack(t, 5, nil)
	repo := filesystem.NewWorkflowRepo(paths.workspaceDir)
	ctx := context.Background()
	invalid := map[string][]byte{}
	var valid []string
	for _, scope := range []workflow.Scope{workflow.Private(localDefaultUser), workflow.Shared()} {
		label := "private"
		if scope.Kind == workflow.ScopeShared {
			label = "shared"
		}
		for _, form := range []string{workflow.GroupFormBranches, workflow.GroupFormWorkflows} {
			name := "a-" + label + "-invalid-" + form
			w := filesModeGroup(name, "default-agent")
			w.Nodes[0].Config.Group.Form = form
			w.Nodes[0].Config.Group.Tasks = nil
			require.NoError(t, repo.Create(ctx, scope, name, w))
			require.NoError(t, repo.WriteFile(ctx, scope, name, "keep.txt", []byte("preserve legacy files")))
			dir := privateDir(paths.workspaceDir, name)
			if scope.Kind == workflow.ScopeShared {
				dir = filepath.Join(paths.workspaceDir, "shared", name)
			}
			for _, file := range []string{"workflow.json", "keep.txt"} {
				path := filepath.Join(dir, file)
				b, err := os.ReadFile(path)
				require.NoError(t, err)
				invalid[path] = b
			}
		}
		branch := filesModeGroup("m-"+label+"-branches", "default-agent")
		require.NoError(t, repo.Create(ctx, scope, branch.Name, branch))
		var parent workflow.Workflow
		require.NoError(t, json.Unmarshal([]byte(groupWorkflowsManifest), &parent))
		parent.Name = "m-" + label + "-workflows"
		for i := range parent.Nodes[1].Config.Group.Tasks {
			parent.Nodes[1].Config.Group.Tasks[i].Workflow = "zz-child"
		}
		require.NoError(t, repo.Create(ctx, scope, parent.Name, &parent))
		valid = append(valid, branch.Name, parent.Name)
	}
	// The shared child sorts after both parents and starts without a registry row.
	var child workflow.Workflow
	require.NoError(t, json.Unmarshal([]byte(groupEchoChildManifest), &child))
	child.Name = "zz-child"
	require.NoError(t, repo.Create(ctx, workflow.Shared(), child.Name, &child))
	valid = append(valid, child.Name)

	ds, err := initDomain(ctx, paths.configPath)
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close(context.Background()) })
	for _, name := range valid {
		assert.Equal(t, 1, registryRows(t, ds, name), "valid neighbor must resolve after boot: %s", name)
	}
	// Reconciliation still reports every rejected name to the startup logger.
	err = ds.workflowSvc.BackfillRegistry(adminCtxForLocalUser(t, ds))
	assert.ErrorIs(t, err, workflow.ErrGroupInvalid)
	var gve *workflow.GroupValidationError
	if assert.True(t, errors.As(err, &gve)) {
		assert.Contains(t, groupCodes(gve.Errors), "GROUP_NO_TASKS")
	}
	for path, before := range invalid {
		after, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		assert.Equal(t, before, after, "invalid imports must remain byte-identical")
		name := filepath.Base(filepath.Dir(path))
		assert.Zero(t, registryRows(t, ds, name))
		assert.Contains(t, err.Error(), name, "every rejection must be observable")
	}
}

// Seeding and legacy-id backfill must run after the live profile checker is
// constructed. The prompt is incoming sample data, absent from the destination.
func TestGroupAuthoringE2E_StartupImportsValidateBeforePersistence(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "workspaces")
	source := filepath.Join(root, "samples")
	for _, tc := range []struct{ name, profile string }{{"valid-group", "default-agent"}, {"invalid-group", "default-chat"}} {
		w := filesModeGroup(tc.name, tc.profile)
		w.Nodes[0].Config.Group.ResolverPromptFile = "resolve.md"
		b, err := json.Marshal(w)
		require.NoError(t, err)
		dir := filepath.Join(source, tc.name)
		require.NoError(t, os.MkdirAll(dir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "workflow.json"), b, 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "resolve.md"), []byte("resolve from sample"), 0o600))
	}
	// A valid id-less legacy group with its own prompt also needs the checker
	// before BackfillRegistry persists the identity.
	legacy := filesModeGroup("legacy-valid", "default-agent")
	legacy.Nodes[0].Config.Group.ResolverPromptFile = "resolve.md"
	b, err := json.Marshal(legacy)
	require.NoError(t, err)
	writeWorkflow(t, ws, "legacy-valid", string(b))
	require.NoError(t, os.WriteFile(filepath.Join(privateDir(ws, "legacy-valid"), "resolve.md"), []byte("legacy prompt"), 0o600))
	configPath := filepath.Join(root, "config.yaml")
	cfg := "server:\n  host: 127.0.0.1\n  port: 0\nauth:\n  mode: local\ndatabase:\n  path: " + filepath.Join(root, "arbitron.db") + "\nworkspace:\n  path: " + ws + "\nsamples:\n  seed: true\n  sourcedir: " + source + "\nlogging:\n  level: error\n  file: " + filepath.Join(root, "arbitron.log") + "\n"
	require.NoError(t, os.WriteFile(configPath, []byte(cfg), 0o600))
	ds, err := initDomain(context.Background(), configPath)
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close(context.Background()) })
	got, err := ds.workflowRepo.Load(context.Background(), workflow.Shared(), "valid-group")
	require.NoError(t, err, "valid incoming prompt-bearing sample must be seeded with the real profile checker")
	assert.Equal(t, "resolve.md", got.Nodes[0].Config.Group.ResolverPromptFile)
	assert.Equal(t, 1, registryRows(t, ds, "valid-group"))
	assert.Equal(t, 0, registryRows(t, ds, "invalid-group"))
	_, err = os.Stat(filepath.Join(ws, "shared", "invalid-group"))
	assert.True(t, os.IsNotExist(err))
	assert.Equal(t, 1, registryRows(t, ds, "legacy-valid"))
	var n int
	require.NoError(t, ds.db.QueryRow("SELECT count(*) FROM published_workflows WHERE name = ?", "invalid-group").Scan(&n))
	assert.Zero(t, n)
	ledger, err := filesystem.NewSeedLedger(ws).Seeded(context.Background())
	require.NoError(t, err)
	assert.True(t, ledger["valid-group"])
	assert.False(t, ledger["invalid-group"])
}

// MCP import tools must expose structured refusal from the production service,
// leaving both real SQLite metadata and the destination folder untouched.
func TestGroupAuthoringE2E_ImportToolsRefuseInvalidDefinitions(t *testing.T) {
	ds, ws := bootGroupAuthoringStack(t)
	ctx := adminCtxForLocalUser(t, ds)
	srv := newGroupMCPServer(ds)
	for _, form := range []string{workflow.GroupFormBranches, workflow.GroupFormWorkflows} {
		for _, op := range []string{"copy", "publish", "duplicate", "rename"} {
			t.Run(op+"/"+form, func(t *testing.T) {
				name := op + "-" + form
				dest := name + "-dest"
				src := workflow.Private(localDefaultUser)
				if op == "copy" {
					src = workflow.Shared()
				}
				w := filesModeGroup(name, "default-agent")
				w.Nodes[0].Config.Group.Form = form
				w.Nodes[0].Config.Group.Tasks = nil
				require.NoError(t, ds.workflowRepo.Create(ctx, src, name, w))
				// Register legacy source for registry-addressed admin operations only.
				if op == "duplicate" || op == "rename" {
					id := uuid.New()
					_, err := ds.db.ExecContext(ctx, "INSERT INTO workflow_identity (id,name,scope,owner_id) VALUES (?,?,?,?)", id.String(), name, "private", localDefaultUser.String())
					require.NoError(t, err)
				}
				path := filepath.Join(privateDir(ws, name), "workflow.json")
				dst := privateDir(ws, dest)
				if op == "copy" {
					path = filepath.Join(ws, "shared", name, "workflow.json")
				}
				if op == "publish" {
					dst = filepath.Join(ws, "shared", dest)
				}
				before, err := os.ReadFile(path)
				require.NoError(t, err)
				args := map[string]any{"name": name, "newName": dest}
				if op == "publish" {
					args = map[string]any{"name": name, "publishedName": dest}
				}
				res := callMCPTool(t, srv, ctx, "workflow_"+op, args)
				require.True(t, res.IsError, mcpText(t, res))
				v := decodeGroupValidation(t, mcpText(t, res))
				assert.False(t, v.Valid)
				assert.Contains(t, groupCodes(v.Errors), "GROUP_NO_TASKS")
				after, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, before, after)
				_, err = os.Stat(dst)
				assert.True(t, os.IsNotExist(err))
				assert.Zero(t, registryRows(t, ds, dest))
				var n int
				require.NoError(t, ds.db.QueryRowContext(ctx, "SELECT count(*) FROM published_workflows WHERE name = ?", dest).Scan(&n))
				assert.Zero(t, n)
			})
		}
	}
}

func TestGroupAuthoringE2E_ImportHTTPRefusesWith422(t *testing.T) {
	ds, _ := bootGroupAuthoringStack(t)
	ctx := adminCtxForLocalUser(t, ds)
	api := gsServeAPI(t, ds)
	for _, op := range []string{"copy", "publish"} {
		name := "http-" + op
		w := filesModeGroup(name, "default-chat")
		src := workflow.Private(localDefaultUser)
		body := map[string]any{"publishedName": name + "-dest"}
		if op == "copy" {
			src = workflow.Shared()
			body = map[string]any{"newName": name + "-dest"}
		}
		require.NoError(t, ds.workflowRepo.Create(ctx, src, name, w))
		status, v := httpJSON(t, http.MethodPost, api.URL+"/api/v1/workflows/"+name+"/"+op, body)
		assert.Equal(t, http.StatusUnprocessableEntity, status)
		assert.Contains(t, groupCodes(v.Errors), "GROUP_RESOLVER_PROFILE_KIND")
		assert.Zero(t, registryRows(t, ds, name+"-dest"))
	}
}
