package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A peer is killed after its shared tree swap but before the publication proof,
// so token T2 is visible on disk yet never committed. The retry's re-authorising
// reload runs inside the destination lock it already holds; there the repository
// skips its own read settlement, so the reload must settle explicitly (restoring
// T1) before the expected token is compared and the generation bumped.
func TestLayoutRetryE2E_KilledSharedWriterSettlesUnderRetryLock(t *testing.T) {
	e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
	ds, _ := e.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.workflowSvc.Create(ctx, "source", layoutE2EWorkflow("source", "Committed old graph")))
	require.NoError(t, ds.workflowSvc.Publish(ctx, "source", "target", workflow.VisibilityPublic, ""))
	oldRev := layoutE2ESavedRevision(t, e.sharedPath("target"))
	layoutE2EWaitTerminal(t, ds, oldRev)
	ds.layout.worker.Stop()
	before, err := ds.layout.store.Get(ctx, oldRev)
	require.NoError(t, err)
	oldBytes, err := os.ReadFile(e.sharedPath("target"))
	require.NoError(t, err)

	timeout, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(timeout, os.Args[0], "-test.run=^TestLayoutE2E_CrashProcess$")
	cmd.Env = append(os.Environ(), "ARBITRON_LAYOUT_CRASH_CONFIG="+e.configPath, "ARBITRON_LAYOUT_CRASH_STAGE=shared/before-proof")
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(e.root, "crash-ready")); return err == nil }, 10*time.Second, 10*time.Millisecond)
	data, err := os.ReadFile(filepath.Join(e.root, "captured.json"))
	require.NoError(t, err)
	var prepared workflow.ArrangementRequest
	require.NoError(t, json.Unmarshal(data, &prepared))
	require.NoError(t, cmd.Process.Kill())
	require.Error(t, cmd.Wait())
	require.NotEqual(t, oldRev, prepared.Revision)
	require.Equal(t, prepared.Revision, layoutE2ESavedRevision(t, e.sharedPath("target")), "the uncommitted T2 tree is visible before settlement")

	// The peer died after this retry's unlocked authorisation and before its lock
	// set was acquired: run the retry with that lock set already held and nothing
	// settled, exactly as RetryWorkflow's locked callback sees the destination.
	targets, err := ds.workflowSvc.LayoutRetryTargets(ctx, "target")
	require.NoError(t, err)
	require.NoError(t, ds.layout.mutations.WithMutations(ctx, targets, func(locked context.Context) error {
		_, err := ds.layout.query.RetryWorkflow(locked, "target", prepared.Revision)
		assert.ErrorIs(t, err, layout.ErrRevisionConflict, "a never-committed token must not be retried")
		return nil
	}))
	_, err = ds.layout.store.Get(ctx, prepared.Revision)
	require.ErrorIs(t, err, layout.ErrRecordNotFound, "no generation may exist for the invisible revision")
	restored, err := os.ReadFile(e.sharedPath("target"))
	require.NoError(t, err)
	require.Equal(t, oldBytes, restored, "settlement under the held lock restores T1")
	unchanged, err := ds.layout.store.Get(ctx, oldRev)
	require.NoError(t, err)
	assert.Equal(t, before.JobGeneration, unchanged.JobGeneration, "a refused retry bumps nothing")

	// After settlement the committed T1 is the only retryable token, over HTTP too.
	router := newLayoutE2ERouter(t, ds)
	status, body := layoutE2EDo(t, router, http.MethodPost, "/api/v1/workflows/target/layout/retry", `{"expectedRevision":"`+prepared.Revision+`"}`)
	require.Equal(t, http.StatusConflict, status, "%v", body)
	status, body = layoutE2EDo(t, router, http.MethodPost, "/api/v1/workflows/target/layout/retry", `{"expectedRevision":"`+oldRev+`"}`)
	require.Equal(t, http.StatusAccepted, status, "%v", body)
	assert.Equal(t, oldRev, body["savedRevision"])
	after, err := ds.layout.store.Get(ctx, oldRev)
	require.NoError(t, err)
	assert.Equal(t, before.JobGeneration+1, after.JobGeneration)
	_, err = ds.layout.store.Get(ctx, prepared.Revision)
	require.ErrorIs(t, err, layout.ErrRecordNotFound)
}
