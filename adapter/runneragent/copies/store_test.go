package copies_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/adapter/runneragent/copies"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ runneragent.CopyStateStore = (*copies.FileStore)(nil)

func TestFileStore_RoundTripListDelete(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "copies")
	s, err := copies.NewFileStore(dir)
	require.NoError(t, err)
	a := runneragent.CopyState{RunID: uuid.New(), WorkDir: "/w/instances/a", HomeGeneration: 2, CheckpointVersion: 5, ManifestHash: "h",
		Base: runner.NewManifest(5, []runner.ManifestEntry{{Path: "x", Size: 1, Mode: 0o644, SHA256: "s"}})}
	b := runneragent.CopyState{RunID: uuid.New(), WorkDir: "/w/instances/b", HomeGeneration: 1, Dirty: true}
	require.NoError(t, s.Put(ctx, a))
	require.NoError(t, s.Put(ctx, b))
	s, err = copies.NewFileStore(dir)
	require.NoError(t, err, "records survive reopening the store")
	got, ok, err := s.Get(ctx, a.RunID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, a.Base, got.Base)
	assert.Equal(t, int64(5), got.CheckpointVersion)
	info, err := os.Stat(filepath.Join(dir, a.RunID.String()+".json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	all, err := s.List(ctx)
	require.NoError(t, err)
	assert.ElementsMatch(t, []runneragent.CopyState{a, b}, all)
	require.NoError(t, s.Delete(ctx, a.RunID))
	require.NoError(t, s.Delete(ctx, a.RunID), "deleting twice is not an error")
	_, ok, err = s.Get(ctx, a.RunID)
	require.NoError(t, err)
	assert.False(t, ok)
}
