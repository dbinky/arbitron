package filesystem_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ modelcatalog.WorkflowScopeSource = (*filesystem.WorkflowRepo)(nil)

func TestLoadManifests_ReturnsEveryWorkflowWithRawContent(t *testing.T) {
	base := t.TempDir()
	repo := filesystem.NewWorkflowRepo(base)
	owner := uuid.New()
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, workflow.Private(owner), "a", sampleWorkflow("a")))
	require.NoError(t, repo.Create(ctx, workflow.Private(owner), "b", sampleWorkflow("b")))

	got, err := repo.LoadManifests(ctx, workflow.Private(owner))

	require.NoError(t, err)
	require.Len(t, got, 2)
	names := []string{got[0].Name, got[1].Name}
	assert.ElementsMatch(t, []string{"a", "b"}, names)
	for _, m := range got {
		assert.Equal(t, workflow.Private(owner), m.Scope)
		require.NotNil(t, m.Workflow)
		assert.Equal(t, m.Name, m.Workflow.Name)
		raw, err := os.ReadFile(filepath.Join(base, "users", owner.String(), m.Name, "workflow.json"))
		require.NoError(t, err)
		assert.Equal(t, raw, m.Content)
	}
}

func TestLoadManifests_MissingScopeRootIsEmpty(t *testing.T) {
	repo := filesystem.NewWorkflowRepo(t.TempDir())

	got, err := repo.LoadManifests(context.Background(), workflow.Shared())

	require.NoError(t, err)
	assert.Empty(t, got)
}

// Repository.List silently skips a broken manifest; the inventory must not, or a
// workflow's model consumers would vanish from eligibility.
func TestLoadManifests_UnparseableManifestIsIncompleteInventory(t *testing.T) {
	base := t.TempDir()
	repo := filesystem.NewWorkflowRepo(base)
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, workflow.Shared(), "ok", sampleWorkflow("ok")))
	broken := filepath.Join(base, "shared", "broken")
	require.NoError(t, os.MkdirAll(broken, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(broken, "workflow.json"), []byte("{not json"), 0o644))

	got, err := repo.LoadManifests(ctx, workflow.Shared())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "broken")
	assert.Nil(t, got)
}

func TestLoadManifests_UnreadableManifestIsIncompleteInventory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	base := t.TempDir()
	repo := filesystem.NewWorkflowRepo(base)
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, workflow.Shared(), "locked", sampleWorkflow("locked")))
	manifest := filepath.Join(base, "shared", "locked", "workflow.json")
	require.NoError(t, os.Chmod(manifest, 0o000))
	t.Cleanup(func() { _ = os.Chmod(manifest, 0o644) })

	_, err := repo.LoadManifests(ctx, workflow.Shared())

	require.Error(t, err)
}

// Directories the runtime can never load as a workflow (no manifest, staging and
// backup artifacts of CopyTree, stray files) hold no model consumers.
func TestLoadManifests_SkipsNonWorkflowEntries(t *testing.T) {
	base := t.TempDir()
	repo := filesystem.NewWorkflowRepo(base)
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, workflow.Shared(), "real", sampleWorkflow("real")))
	shared := filepath.Join(base, "shared")
	require.NoError(t, os.MkdirAll(filepath.Join(shared, "empty-dir"), 0o755))
	staging := filepath.Join(shared, "real.tmp-"+uuid.NewString())
	require.NoError(t, os.MkdirAll(staging, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(staging, "workflow.json"), []byte("{partial"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(shared, "notes.txt"), []byte("x"), 0o644))

	got, err := repo.LoadManifests(ctx, workflow.Shared())

	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "real", got[0].Name)
}

func TestLoadManifests_CancelledContext(t *testing.T) {
	repo := filesystem.NewWorkflowRepo(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := repo.LoadManifests(ctx, workflow.Shared())

	assert.ErrorIs(t, err, context.Canceled)
}
