package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var fsDefaultUser = uuid.MustParse("00000000-0000-0000-0000-000000000001")

func TestFlatStoreMigrationPreservesLayoutArtifacts(t *testing.T) {
	base := t.TempDir()
	names := []string{".arbitron-locks", ".arbitron-layout-journal", ".arbitron-tree-commits", ".demo.tmp-" + uuid.NewString(), ".demo.old-" + uuid.NewString()}
	for _, name := range names {
		flatWorkflow(t, base, name)
	}
	require.NoError(t, migrateFlatStore(base, fsDefaultUser))
	for _, name := range names {
		_, err := os.Stat(filepath.Join(base, name, "workflow.json"))
		require.NoError(t, err, "artifact %s must not migrate", name)
	}
}

// flatWorkflow writes a flat workflow folder (workflow.json + an extra file)
// under base.
func flatWorkflow(t *testing.T, base, name string) {
	t.Helper()
	dir := filepath.Join(base, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "workflow.json"), []byte(`{"name":"`+name+`"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "extra.txt"), []byte("data-"+name), 0o644))
}

// --- Happy path ---

func TestMigrateFlatStore_MovesFlatWorkflows(t *testing.T) {
	base := t.TempDir()
	flatWorkflow(t, base, "alpha")
	flatWorkflow(t, base, "beta")

	require.NoError(t, migrateFlatStore(base, fsDefaultUser))

	userRoot := filepath.Join(base, "users", fsDefaultUser.String())
	for _, name := range []string{"alpha", "beta"} {
		mf, err := os.ReadFile(filepath.Join(userRoot, name, "workflow.json"))
		require.NoError(t, err)
		assert.Contains(t, string(mf), name)
		extra, err := os.ReadFile(filepath.Join(userRoot, name, "extra.txt"))
		require.NoError(t, err)
		assert.Equal(t, "data-"+name, string(extra))
		// Original location gone.
		_, err = os.Stat(filepath.Join(base, name))
		assert.True(t, os.IsNotExist(err))
	}
	// Marker written.
	_, err := os.Stat(filepath.Join(base, flatStoreMarker))
	require.NoError(t, err)
}

// --- Validation ---

func TestMigrateFlatStore_LeavesNonWorkflowAndReservedDirs(t *testing.T) {
	base := t.TempDir()
	flatWorkflow(t, base, "wf")
	// A stray dir without workflow.json.
	require.NoError(t, os.MkdirAll(filepath.Join(base, "notes"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(base, "notes", "todo.md"), []byte("x"), 0o644))
	// Reserved roots.
	require.NoError(t, os.MkdirAll(filepath.Join(base, "instances", "abc"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(base, "shared", "pub"), 0o755))

	require.NoError(t, migrateFlatStore(base, fsDefaultUser))

	// Stray dir untouched.
	_, err := os.Stat(filepath.Join(base, "notes", "todo.md"))
	require.NoError(t, err)
	// Reserved roots untouched.
	_, err = os.Stat(filepath.Join(base, "instances", "abc"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(base, "shared", "pub"))
	require.NoError(t, err)
	// The actual workflow moved.
	_, err = os.Stat(filepath.Join(base, "users", fsDefaultUser.String(), "wf", "workflow.json"))
	require.NoError(t, err)
}

// --- Error/failure ---

func TestMigrateFlatStore_LeavesLayoutArtifactsAndStagingTrees(t *testing.T) {
	base := t.TempDir()
	names := []string{".arbitron-locks", ".arbitron-layout-journal", ".arbitron-tree-commits", ".target.tmp-00000000-0000-0000-0000-000000000001", ".target.old-00000000-0000-0000-0000-000000000001"}
	for _, name := range names {
		flatWorkflow(t, base, name)
	}
	flatWorkflow(t, base, "ordinary")
	require.NoError(t, migrateFlatStore(base, fsDefaultUser))
	for _, name := range names {
		require.FileExists(t, filepath.Join(base, name, "workflow.json"))
		require.NoDirExists(t, filepath.Join(base, "users", fsDefaultUser.String(), name))
	}
	require.FileExists(t, filepath.Join(base, "users", fsDefaultUser.String(), "ordinary", "workflow.json"))
}

func TestMigrateFlatStore_CollisionErrorsWithoutClobber(t *testing.T) {
	base := t.TempDir()
	flatWorkflow(t, base, "dup")

	// Pre-existing destination with different content.
	dst := filepath.Join(base, "users", fsDefaultUser.String(), "dup")
	require.NoError(t, os.MkdirAll(dst, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dst, "workflow.json"), []byte(`{"name":"existing"}`), 0o644))

	err := migrateFlatStore(base, fsDefaultUser)
	require.Error(t, err)

	// Destination not clobbered.
	mf, rerr := os.ReadFile(filepath.Join(dst, "workflow.json"))
	require.NoError(t, rerr)
	assert.Contains(t, string(mf), "existing")
}

// --- Boundary ---

func TestMigrateFlatStore_EmptyStoreWritesMarker(t *testing.T) {
	base := t.TempDir()

	require.NoError(t, migrateFlatStore(base, fsDefaultUser))

	_, err := os.Stat(filepath.Join(base, flatStoreMarker))
	require.NoError(t, err)
}

func TestMigrateFlatStore_Idempotent(t *testing.T) {
	base := t.TempDir()
	flatWorkflow(t, base, "wf")

	require.NoError(t, migrateFlatStore(base, fsDefaultUser))
	// Add a NEW flat workflow after the first migration; a second run must
	// short-circuit on the marker and NOT move it.
	flatWorkflow(t, base, "late")

	require.NoError(t, migrateFlatStore(base, fsDefaultUser))

	// "late" stays flat (marker short-circuit).
	_, err := os.Stat(filepath.Join(base, "late", "workflow.json"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(base, "users", fsDefaultUser.String(), "late"))
	assert.True(t, os.IsNotExist(err))
}

// --- Isolation/security ---

func TestMigrateFlatStore_PermsAndMarkerMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits (0700/0600) are not honored on Windows filesystems")
	}
	base := t.TempDir()
	flatWorkflow(t, base, "wf")

	require.NoError(t, migrateFlatStore(base, fsDefaultUser))

	userRoot := filepath.Join(base, "users", fsDefaultUser.String())
	usersParent := filepath.Join(base, "users")

	uinfo, err := os.Stat(userRoot)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), uinfo.Mode().Perm(), "per-user dir must be 0o700")

	pinfo, err := os.Stat(usersParent)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), pinfo.Mode().Perm(), "users/ parent must be 0o700")

	minfo, err := os.Stat(filepath.Join(base, flatStoreMarker))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), minfo.Mode().Perm(), "marker must be 0o600")
}

// Migration only renames folders: it must never stamp a revision, write
// presentation metadata or leave a layout journal entry behind.
func TestMigrateFlatStore_NeverStampsOrJournals(t *testing.T) {
	base := t.TempDir()
	flatWorkflow(t, base, "alpha")
	before, err := os.ReadFile(filepath.Join(base, "alpha", "workflow.json"))
	require.NoError(t, err)

	require.NoError(t, migrateFlatStore(base, fsDefaultUser))

	after, err := os.ReadFile(filepath.Join(base, "users", fsDefaultUser.String(), "alpha", "workflow.json"))
	require.NoError(t, err)
	require.Equal(t, before, after, "migration must move workflow.json byte-for-byte")
	require.NotContains(t, string(after), "presentation")
	for _, dir := range []string{".arbitron-layout-journal", ".arbitron-tree-commits"} {
		require.NoDirExists(t, filepath.Join(base, dir), "migration must not create %s", dir)
	}
}
