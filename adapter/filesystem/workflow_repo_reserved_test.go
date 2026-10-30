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

// plantLibManifest writes a valid workflow.json inside <scopeRoot>/_lib so the
// folder looks exactly like a workflow to any enumerator that forgets to skip it.
func plantLibManifest(t *testing.T, scopeRoot string) {
	t.Helper()
	dir := filepath.Join(scopeRoot, workflow.LibraryFolder)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "workflow.json"),
		[]byte(`{"name":"_lib","description":"d","nodes":[]}`), 0o644))
}

// --- Happy path ---

func TestWorkflowRepo_ListHidesLibEvenWithManifest(t *testing.T) {
	base := t.TempDir()
	repo := filesystem.NewWorkflowRepo(base)
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, workflow.Shared(), "real", sampleWorkflow("real")))
	plantLibManifest(t, filepath.Join(base, "shared"))

	got, err := repo.List(ctx, workflow.Shared())

	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "real", got[0].Name)
}

func TestWorkflowRepo_LoadManifestsSkipsLib(t *testing.T) {
	base := t.TempDir()
	repo := filesystem.NewWorkflowRepo(base)
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, workflow.Shared(), "real", sampleWorkflow("real")))
	plantLibManifest(t, filepath.Join(base, "shared"))

	got, err := repo.LoadManifests(ctx, workflow.Shared())

	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "real", got[0].Name)
}

// --- Error/failure ---

func TestWorkflowRepo_ReservedNameRejectedEverywhere(t *testing.T) {
	ctx := context.Background()
	owner := uuid.New()
	for _, scope := range []workflow.Scope{workflow.Shared(), workflow.Private(owner)} {
		for _, name := range []string{"_lib", "_LIB"} {
			repo := filesystem.NewWorkflowRepo(t.TempDir())
			ops := map[string]func() error{
				"Create": func() error { return repo.Create(ctx, scope, name, sampleWorkflow(name)) },
				"Update": func() error { return repo.Update(ctx, scope, name, sampleWorkflow(name)) },
				"Delete": func() error { return repo.Delete(ctx, scope, name) },
				"Exists": func() error { _, err := repo.Exists(ctx, scope, name); return err },
				"WriteFile": func() error {
					return repo.WriteFile(ctx, scope, name, "x.md", []byte("x"))
				},
				"DeleteFile": func() error { return repo.DeleteFile(ctx, scope, name, "x.md") },
				"ListFiles":  func() error { _, err := repo.ListFiles(ctx, scope, name); return err },
				"CopyTree": func() error {
					return repo.CopyTree(ctx, scope, "src", scope, name, false)
				},
			}
			for op, call := range ops {
				assert.ErrorIs(t, call(), workflow.ErrInvalidName, "%s(%q)", op, name)
			}
			_, err := repo.Load(ctx, scope, name)
			assert.ErrorIs(t, err, workflow.ErrNotFound, "Load(%q)", name)
		}
	}
}

// Review Focus 1: a workflow-scoped path that cleans to @lib/x can never be
// reached by a read, so writing it would strand the file.
func TestWorkflowRepo_WriteFileRejectsLibPrefixedPath(t *testing.T) {
	ctx := context.Background()
	repo := filesystem.NewWorkflowRepo(t.TempDir())
	scope := workflow.Private(uuid.New())
	require.NoError(t, repo.Create(ctx, scope, "wf", sampleWorkflow("wf")))

	for _, p := range []string{"@lib/x", "./@lib/x", "a/../@lib/x"} {
		err := repo.WriteFile(ctx, scope, "wf", p, []byte("x"))
		assert.ErrorIs(t, err, workflow.ErrInvalidFilePath, "WriteFile(%q)", p)
	}
	files, err := repo.ListFiles(ctx, scope, "wf")
	require.NoError(t, err)
	assert.Empty(t, files)
}

func TestFileReader_ReservedWorkflowNameRejectedForNonLibPath(t *testing.T) {
	base := t.TempDir()
	owner := uuid.New()
	for _, root := range []string{
		filepath.Join(base, "users", owner.String(), "_lib"),
		filepath.Join(base, "shared", "_lib"),
	} {
		require.NoError(t, os.MkdirAll(root, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, "x.md"), []byte("secret"), 0o644))
	}
	reader := filesystem.NewFileReader(base)

	for _, name := range []string{"_lib", "_LIB", "@lib"} {
		data, err := reader.ReadFile(owner, name, "x.md")
		assert.Nil(t, data, name)
		assert.ErrorIs(t, err, workflow.ErrInvalidName, name)
		assert.False(t, reader.FileExists(owner, name, "x.md"), name)
	}
}

// --- Boundary ---

func TestWorkflowRepo_NearMissNamesAllowed(t *testing.T) {
	ctx := context.Background()
	repo := filesystem.NewWorkflowRepo(t.TempDir())
	for _, name := range []string{"lib", "_lib2", "my_lib"} {
		require.NoError(t, repo.Create(ctx, workflow.Shared(), name, sampleWorkflow(name)), name)
	}
	got, err := repo.List(ctx, workflow.Shared())
	require.NoError(t, err)
	assert.Len(t, got, 3)
}
