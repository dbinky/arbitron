package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
)

// Contract + chaos (design §4 step 7, §7, §8 Error): the runner's socket is
// killed mid-step. A connection drop alone is not loss; after LostAfter (6s)
// with no heartbeat the hub marks the runner lost, the step is re-placed with
// generation+1 on the built-in runner and completes. When the partitioned
// runner later reconnects holding its finished generation-1 result, the hub
// answers `fenced`, the runner drops it, and the step keeps its newer outcome.
// Without hub.Start (no liveness ticks) the runner is never marked lost and
// the step never re-places.
func TestRemoteRunnersCompositionRootLostRunnerReplacesStepAndFencesLateResult(t *testing.T) {
	st := bootRemoteRunnerStack(t, map[string]string{"rr-single": rrSingleManifest}, false)
	ctx := adminCtxForLocalUser(t, st.ds)

	plain, rid, _ := rrCreateKey(t, st, "mac-chaos")
	exec := newRRExecutor(t, "late-answer", true)
	conn, spool := startRemoteAgent(t, st, plain, st.workspaceDir, exec, runner.Telemetry{})
	rrAwaitLiveness(t, st, rid, "online")

	code, body := rrDo(t, http.MethodPost, st.srv.URL+"/api/v1/runners/builtin/cordon", nil)
	require.Less(t, code, 300, string(body))
	id, _, err := st.ds.eng.Trigger(ctx, "rr-single", map[string]any{}, "normal")
	require.NoError(t, err)
	d := rrAwaitDispatch(t, exec)
	code, body = rrDo(t, http.MethodPost, st.srv.URL+"/api/v1/runners/builtin/uncordon", nil)
	require.Less(t, code, 300, string(body))

	conn.cut() // kill the socket mid-step; redials are held
	rrAwaitLiveness(t, st, rid, "lost")
	rrRequireCompleted(t, st.ds, id)

	var gen int64
	var owner, state string
	require.NoError(t, st.ds.db.QueryRow(`SELECT generation, runner_id, state FROM runner_jobs WHERE id = ?`, d.JobID).Scan(&gen, &owner, &state))
	assert.Greater(t, gen, d.Generation, "the lost step is re-placed with a bumped generation")
	assert.Equal(t, string(runner.BuiltInRunnerID), owner)
	assert.Equal(t, "completed", state)
	assert.Equal(t, []string{rid},
		rrStrings(t, st.ds, `SELECT runner_id FROM runner_job_attempts WHERE job_id = ? AND generation = ?`, d.JobID, d.Generation),
		"the first generation was the remote runner's")

	// The partitioned runner finishes late and reconnects with its old result.
	exec.releaseAll()
	ref := runneragent.JobRef{JobID: d.JobID, Generation: d.Generation}
	require.Eventually(t, func() bool {
		j, ok, err := spool.Get(context.Background(), ref)
		return err == nil && ok && j.Result != nil
	}, 10*time.Second, 25*time.Millisecond, "the late result is retained for replay")
	conn.allow()
	require.Eventually(t, func() bool { return conn.received(runneragent.KindFenced, d.JobID, d.Generation) },
		30*time.Second, 50*time.Millisecond, "the hub fences the stale generation")
	require.Eventually(t, func() bool {
		_, ok, err := spool.Get(context.Background(), ref)
		return err == nil && !ok
	}, 10*time.Second, 25*time.Millisecond, "the fenced runner drops the job")

	// The step keeps its newer outcome.
	require.NoError(t, st.ds.db.QueryRow(`SELECT generation, runner_id, state FROM runner_jobs WHERE id = ?`, d.JobID).Scan(&gen, &owner, &state))
	assert.Equal(t, string(runner.BuiltInRunnerID), owner)
	assert.Equal(t, "completed", state)
	outs := rrStrings(t, st.ds, `SELECT COALESCE(output, '') FROM node_executions WHERE instance_id = ? AND node_id = 'ask'`, id.String())
	require.Len(t, outs, 1, "exactly one committed execution")
	assert.Contains(t, outs[0], "builtin-answer")
	assert.False(t, strings.Contains(outs[0], "late-answer"), "the stale result never reaches the run")
	rrAwaitLiveness(t, st, rid, "online") // the reconnected runner is usable again
}
