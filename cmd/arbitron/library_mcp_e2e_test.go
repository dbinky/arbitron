package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
)

var libUserC = uuid.MustParse("00000000-0000-0000-0000-0000000000c3")

func libUserCtx(id identity.Identity) context.Context {
	return identity.WithIdentity(context.Background(), id)
}

// The identity the stdio MCP transport builds in local mode is an admin, so
// the local operator can write the shared library; a plain user cannot.
func TestLibTool_LocalModeIdentityCanWriteShared(t *testing.T) {
	ds, workspaceDir := bootGroupAuthoringStack(t)
	srv := newGroupMCPServer(ds)

	localCtx, err := ds.stdioContext(context.Background())
	require.NoError(t, err)
	res := callMCPTool(t, srv, localCtx, "workflow_write_file",
		map[string]any{"name": "@lib", "path": "common.md", "content": "shared text", "scope": "shared"})
	require.False(t, res.IsError, mcpText(t, res))
	assert.FileExists(t, filepath.Join(workspaceDir, "shared", "_lib", "common.md"))

	userCtx := libUserCtx(identity.Identity{UserID: groupOtherUser, Username: "bob", Role: identity.RoleUser})
	res = callMCPTool(t, srv, userCtx, "workflow_write_file",
		map[string]any{"name": "@lib", "path": "evil.md", "content": "x", "scope": "shared"})
	assert.True(t, res.IsError)
	assert.Equal(t, "the shared library requires admin", mcpText(t, res))
	assert.NoFileExists(t, filepath.Join(workspaceDir, "shared", "_lib", "evil.md"))
}

// An MCP-written library file is expanded by the run-time template loader, in
// the run owner's scope: shared is visible to everyone, and a private copy
// shadows it for its owner only.
func TestLibTool_SharedWriteVisibleToAnotherUsersRunInclude(t *testing.T) {
	ds, workspaceDir := bootGroupAuthoringStack(t)
	srv := newGroupMCPServer(ds)
	admin := adminCtxForLocalUser(t, ds)
	userB := libUserCtx(identity.Identity{UserID: groupOtherUser, Username: "bob", Role: identity.RoleUser})

	res := callMCPTool(t, srv, admin, "workflow_write_file",
		map[string]any{"name": "@lib", "path": "x.md", "content": "SHARED-X", "scope": "shared"})
	require.False(t, res.IsError, mcpText(t, res))

	// A template per runner, written next to the runner's own workflow.
	for _, owner := range []string{groupOtherUser.String(), libUserC.String()} {
		dir := filepath.Join(workspaceDir, "users", owner, "tw")
		require.NoError(t, os.MkdirAll(dir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "prompt.md"), []byte("before {{include:@lib/x.md}} after"), 0o600))
	}

	reader := filesystem.NewFileReader(workspaceDir)
	data := engine.GroupResolverPromptData{TaskKey: "a", WorkDir: t.TempDir(), ConflictPaths: []string{"f.txt"}}
	render := func(owner uuid.UUID) string {
		t.Helper()
		out, err := engine.LoadGroupResolverPrompt(reader, owner, "tw", "prompt.md", data)
		require.NoError(t, err)
		return out
	}
	assert.Contains(t, render(groupOtherUser), "before SHARED-X after")
	assert.Contains(t, render(libUserC), "before SHARED-X after")

	// B's private copy shadows the shared one for B alone.
	res = callMCPTool(t, srv, userB, "workflow_write_file",
		map[string]any{"name": "@lib", "path": "x.md", "content": "PRIVATE-B"})
	require.False(t, res.IsError, mcpText(t, res))
	assert.Contains(t, render(groupOtherUser), "before PRIVATE-B after")
	assert.Contains(t, render(libUserC), "before SHARED-X after")
}
