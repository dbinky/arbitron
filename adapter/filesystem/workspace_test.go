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
		wm := NewWorkspaceManager(base)
		id := uuid.New()

		path, err := wm.EnsureInstanceDir(id)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(base, "instances", id.String()), path)

		info, statErr := os.Stat(path)
		require.NoError(t, statErr)
		assert.True(t, info.IsDir())
	})

	t.Run("EnsureInstanceDir is idempotent", func(t *testing.T) {
		wm := NewWorkspaceManager(t.TempDir())
		id := uuid.New()

		p1, err := wm.EnsureInstanceDir(id)
		require.NoError(t, err)
		p2, err := wm.EnsureInstanceDir(id)
		require.NoError(t, err)
		assert.Equal(t, p1, p2)
	})

	t.Run("RemoveInstanceDir deletes the directory", func(t *testing.T) {
		wm := NewWorkspaceManager(t.TempDir())
		id := uuid.New()
		path, err := wm.EnsureInstanceDir(id)
		require.NoError(t, err)

		require.NoError(t, wm.RemoveInstanceDir(id))
		_, statErr := os.Stat(path)
		assert.True(t, os.IsNotExist(statErr))
	})

	t.Run("RemoveInstanceDir on a missing directory is not an error", func(t *testing.T) {
		wm := NewWorkspaceManager(t.TempDir())
		assert.NoError(t, wm.RemoveInstanceDir(uuid.New()))
	})
}
