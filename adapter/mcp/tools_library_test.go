package mcp_test

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/filesystem"
	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/workflow"
)

var libOtherUserID = uuid.MustParse("99999999-9999-9999-9999-999999999999")

// newLibraryServer builds an MCP server whose workflow service has a REAL
// filesystem library store, so the @lib branches run end to end.
func newLibraryServer(t *testing.T) (*adaptmcp.Server, string) {
	t.Helper()
	dir := t.TempDir()
	svc := workflow.NewWorkflowService(&stubWorkflowRepo{}, nil)
	svc.SetLibraryStore(filesystem.NewLibraryStore(dir))
	deps := &adaptmcp.Deps{Workflows: &stubWorkflowRepo{}, FileReader: &stubFileReader{}, WorkflowSvc: svc}
	srv := adaptmcp.NewServerWithDeps("test", "0.0.0", deps)
	adaptmcp.RegisterWorkflowTools(srv)
	return srv, dir
}

func libArgs(kv ...string) map[string]any {
	m := map[string]any{"name": "@lib"}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}

func TestLibTool_WriteDefaultsPrivate(t *testing.T) {
	srv, dir := newLibraryServer(t)
	res := callTool(t, srv, "workflow_write_file", libArgs("path", "x.md", "content", "hello"))
	require.False(t, res.IsError, extractText(t, res))

	var out map[string]string
	require.NoError(t, json.Unmarshal([]byte(extractText(t, res)), &out))
	assert.Equal(t, map[string]string{"status": "written", "name": "@lib", "scope": "private", "path": "x.md"}, out)

	listed := callTool(t, srv, "workflow_list_files", libArgs())
	var l workflow.LibraryListing
	require.NoError(t, json.Unmarshal([]byte(extractText(t, listed)), &l))
	assert.Equal(t, []string{"x.md"}, l.Private)
	assert.Empty(t, l.Shared)
	assert.NoFileExists(t, filepath.Join(dir, "shared", "_lib", "x.md"))
}

func TestLibTool_AdminWritesShared(t *testing.T) {
	srv, _ := newLibraryServer(t)
	res := callToolAdmin(t, srv, "workflow_write_file", libArgs("path", "s.md", "content", "shared", "scope", "shared"))
	require.False(t, res.IsError, extractText(t, res))
	assert.Contains(t, extractText(t, res), `"scope": "shared"`)

	// Any user sees it through the effective read.
	read := callToolWithIdentity(t, srv, "workflow_read_file", libArgs("path", "s.md"),
		identity.Identity{UserID: libOtherUserID, Role: identity.RoleUser})
	require.False(t, read.IsError, extractText(t, read))
	assert.Equal(t, "shared", extractText(t, read))
}

func TestLibTool_ListReturnsBothScopes(t *testing.T) {
	srv, _ := newLibraryServer(t)
	require.False(t, callTool(t, srv, "workflow_write_file", libArgs("path", "p.md", "content", "p")).IsError)
	require.False(t, callToolAdmin(t, srv, "workflow_write_file", libArgs("path", "s.md", "content", "s", "scope", "shared")).IsError)

	res := callTool(t, srv, "workflow_list_files", libArgs())
	require.False(t, res.IsError, extractText(t, res))
	var raw map[string][]string
	require.NoError(t, json.Unmarshal([]byte(extractText(t, res)), &raw))
	assert.Equal(t, map[string][]string{"private": {"p.md"}, "shared": {"s.md"}}, raw)
}

func TestLibTool_ListEmptyScopesAreArrays(t *testing.T) {
	srv, _ := newLibraryServer(t)
	res := callTool(t, srv, "workflow_list_files", libArgs())
	require.False(t, res.IsError, extractText(t, res))
	assert.JSONEq(t, `{"private":[],"shared":[]}`, extractText(t, res))
}

func TestLibTool_ReadEffective(t *testing.T) {
	srv, _ := newLibraryServer(t)
	require.False(t, callToolAdmin(t, srv, "workflow_write_file", libArgs("path", "x.md", "content", "SHARED", "scope", "shared")).IsError)
	res := callTool(t, srv, "workflow_read_file", libArgs("path", "x.md"))
	assert.Equal(t, "SHARED", extractText(t, res))

	require.False(t, callTool(t, srv, "workflow_write_file", libArgs("path", "x.md", "content", "PRIVATE")).IsError)
	res = callTool(t, srv, "workflow_read_file", libArgs("path", "x.md"))
	assert.Equal(t, "PRIVATE", extractText(t, res), "private copy wins the effective read")
}

func TestLibTool_ReadExactSharedScope(t *testing.T) {
	srv, _ := newLibraryServer(t)
	require.False(t, callToolAdmin(t, srv, "workflow_write_file", libArgs("path", "x.md", "content", "SHARED", "scope", "shared")).IsError)
	require.False(t, callTool(t, srv, "workflow_write_file", libArgs("path", "x.md", "content", "PRIVATE")).IsError)

	res := callTool(t, srv, "workflow_read_file", libArgs("path", "x.md", "scope", "shared"))
	assert.Equal(t, "SHARED", extractText(t, res))
	res = callTool(t, srv, "workflow_read_file", libArgs("path", "x.md", "scope", "private"))
	assert.Equal(t, "PRIVATE", extractText(t, res))
}

func TestLibTool_DeleteThenEffectiveFallsBack(t *testing.T) {
	srv, _ := newLibraryServer(t)
	require.False(t, callToolAdmin(t, srv, "workflow_write_file", libArgs("path", "x.md", "content", "SHARED", "scope", "shared")).IsError)
	require.False(t, callTool(t, srv, "workflow_write_file", libArgs("path", "x.md", "content", "PRIVATE")).IsError)

	res := callTool(t, srv, "workflow_delete_file", libArgs("path", "x.md"))
	require.False(t, res.IsError, extractText(t, res))
	assert.JSONEq(t, `{"status":"deleted","name":"@lib","scope":"private","path":"x.md"}`, extractText(t, res))

	res = callTool(t, srv, "workflow_read_file", libArgs("path", "x.md"))
	assert.Equal(t, "SHARED", extractText(t, res))
}

func TestLibTool_DeleteMissingIsNotFound(t *testing.T) {
	srv, _ := newLibraryServer(t)
	res := callTool(t, srv, "workflow_delete_file", libArgs("path", "nope.md"))
	assert.True(t, res.IsError)
	assert.Equal(t, "not found: nope.md", extractText(t, res))
}

func TestLibTool_NonAdminSharedWriteRefused(t *testing.T) {
	srv, _ := newLibraryServer(t)
	for _, tool := range []string{"workflow_write_file", "workflow_delete_file"} {
		res := callTool(t, srv, tool, libArgs("path", "s.md", "content", "x", "scope", "shared"))
		assert.True(t, res.IsError, tool)
		assert.Equal(t, "the shared library requires admin", extractText(t, res), tool)
	}
}

func TestLibTool_ScopeWithOtherNameRefused(t *testing.T) {
	srv, _ := newLibraryServer(t)
	for _, tool := range []string{"workflow_list_files", "workflow_read_file", "workflow_write_file", "workflow_delete_file"} {
		res := callTool(t, srv, tool, map[string]any{"name": "wf1", "path": "a", "content": "c", "scope": "shared"})
		assert.True(t, res.IsError, tool)
		assert.Equal(t, "scope is only valid with name @lib", extractText(t, res), tool)
	}
}

func TestLibTool_ReservedSpellingsRefused(t *testing.T) {
	srv, _ := newLibraryServer(t)
	for _, name := range []string{"_lib", "_LIB", "@LIB", "_Lib"} {
		for _, tool := range []string{"workflow_list_files", "workflow_read_file", "workflow_write_file", "workflow_delete_file"} {
			res := callTool(t, srv, tool, map[string]any{"name": name, "path": "a.md", "content": "c"})
			assert.True(t, res.IsError, "%s %s", tool, name)
			assert.Contains(t, extractText(t, res), "use @lib", "%s %s", tool, name)
		}
	}
}

func TestLibTool_InvalidPathRefused(t *testing.T) {
	srv, _ := newLibraryServer(t)
	for _, p := range []string{"../x", "workflow.json"} {
		for _, tool := range []string{"workflow_read_file", "workflow_write_file", "workflow_delete_file"} {
			res := callTool(t, srv, tool, libArgs("path", p, "content", "c"))
			assert.True(t, res.IsError, "%s %s", tool, p)
			assert.Equal(t, "invalid library path: "+p, extractText(t, res), "%s %s", tool, p)
		}
	}
}

func TestLibTool_UnknownScopeValueRefused(t *testing.T) {
	srv, _ := newLibraryServer(t)
	for _, tool := range []string{"workflow_read_file", "workflow_write_file", "workflow_delete_file"} {
		res := callTool(t, srv, tool, libArgs("path", "x.md", "content", "c", "scope", "public"))
		assert.True(t, res.IsError, tool)
		assert.Contains(t, extractText(t, res), `invalid scope "public"`, tool)
	}
}

func TestLibTool_WriteSizeLimit(t *testing.T) {
	srv, _ := newLibraryServer(t)
	res := callTool(t, srv, "workflow_write_file", libArgs("path", "big.md", "content", strings.Repeat("a", 10<<20)))
	assert.False(t, res.IsError, "10 MiB is accepted")
	res = callTool(t, srv, "workflow_write_file", libArgs("path", "big2.md", "content", strings.Repeat("a", 10<<20+1)))
	assert.True(t, res.IsError)
	assert.Contains(t, extractText(t, res), "content too large")
}

func TestLibTool_NonStringContentRefused(t *testing.T) {
	srv, dir := newLibraryServer(t)
	for _, bad := range []any{42, true, map[string]any{"a": 1}, nil} {
		res := callTool(t, srv, "workflow_write_file", map[string]any{"name": "@lib", "path": "n.md", "content": bad})
		assert.True(t, res.IsError, "content %v", bad)
		assert.Contains(t, extractText(t, res), "content must be a string")
	}
	assert.NoFileExists(t, filepath.Join(dir, "users", libOtherUserID.String(), "_lib", "n.md"))
	listed := callTool(t, srv, "workflow_list_files", libArgs())
	var l workflow.LibraryListing
	require.NoError(t, json.Unmarshal([]byte(extractText(t, listed)), &l))
	assert.Empty(t, l.Private, "nothing written")
}

// A workflow-scoped read of an @lib/ path goes through the file reader; its
// errors can embed absolute server paths and must be mapped, not echoed.
func TestWorkflowReadFile_LibPathErrorsAreSanitized(t *testing.T) {
	const leak = "/var/secret/arbitron/workspaces/users/abc/_lib"
	cases := map[string]error{
		"invalid":  fmt.Errorf("%w: %q escapes the library", workflow.ErrInvalidLibPath, leak),
		"notfound": fmt.Errorf("%w: %s/x.md", workflow.ErrFileNotFound, leak),
		"other":    fmt.Errorf("open %s/x.md: permission denied", leak),
	}
	for name, rerr := range cases {
		t.Run(name, func(t *testing.T) {
			deps := &adaptmcp.Deps{Workflows: &stubWorkflowRepo{}, FileReader: &stubFileReader{
				readFileFn: func(string, string) ([]byte, error) { return nil, rerr },
			}}
			srv := adaptmcp.NewServerWithDeps("test", "0.0.0", deps)
			adaptmcp.RegisterWorkflowTools(srv)
			res := callTool(t, srv, "workflow_read_file", map[string]any{"name": "some-wf", "path": "@lib/x.md"})
			require.True(t, res.IsError)
			txt := extractText(t, res)
			assert.NotContains(t, txt, "/var/")
			assert.NotContains(t, txt, leak)
		})
	}
}

func TestLibTool_ErrorsDoNotLeakServerPaths(t *testing.T) {
	srv, dir := newLibraryServer(t)
	res := callTool(t, srv, "workflow_read_file", libArgs("path", "missing.md"))
	assert.True(t, res.IsError)
	assert.NotContains(t, extractText(t, res), dir)
}

func TestLibTool_UnavailableWithoutStore(t *testing.T) {
	deps := &adaptmcp.Deps{Workflows: &stubWorkflowRepo{}, FileReader: &stubFileReader{}}
	srv := adaptmcp.NewServerWithDeps("test", "0.0.0", deps)
	adaptmcp.RegisterWorkflowTools(srv)
	res := callTool(t, srv, "workflow_list_files", libArgs())
	assert.True(t, res.IsError)
	assert.Contains(t, extractText(t, res), "library is not configured")
}

func TestLibTool_NoIdentityRefused(t *testing.T) {
	srv, _ := newLibraryServer(t)
	res := callToolCtx(t, srv, t.Context(), "workflow_list_files", libArgs())
	assert.True(t, res.IsError)
}
