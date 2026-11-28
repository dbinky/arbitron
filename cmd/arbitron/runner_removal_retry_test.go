package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
)

// The production notifier writes the tombstone while the shared group/step
// lease is held. Neither an empty inventory nor a test-only retrier can satisfy
// the SQL and immutable-file sinks below.
func p3PendingRemoval(t *testing.T, ds *domainStack) (uuid.UUID, engine.ResourceFenceRef) {
	t.Helper()
	ctx := context.Background()
	id := uuid.New()
	src := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(src, "kept.txt"), []byte("committed bytes"), 0o600))
	_, _, err := ds.mobility.Checkpoints.Commit(ctx, id, src)
	require.NoError(t, err)
	_, err = ds.mobility.Local.StageLocal(ctx, id, src, 1)
	require.NoError(t, err)
	claim, err := ds.mobility.Leases.Claim(ctx, "run:"+id.String(), runner.GroupHolder(id, "group gather"), runner.BuiltInRunnerID)
	require.NoError(t, err)
	ds.mobility.Removal.WorkspaceRemoved(ctx, id)
	p3AssertRemovalPending(t, ds, id)
	assert.DirExists(t, filepath.Join(ds.cfg.Runners.CheckpointDir, "local", id.String()))
	return id, claim
}

func p3AssertRemovalPending(t *testing.T, ds *domainStack, id uuid.UUID) {
	t.Helper()
	assert.Equal(t, []string{"delete_pending"}, rrStrings(t, ds,
		`SELECT state FROM runner_copies WHERE run_id = ? AND runner_id = 'builtin'`, id.String()))
	assert.Equal(t, []string{"committed"}, rrStrings(t, ds,
		`SELECT state FROM workspace_checkpoints WHERE run_id = ?`, id.String()))
	assert.DirExists(t, filepath.Join(ds.cfg.Runners.CheckpointDir, "runs", id.String()))
}

func p3RemovalFinished(ds *domainStack, id uuid.UUID) bool {
	var rows int
	err := ds.db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM workspace_checkpoints WHERE run_id = ?) +
		(SELECT COUNT(*) FROM runner_copies WHERE run_id = ? AND runner_id = 'builtin' AND state != 'deleted') +
		(SELECT COUNT(*) FROM workspace_leases WHERE resource_key = ? AND active = 1)`,
		id.String(), id.String(), "run:"+id.String()).Scan(&rows)
	_, portableErr := os.Stat(filepath.Join(ds.cfg.Runners.CheckpointDir, "runs", id.String()))
	_, localErr := os.Stat(filepath.Join(ds.cfg.Runners.CheckpointDir, "local", id.String()))
	return err == nil && rows == 0 && os.IsNotExist(portableErr) && os.IsNotExist(localErr)
}

func TestBuildWorkspaceMobility_DeferredRemovalRetriesOnDrainTick(t *testing.T) {
	st := bootP3Stack(t, nil, "")
	ctx := context.Background()
	id, claim := p3PendingRemoval(t, st.ds)

	require.NoError(t, st.ds.mobility.Drain.Tick(ctx))
	p3AssertRemovalPending(t, st.ds, id)
	assert.DirExists(t, filepath.Join(st.ds.cfg.Runners.CheckpointDir, "local", id.String()))
	require.NoError(t, st.ds.mobility.Leases.Check(ctx, claim), "the retry must not release another writer's claim")
	require.NoError(t, st.ds.mobility.Leases.Release(ctx, claim))
	require.NoError(t, st.ds.mobility.Drain.Tick(ctx))
	assert.True(t, p3RemovalFinished(st.ds, id), "SINK: a drain tick deletes both stores and clears the durable server tombstone after the writer releases")
}

func TestBuildWorkspaceMobility_DeferredRemovalSurvivesDeleteErrorAndPoolWake(t *testing.T) {
	st := bootP3Stack(t, nil, "")
	ctx := context.Background()
	id, claim := p3PendingRemoval(t, st.ds)
	_, err := st.ds.db.Exec(`CREATE TRIGGER p3_fail_removal BEFORE DELETE ON workspace_checkpoints
		BEGIN SELECT RAISE(ABORT, 'checkpoint disk unavailable'); END`)
	require.NoError(t, err)
	require.NoError(t, st.ds.mobility.Leases.Release(ctx, claim))
	err = st.ds.mobility.Drain.Tick(ctx)
	require.ErrorContains(t, err, "checkpoint disk unavailable")
	p3AssertRemovalPending(t, st.ds, id)
	assert.NoDirExists(t, filepath.Join(st.ds.cfg.Runners.CheckpointDir, "local", id.String()),
		"joined deletion still attempts the independent local store when catalog deletion fails")

	_, err = st.ds.db.Exec(`DROP TRIGGER p3_fail_removal`)
	require.NoError(t, err)
	// Start's real drain loop consumes the existing pool notifier. Do not call
	// RetryDeferred (or another test-only maintenance loop) to finish cleanup.
	st.ds.runners.notifier.Notify()
	require.Eventually(t, func() bool { return p3RemovalFinished(st.ds, id) }, 5*time.Second, 20*time.Millisecond,
		"SINK: the serving drain loop retries owed cleanup after an earlier deletion error")
}

func TestBuildWorkspaceMobility_DeferredRemovalRetriesAtStartup(t *testing.T) {
	st := bootP3Stack(t, nil, "")
	id, claim := p3PendingRemoval(t, st.ds)
	st.ds.Close(context.Background())
	ds, err := initDomainWithModelDependencies(context.Background(), st.configPath, ModelDependencies{
		FetchTransport: catalogHTTPLeaf(func(*http.Request) (*http.Response, error) { return nil, errOfflineCatalog }),
	})
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close(context.Background()) })
	p3AssertRemovalPending(t, ds, id)
	require.NoError(t, ds.mobility.Leases.Release(context.Background(), claim))

	require.NoError(t, ds.Start(context.Background()))
	assert.True(t, p3RemovalFinished(ds, id), "SINK: startup finishes persisted server cleanup without waiting for a drain tick or a new workspace-removal event")
}
