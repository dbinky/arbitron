package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/adapter/checkpoint"
	"github.com/stretchr/testify/require"
)

func TestResolutionPortability_RejectsSymlinkedResourceRoot(t *testing.T) {
	f, h := conflictedStaging(t, false)
	outside := t.TempDir()
	root := f.a.ResolutionResourceRoot()
	require.NoError(t, os.MkdirAll(filepath.Dir(root), 0700))
	require.NoError(t, os.Symlink(outside, root))
	require.ErrorIs(t, f.a.ExportResolution(context.Background(), h.StagingDir, resourceDst(f)), ErrResolutionPortability)
	entries, err := os.ReadDir(outside)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestResolutionPortability_BoundsMaterializedObjects(t *testing.T) {
	f, h := conflictedStaging(t, false)
	require.NoError(t, WithResolutionMaxBytes(1<<20)(f.a))
	require.NoError(t, WithPortableGit(func(ctx context.Context, src, dst string) error {
		if err := checkpoint.MaterializePortableGit(ctx, src, dst); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, ".git", "objects", "expanded"), make([]byte, 2<<20), 0600)
	})(f.a))
	dst := resourceDst(f)
	require.ErrorIs(t, f.a.ExportResolution(context.Background(), h.StagingDir, dst), ErrResolutionTooLarge)
	_, err := os.Stat(dst)
	require.True(t, os.IsNotExist(err))
}
