package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/checkpoint"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildWorkspaceMobility_ExposesThePrunedStore(t *testing.T) {
	st := bootP3Stack(t, nil, "")
	m := st.ds.mobility
	require.Same(t, m.Checkpoints, m.CheckpointStore)

	// Feed the shared baseline/artifact producer, not a second test-only store.
	ctx := context.Background()
	run := uuid.New()
	src, err := m.Workspaces.EnsureInstanceDir(run)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(src, "value"), []byte("committed"), 0o600))
	require.NoError(t, m.Baseline.EnsureBaseline(ctx, run, src))
	base, ok, err := m.Checkpoints.Latest(ctx, run)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, os.WriteFile(filepath.Join(src, "value"), []byte("abandoned"), 0o600))
	orphan, err := m.Checkpoints.StageResource(ctx, run, src, base)
	require.NoError(t, err)
	old := time.Now().Add(-2 * checkpoint.DefaultPruneGrace)
	require.NoError(t, filepath.Walk(filepath.Dir(orphan.ArchivePath), func(path string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(path, old, old)
	}))
	require.FileExists(t, orphan.ArchivePath)

	st.crashAndRestart(t)
	require.Same(t, st.ds.mobility.Checkpoints, st.ds.mobility.CheckpointStore)
	require.Eventually(t, func() bool {
		_, err := os.Stat(orphan.ArchivePath)
		return os.IsNotExist(err)
	}, 5*time.Second, 10*time.Millisecond, "SINK: runtime boot prunes the real orphan artifact")
	dst := t.TempDir()
	require.NoError(t, st.ds.mobility.Checkpoints.Restore(ctx, base, dst))
	value, err := os.ReadFile(filepath.Join(dst, "value"))
	require.NoError(t, err)
	assert.Equal(t, "committed", string(value), "the referenced checkpoint survives pruning")
}
