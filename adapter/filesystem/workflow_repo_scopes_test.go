package filesystem_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListScopes_MissingRootsAreEmptyNotError(t *testing.T) {
	repo := filesystem.NewWorkflowRepo(t.TempDir())

	scopes, err := repo.ListScopes(context.Background())

	require.NoError(t, err)
	assert.Empty(t, scopes)
}

func TestListScopes_FindsSharedAndEveryOwnerOnDisk(t *testing.T) {
	base := t.TempDir()
	ownerA := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	ownerB := uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	require.NoError(t, os.MkdirAll(filepath.Join(base, "shared"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(base, "users", ownerA.String()), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(base, "users", ownerB.String()), 0o700))
	// A stray regular file cannot hold workflows and must not fail the scan.
	require.NoError(t, os.WriteFile(filepath.Join(base, "users", ".DS_Store"), nil, 0o600))
	repo := filesystem.NewWorkflowRepo(base)

	scopes, err := repo.ListScopes(context.Background())

	require.NoError(t, err)
	assert.ElementsMatch(t, []workflow.Scope{
		workflow.Shared(), workflow.Private(ownerA), workflow.Private(ownerB),
	}, scopes)
}

func TestListScopes_FindsOwnerAbsentFromAnyRegistry(t *testing.T) {
	base := t.TempDir()
	repo := filesystem.NewWorkflowRepo(base)
	owner := uuid.New()
	// Created straight on disk (as a CLI user or restore would), never registered.
	require.NoError(t, repo.Create(context.Background(), workflow.Private(owner), "wf", sampleWorkflow("wf")))

	scopes, err := repo.ListScopes(context.Background())

	require.NoError(t, err)
	assert.Equal(t, []workflow.Scope{workflow.Private(owner)}, scopes)
}

func TestListScopes_MalformedOwnerDirectoryIsIncompleteInventory(t *testing.T) {
	for name, dir := range map[string]string{
		"non-uuid name": "not-a-uuid",
		"nil uuid":      uuid.Nil.String(),
	} {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(base, "users", dir), 0o700))
			repo := filesystem.NewWorkflowRepo(base)

			scopes, err := repo.ListScopes(context.Background())

			require.Error(t, err)
			assert.Nil(t, scopes, "a partial scope list must never be returned as complete")
		})
	}
}

func TestListScopes_UnreadableUsersDirIsIncompleteInventory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not bind root")
	}
	base := t.TempDir()
	users := filepath.Join(base, "users")
	require.NoError(t, os.MkdirAll(filepath.Join(users, uuid.NewString()), 0o700))
	require.NoError(t, os.Chmod(users, 0o000))
	t.Cleanup(func() { _ = os.Chmod(users, 0o700) })
	repo := filesystem.NewWorkflowRepo(base)

	scopes, err := repo.ListScopes(context.Background())

	require.Error(t, err)
	assert.Nil(t, scopes)
}

func TestListScopes_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	scopes, err := filesystem.NewWorkflowRepo(t.TempDir()).ListScopes(ctx)

	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, scopes)
}

func TestListScopes_UnreadableOwnerDirIsIncompleteInventory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not bind root")
	}
	base := t.TempDir()
	owner := filepath.Join(base, "users", uuid.NewString())
	require.NoError(t, os.MkdirAll(owner, 0o700))
	require.NoError(t, os.Chmod(owner, 0o000))
	t.Cleanup(func() { _ = os.Chmod(owner, 0o700) })

	scopes, err := filesystem.NewWorkflowRepo(base).ListScopes(context.Background())

	require.Error(t, err)
	assert.Nil(t, scopes)
}

// Regression pin: green on arrival. ListScopes enumerates users/<uuid>, not
// workflow folders, so a shared/_lib must not be mistaken for a scope.
func TestListScopes_SharedLibAddsNoScope(t *testing.T) {
	base := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(base, "shared", workflow.LibraryFolder), 0o700))
	repo := filesystem.NewWorkflowRepo(base)

	scopes, err := repo.ListScopes(context.Background())

	require.NoError(t, err)
	assert.Equal(t, []workflow.Scope{workflow.Shared()}, scopes)
}
