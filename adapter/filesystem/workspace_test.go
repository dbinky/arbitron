package filesystem

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceManager(t *testing.T) {
	t.Run("EnsureInstanceDir creates the directory and returns its path", func(t *testing.T) {
		base := t.TempDir()
		wm, err := NewWorkspaceManager(base)
		require.NoError(t, err)
		id := uuid.New()

		path, err := wm.EnsureInstanceDir(id)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(base, "instances", id.String()), path)

		info, statErr := os.Stat(path)
		require.NoError(t, statErr)
		assert.True(t, info.IsDir())
	})

	t.Run("instance dirs are created mode 0o700 (owner-only)", func(t *testing.T) {
		// SECURITY: PR #8 thread 3291943659. Default 0o755 lets other local
		// users read agent workspace contents (intermediate prompts, dumped
		// secrets, tool outputs). 0o700 makes them owner-only.
		base := t.TempDir()
		wm, err := NewWorkspaceManager(base)
		require.NoError(t, err)
		id := uuid.New()

		path, err := wm.EnsureInstanceDir(id)
		require.NoError(t, err)

		info, statErr := os.Stat(path)
		require.NoError(t, statErr)
		assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(),
			"per-instance dir must be 0o700")

		// Parent instances/ dir must also be 0o700.
		parentInfo, err := os.Stat(filepath.Join(base, "instances"))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o700), parentInfo.Mode().Perm(),
			"parent instances/ dir must be 0o700")
	})

	t.Run("NewWorkspaceManager resolves basePath to absolute", func(t *testing.T) {
		// SF10 / thread 3291907750: an adapter that touches the filesystem
		// owns its own invariants. Storing the absolute form means later
		// WorkDir-pin checks have a canonical root to compare against.
		base := t.TempDir() // already absolute
		wm, err := NewWorkspaceManager(base)
		require.NoError(t, err)
		assert.True(t, filepath.IsAbs(wm.BasePath()),
			"BasePath must be absolute after construction")
	})

	t.Run("NewWorkspaceManager rejects empty basePath", func(t *testing.T) {
		_, err := NewWorkspaceManager("")
		require.Error(t, err)
	})

	t.Run("EnsureInstanceDir is idempotent", func(t *testing.T) {
		wm, err := NewWorkspaceManager(t.TempDir())
		require.NoError(t, err)
		id := uuid.New()

		p1, err := wm.EnsureInstanceDir(id)
		require.NoError(t, err)
		p2, err := wm.EnsureInstanceDir(id)
		require.NoError(t, err)
		assert.Equal(t, p1, p2)
	})

	t.Run("RemoveInstanceDir deletes the directory", func(t *testing.T) {
		wm, err := NewWorkspaceManager(t.TempDir())
		require.NoError(t, err)
		id := uuid.New()
		path, err := wm.EnsureInstanceDir(id)
		require.NoError(t, err)

		require.NoError(t, wm.RemoveInstanceDir(id))
		_, statErr := os.Stat(path)
		assert.True(t, os.IsNotExist(statErr))
	})

	t.Run("RemoveInstanceDir on a missing directory is not an error", func(t *testing.T) {
		wm, err := NewWorkspaceManager(t.TempDir())
		require.NoError(t, err)
		assert.NoError(t, wm.RemoveInstanceDir(uuid.New()))
	})

	t.Run("InstanceDirStats counts bytes and files recursively", func(t *testing.T) {
		// SF9 / PR #8 thread 3291941512: engine uses these stats to emit a
		// retain-on-failure warning showing how much agent-generated data
		// is sitting on disk.
		wm, err := NewWorkspaceManager(t.TempDir())
		require.NoError(t, err)
		id := uuid.New()
		dir, err := wm.EnsureInstanceDir(id)
		require.NoError(t, err)

		require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o600))
		require.NoError(t, os.Mkdir(filepath.Join(dir, "sub"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("world!"), 0o600))

		bytesTotal, files, err := wm.InstanceDirStats(id)
		require.NoError(t, err)
		assert.Equal(t, int64(11), bytesTotal) // 5 + 6
		assert.Equal(t, 2, files)
	})

	t.Run("InstanceDirStats returns zeros for a missing dir", func(t *testing.T) {
		wm, err := NewWorkspaceManager(t.TempDir())
		require.NoError(t, err)
		bytesTotal, files, err := wm.InstanceDirStats(uuid.New())
		require.NoError(t, err)
		assert.Equal(t, int64(0), bytesTotal)
		assert.Equal(t, 0, files)
	})
}
