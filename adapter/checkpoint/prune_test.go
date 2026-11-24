// adapter/checkpoint/prune_test.go
package checkpoint_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/checkpoint"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ckVersionFile(storeDir string, run uuid.UUID, v int64, suffix string) string {
	return filepath.Join(storeDir, "runs", run.String(), fmt.Sprintf("v%010d%s", v, suffix))
}

// ckAge backdates every path (recursively for directories).
func ckAge(t *testing.T, at time.Time, paths ...string) {
	t.Helper()
	for _, p := range paths {
		require.NoError(t, filepath.Walk(p, func(q string, _ os.FileInfo, err error) error {
			require.NoError(t, err)
			return os.Chtimes(q, at, at)
		}))
	}
}

func ckExists(p string) bool { _, err := os.Lstat(p); return err == nil }

func TestStore_PruneRemovesOnlyUnreferencedExpiredArtifacts(t *testing.T) {
	ctx := context.Background()
	cat := newMemCatalog()
	store, storeDir := newCkStore(t, cat)
	now := time.Now()
	old := now.Add(-48 * time.Hour)
	run := uuid.New()
	ws := t.TempDir()
	ckWrite(t, ws, "a.txt", "v1", 0o644)
	v1, v1M, err := store.Commit(ctx, run, ws) // committed
	require.NoError(t, err)
	rc, err := store.ReadFile(ctx, v1, "a.txt") // fills cache v1
	require.NoError(t, err)
	require.NoError(t, rc.Close())

	ckWrite(t, ws, "a.txt", "v2", 0o644)
	f, res := ckOperation(cat, run)
	v2, _, err := store.StageBuiltIn(ctx, run, ws, f, res) // prepared row
	require.NoError(t, err)
	require.NoError(t, cat.commit(v2)) // pointer moves to v2; cache v1 is now stale
	cat.deactivate(f, res)             // the job and its claim are gone

	orphan, err := store.StageResource(ctx, run, ws, v2) // artifact, no row
	require.NoError(t, err)
	runnerCopy := t.TempDir()
	require.NoError(t, store.Restore(ctx, v1, runnerCopy))
	ckWrite(t, runnerCopy, "b.txt", "pushed", 0o644)
	delta, _ := ckDelta(t, runnerCopy, v1M.Entries)
	abandoned, err := store.StageDelta(ctx, run, v1, bytes.NewReader(delta)) // reserves a version...
	require.NoError(t, err)
	require.NoError(t, store.Discard(ctx, abandoned)) // ...then is abandoned: marker only
	ckWrite(t, ws, "a.txt", "in flight", 0o644)
	fresh, err := store.StageResource(ctx, run, ws, v2) // in-flight: within the grace period
	require.NoError(t, err)
	scratch := filepath.Join(storeDir, "staging", "op-crashed")
	require.NoError(t, os.MkdirAll(scratch, 0o700))

	ckAge(t, old, filepath.Join(storeDir, "runs"), filepath.Join(storeDir, "cache"), scratch)
	ckAge(t, now, ckVersionFile(storeDir, run, fresh.Version, ".tar.gz"), ckVersionFile(storeDir, run, fresh.Version, ".manifest.json"),
		ckVersionFile(storeDir, run, fresh.Version, ".reserved"))
	cacheV1 := filepath.Join(storeDir, "cache", run.String(), fmt.Sprintf("v%010d", v1.Version))

	rep, err := store.Prune(ctx, now)
	require.NoError(t, err)
	assert.Equal(t, 1, rep.Archives, "only the orphaned StageResource artifact")
	assert.Equal(t, 2, rep.Markers, "the orphan's and the abandoned push's reservations")
	assert.Equal(t, 1, rep.CacheEntries, "the stale cache of v1")
	assert.Equal(t, 1, rep.ScratchDirs)
	assert.Positive(t, rep.Bytes)
	assert.False(t, ckExists(orphan.ArchivePath))
	assert.False(t, ckExists(ckVersionFile(storeDir, run, abandoned.Version, ".reserved")))
	assert.False(t, ckExists(cacheV1))
	assert.False(t, ckExists(scratch))
	assert.True(t, ckExists(fresh.ArchivePath), "in-flight work inside the grace period is never raced")

	for _, ref := range []runner.CheckpointRef{v1, v2} {
		require.NoError(t, store.Restore(ctx, ref, t.TempDir()), "artifacts named by catalog rows survive: v%d", ref.Version)
	}
	again, err := store.Prune(ctx, now)
	require.NoError(t, err)
	assert.Equal(t, checkpoint.PruneReport{}, again, "idempotent")

	next, err := store.StageResource(ctx, run, ws, v2)
	require.NoError(t, err)
	assert.Greater(t, next.Version, fresh.Version, "pruning never lets a version be reused")
}

func TestStore_PruneHonoursGraceHeldRunsAndCatalogErrors(t *testing.T) {
	ctx := context.Background()
	cat := newMemCatalog()
	store, storeDir := newCkStore(t, cat, checkpoint.WithPruneGrace(time.Hour))
	now := time.Now()
	run := uuid.New()
	ws := t.TempDir()
	ckWrite(t, ws, "a", "a", 0o644)
	base, _, err := store.Commit(ctx, run, ws)
	require.NoError(t, err)
	young, err := store.StageResource(ctx, run, ws, base)
	require.NoError(t, err)
	ckAge(t, now.Add(-30*time.Minute), filepath.Join(storeDir, "runs"))
	rep, err := store.Prune(ctx, now)
	require.NoError(t, err)
	assert.Zero(t, rep.Archives, "30 minutes is inside a one-hour grace")
	assert.True(t, ckExists(young.ArchivePath))

	ckAge(t, now.Add(-2*time.Hour), filepath.Join(storeDir, "runs"))
	_, res := ckOperation(cat, run) // an active writer holds the run
	rep, err = store.Prune(ctx, now)
	require.NoError(t, err)
	assert.Equal(t, 1, rep.SkippedHeldRuns)
	assert.True(t, ckExists(young.ArchivePath), "nothing of a held run is removed")

	cat.deactivate(runner.Fence{}, res)
	cat.retentionErr = errors.New("database is locked")
	_, err = store.Prune(ctx, now)
	require.Error(t, err)
	assert.True(t, ckExists(young.ArchivePath), "a catalog error removes nothing")

	cat.retentionErr = nil
	rep, err = store.Prune(ctx, now)
	require.NoError(t, err)
	assert.Equal(t, 1, rep.Archives)
	assert.False(t, ckExists(young.ArchivePath))
	require.NoError(t, store.Restore(ctx, base, t.TempDir()))

	_, err = checkpoint.NewStore(t.TempDir()).Prune(ctx, now)
	require.ErrorIs(t, err, runner.ErrNoCheckpointCatalog)
}
