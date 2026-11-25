package main

import (
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/runneragent/wsclient"
	"github.com/arbitron/arbitron/domain/runner"
)

// A healthy built-in needs no runner setup: with zero keys the production
// stack serves the key surfaces and the connect endpoint, and a chat step
// runs on the built-in runner. Unwiring RouterDeps.RunnerKeys (404),
// RouterDeps.RunnerConnect (404 instead of 401) or MCP Deps.RunnerKeys
// (tool unavailable) fails this test.
func TestRemoteRunnersCompositionRootZeroKeysRunsLocally(t *testing.T) {
	st := bootRemoteRunnerStack(t, map[string]string{"rr-single": rrSingleManifest}, false)
	ctx := adminCtxForLocalUser(t, st.ds)

	var keys int
	require.NoError(t, st.ds.db.QueryRow(`SELECT COUNT(*) FROM runner_keys`).Scan(&keys))
	require.Zero(t, keys)
	code, body := rrDo(t, http.MethodGet, st.srv.URL+"/api/v1/runners/keys", nil)
	assert.Equal(t, http.StatusOK, code, string(body))

	resp, err := http.Get(st.srv.URL + wsclient.ConnectPath)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "the connect endpoint is mounted and refuses a keyless request")

	mcp := newGroupMCPServer(st.ds)
	res := callMCPTool(t, mcp, ctx, "runner_key_list", map[string]any{})
	assert.False(t, res.IsError, "MCP runner_key_list is wired: %s", mcpText(t, res))

	id, _, err := st.ds.eng.Trigger(ctx, "rr-single", map[string]any{}, "normal")
	require.NoError(t, err)
	rrRequireCompleted(t, st.ds, id)
	assert.Equal(t, []string{string(runner.BuiltInRunnerID)},
		rrStrings(t, st.ds, `SELECT DISTINCT runner_id FROM runner_jobs WHERE run_id = ?`, id.String()))
	assert.Equal(t, []string{string(runner.BuiltInRunnerID)},
		rrStrings(t, st.ds, `SELECT runner_id FROM node_executions WHERE instance_id = ? AND node_id = 'ask'`, id.String()))
}

// Happy path (design §8): a 3-branch chat fan-out with step limit 1 on every
// runner spreads across the built-in runner and two remote runners connected
// over a real WebSocket. mac-a advertises the server's execution root
// (compatible); mac-b advertises its own root (chat_only) and still runs
// hermetic chat. Keeping Phase 1's StaticRunnerSet, dropping RunnerConnect or
// passing an empty ServerRoot fails this test.
func TestRemoteRunnersCompositionRootSpreadsChatFanOutAcrossBuiltInAndRemotes(t *testing.T) {
	st := bootRemoteRunnerStack(t, map[string]string{"rr-fanout": rrFanoutManifest}, true)
	ctx := adminCtxForLocalUser(t, st.ds)

	plainA, idA, _ := rrCreateKey(t, st, "mac-a")
	plainB, idB, _ := rrCreateKey(t, st, "mac-b")
	execA := newRRExecutor(t, "remote-answer-a", true)
	execB := newRRExecutor(t, "remote-answer-b", true)
	startRemoteAgent(t, st, plainA, st.workspaceDir, execA, runner.Telemetry{})
	startRemoteAgent(t, st, plainB, t.TempDir(), execB, runner.Telemetry{})
	rrAwaitLiveness(t, st, idA, "online")
	rrAwaitLiveness(t, st, idB, "online")
	assert.Equal(t, "compatible", rrCompat(st.srv, idA), "same absolute root as wsMgr.BasePath()")
	assert.Equal(t, "chat_only", rrCompat(st.srv, idB), "a different root is hermetic chat only")

	id, _, err := st.ds.eng.Trigger(ctx, "rr-fanout", map[string]any{}, "normal")
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		r := rrRunningByRunner(st.ds)
		return r[string(runner.BuiltInRunnerID)] == 1 && r[idA] == 1 && r[idB] == 1
	}, 30*time.Second, 50*time.Millisecond, "three concurrent steps must occupy built-in, mac-a and mac-b (got %v)", rrRunningByRunner(st.ds))

	for _, e := range []*rrExecutor{execA, execB} {
		d := rrAwaitDispatch(t, e)
		require.NotNil(t, d.Chat)
		assert.Equal(t, "claude-sonnet-4-5", d.Chat.Model, "the pinned profile model is dispatched")
		assert.Contains(t, d.Chat.Prompt+d.Chat.SystemPrompt, rrTemplateMarker, "the rendered template is dispatched")
		assert.GreaterOrEqual(t, d.Generation, int64(1))
	}

	require.NoError(t, os.WriteFile(st.goFile, nil, 0o600))
	execA.releaseAll()
	execB.releaseAll()
	rrRequireCompleted(t, st.ds, id)

	assert.ElementsMatch(t, []string{string(runner.BuiltInRunnerID), idA, idB},
		rrStrings(t, st.ds, `SELECT DISTINCT runner_id FROM runner_jobs WHERE state = 'completed'`))
	rows, err := st.ds.db.Query(`SELECT runner_id, COALESCE(output, '') FROM node_executions WHERE node_id IN ('a1','b1','c1') AND status = 'succeeded'`)
	require.NoError(t, err)
	byRunner := map[string]string{}
	for rows.Next() {
		var rid, out string
		require.NoError(t, rows.Scan(&rid, &out))
		byRunner[rid] = out
	}
	require.NoError(t, rows.Close())
	require.Len(t, byRunner, 3, "each branch ran on a different runner: %v", byRunner)
	assert.Contains(t, byRunner[idA], "remote-answer-a")
	assert.Contains(t, byRunner[idB], "remote-answer-b")
	assert.Contains(t, byRunner[string(runner.BuiltInRunnerID)], "builtin-answer")
	assert.Equal(t, 1, execA.peak(), "mac-a never exceeds its step limit")
	assert.Equal(t, 1, execB.peak(), "mac-b never exceeds its step limit")
}

// Design §8 Failure: revoking a key disconnects the runner (KeyService →
// Revoker = hub) and its running chat step is re-placed and completes on the
// built-in runner with a newer generation. Without Revoker = hub the session
// stays open and the step never leaves the remote runner.
func TestRemoteRunnersCompositionRootRevokedKeyDisconnectsAndReplacesStep(t *testing.T) {
	st := bootRemoteRunnerStack(t, map[string]string{"rr-single": rrSingleManifest}, false)
	ctx := adminCtxForLocalUser(t, st.ds)

	plain, rid, keyID := rrCreateKey(t, st, "mac-r")
	exec := newRRExecutor(t, "revoked-answer", true)
	startRemoteAgent(t, st, plain, st.workspaceDir, exec, runner.Telemetry{})
	rrAwaitLiveness(t, st, rid, "online")

	code, body := rrDo(t, http.MethodPost, st.srv.URL+"/api/v1/runners/builtin/cordon", nil)
	require.Less(t, code, 300, string(body))
	id, _, err := st.ds.eng.Trigger(ctx, "rr-single", map[string]any{}, "normal")
	require.NoError(t, err)
	d := rrAwaitDispatch(t, exec)
	code, body = rrDo(t, http.MethodPost, st.srv.URL+"/api/v1/runners/builtin/uncordon", nil)
	require.Less(t, code, 300, string(body))

	code, body = rrDo(t, http.MethodDelete, st.srv.URL+"/api/v1/runners/keys/"+keyID, map[string]any{"acknowledge": true})
	require.Equal(t, http.StatusOK, code, string(body))
	rrAwaitLiveness(t, st, rid, "lost")

	rrRequireCompleted(t, st.ds, id)
	var gen int64
	var owner, state string
	require.NoError(t, st.ds.db.QueryRow(`SELECT generation, runner_id, state FROM runner_jobs WHERE id = ?`, d.JobID).Scan(&gen, &owner, &state))
	assert.Greater(t, gen, d.Generation, "re-placement bumps the generation")
	assert.Equal(t, string(runner.BuiltInRunnerID), owner)
	assert.Equal(t, "completed", state)
	outs := rrStrings(t, st.ds, `SELECT COALESCE(output, '') FROM node_executions WHERE instance_id = ? AND node_id = 'ask'`, id.String())
	require.Len(t, outs, 1)
	assert.Contains(t, outs[0], "builtin-answer")
	assert.False(t, strings.Contains(outs[0], "revoked-answer"))
	assert.Never(t, func() bool { return rrLiveness(st.srv, rid) == "online" }, 2*time.Second, 100*time.Millisecond,
		"a revoked key cannot bring the runner back online")
}
