package main

import (
	"context"
	"encoding/json"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rrRestart(t *testing.T, st *rrStack) {
	t.Helper()
	st.srv.Close()
	st.ds.Close(context.Background())
	ds, err := initStartedDomain(context.Background(), st.configPath)
	require.NoError(t, err)
	listener, err := net.Listen("tcp", st.addr)
	require.NoError(t, err)
	srv := httptest.NewUnstartedServer(adapthttp.NewRouterFromDeps(ds.httpDeps()))
	srv.Listener = listener
	srv.Start()
	st.ds, st.srv = ds, srv
	t.Cleanup(func() { srv.Close(); ds.Close(context.Background()) })
}

func TestRemoteRunnersCompositionRoot_DurableAckThenRestartRetainsResult(t *testing.T) {
	st := bootRemoteRunnerStack(t, map[string]string{"rr-single": rrSingleManifest}, false)
	ctx := adminCtxForLocalUser(t, st.ds)
	plain, rid, _ := rrCreateKey(t, st, "durable")
	exec := newRRExecutor(t, "remote-answer", false)
	_, spool := startRemoteAgent(t, st, plain, st.workspaceDir, exec, runner.Telemetry{})
	rrAwaitLiveness(t, st, rid, "online")
	code, body := rrDo(t, http.MethodPost, st.srv.URL+"/api/v1/runners/builtin/cordon", nil)
	require.Less(t, code, 300, string(body))
	id, _, err := st.ds.eng.Trigger(ctx, "rr-single", map[string]any{}, "normal")
	require.NoError(t, err)
	d := rrAwaitDispatch(t, exec)
	require.Eventually(t, func() bool {
		pending, err := spool.Pending(ctx)
		receipt, found, receiptErr := adaptsqlite.NewRemoteInboxRepo(st.ds.db).Receipt(ctx, runner.Fence{JobID: uuid.MustParse(d.JobID), Generation: d.Generation, RunnerID: runner.RunnerID(rid)})
		return err == nil && len(pending) == 0 && receiptErr == nil && found && receipt.Result != nil
	}, 20*time.Second, 20*time.Millisecond, "result was acknowledged")
	rrRestart(t, st)
	rrRequireCompleted(t, st.ds, id)
	receipt, found, err := adaptsqlite.NewRemoteInboxRepo(st.ds.db).Receipt(ctx, runner.Fence{JobID: uuid.MustParse(d.JobID), Generation: d.Generation, RunnerID: runner.RunnerID(rid)})
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, receipt.Result)
	assert.Equal(t, "remote-answer", receipt.Result.Chat.Output)
	assert.Equal(t, 1, exec.count())
	outs := rrStrings(t, st.ds, `SELECT COALESCE(output,'') FROM node_executions WHERE instance_id = ? AND node_id = 'ask'`, id.String())
	require.Len(t, outs, 1)
	assert.Contains(t, outs[0], "remote-answer")
}

func TestRemoteRunnersCompositionRoot_UnsupportedRunnerVersionGetsNoWork(t *testing.T) {
	st := bootRemoteRunnerStack(t, map[string]string{"rr-single": rrSingleManifest}, false)
	ctx := adminCtxForLocalUser(t, st.ds)
	plain, rid, _ := rrCreateKey(t, st, "future")
	exec := newRRExecutor(t, "must-not-run", false)
	startRemoteAgentVersion(t, st, plain, st.workspaceDir, exec, runner.Telemetry{}, "0.3.0")
	require.Eventually(t, func() bool { return rrCompat(st.srv, rid) == "incompatible" }, 20*time.Second, 20*time.Millisecond)
	row, ok := rrRunner(st.srv, rid)
	require.True(t, ok)
	compat := row["compat"].(map[string]any)
	assert.Contains(t, compat["reason"], "runner version")
	id, _, err := st.ds.eng.Trigger(ctx, "rr-single", map[string]any{}, "normal")
	require.NoError(t, err)
	rrRequireCompleted(t, st.ds, id)
	assert.Zero(t, exec.count())
	assert.Equal(t, []string{string(runner.BuiltInRunnerID)}, rrStrings(t, st.ds, `SELECT DISTINCT runner_id FROM runner_jobs WHERE run_id = ?`, id.String()))
}

func TestRemoteRunnersCompositionRoot_ExactRouteAndCapabilityGate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		model  string
		effort provider.Effort
		window provider.ContextWindow
		remote bool
	}{
		{name: "canonical", model: rrModel, remote: true},
		{name: "effort", model: rrModel, effort: provider.EffortMax},
		{name: "context", model: rrModel, window: provider.Context1M},
		{name: "model", model: "claude-opus-4-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := strings.ReplaceAll(rrSingleManifest, "default-chat", "rr-route")
			st := bootRemoteRunnerStack(t, map[string]string{"rr-single": manifest}, false)
			ctx := adminCtxForLocalUser(t, st.ds)
			_, _, err := st.ds.profileSvc.Create(ctx, provider.Profile{Alias: "rr-route", Kind: provider.ProfileKindChat, Model: tc.model, Effort: tc.effort, ContextWindow: tc.window})
			require.NoError(t, err)
			plain, rid, _ := rrCreateKey(t, st, "route")
			exec := newRRExecutor(t, "remote-answer", false)
			startRemoteAgent(t, st, plain, st.workspaceDir, exec, runner.Telemetry{})
			rrAwaitLiveness(t, st, rid, "online")
			code, body := rrDo(t, http.MethodPost, st.srv.URL+"/api/v1/runners/builtin/cordon", nil)
			require.Less(t, code, 300, string(body))
			t.Cleanup(func() { rrDo(t, http.MethodPost, st.srv.URL+"/api/v1/runners/builtin/uncordon", nil) })
			id, _, err := st.ds.eng.Trigger(ctx, "rr-single", map[string]any{}, "normal")
			require.NoError(t, err)
			if !tc.remote {
				require.Eventually(t, func() bool {
					var n int
					err := st.ds.db.QueryRow(`SELECT COUNT(*) FROM runner_jobs WHERE run_id = ? AND state = 'queued' AND wait IS NOT NULL`, id.String()).Scan(&n)
					return err == nil && n == 1
				}, 10*time.Second, 20*time.Millisecond, "unsupported route exposes a capacity wait")
				assert.Zero(t, exec.count())
				code, body := rrDo(t, http.MethodPost, st.srv.URL+"/api/v1/runners/builtin/uncordon", nil)
				require.Less(t, code, 300, string(body))
			}
			rrRequireCompleted(t, st.ds, id)
			if tc.remote {
				d := rrAwaitDispatch(t, exec)
				require.NotNil(t, d.Chat)
				assert.Equal(t, "claude-cli", d.Chat.Provider)
				assert.Equal(t, rrFP, d.Chat.EndpointFingerprint)
				raw, err := json.Marshal(d)
				require.NoError(t, err)
				for _, secret := range []string{"ANTHROPIC_API_KEY", "apiKey", "connectionKey", plain} {
					assert.NotContains(t, string(raw), secret)
				}
			} else {
				assert.Zero(t, exec.count())
				assert.Equal(t, []string{string(runner.BuiltInRunnerID)}, rrStrings(t, st.ds, `SELECT DISTINCT runner_id FROM runner_jobs WHERE run_id = ?`, id.String()))
			}
		})
	}
}

func TestRemoteRunnersCompositionRoot_ServerConnectionAndNonHostedRouteNeverGoRemote(t *testing.T) {
	st := bootRemoteRunnerStack(t, nil, false)
	ctx := adminCtxForLocalUser(t, st.ds)
	t.Run("non_hosted_lunaroute", func(t *testing.T) {
		err := st.ds.connSvc.Create(ctx, provider.Connection{Name: "non-hosted", Provider: "lunaroute", BaseURL: "https://lunaroute.example.test/v1"}, "must-not-travel")
		require.Error(t, err, "non-hosted route is visibly refused before placement")
	})
	t.Run("server_connection_named_like_remote_source", func(t *testing.T) {
		plain, rid, _ := rrCreateKey(t, st, "generic")
		exec := newRRExecutor(t, "must-not-run", false)
		startRemoteAgent(t, st, plain, st.workspaceDir, exec, runner.Telemetry{})
		rrAwaitLiveness(t, st, rid, "online")
		const secret = "server-connection-key-must-not-travel"
		require.NoError(t, st.ds.connSvc.Create(ctx, provider.Connection{Name: "anthropic_subscription", Provider: "anthropic", BaseURL: "https://api.anthropic.com"}, secret))
		_, _, err := st.ds.profileSvc.Create(ctx, provider.Profile{Alias: "generic", Kind: provider.ProfileKindChat, Model: rrModel, ConnectionRef: "anthropic_subscription"})
		require.NoError(t, err)
		b, err := st.ds.profileResolver.Resolve(ctx, "generic")
		require.NoError(t, err)
		p, err := st.ds.runners.chats.ChatProviderFor(b)
		require.NoError(t, err)
		code, body := rrDo(t, http.MethodPost, st.srv.URL+"/api/v1/runners/builtin/cordon", nil)
		require.Less(t, code, 300, string(body))
		run := uuid.New()
		stepCtx, cancel := context.WithCancel(engine.ContextWithStepIdentity(ctx, engine.StepIdentity{RunID: run, NodeID: "ask", InvocationID: uuid.New(), OperationID: "ask", Kind: engine.OperationNode, Attempt: 1}))
		done := make(chan error, 1)
		go func() {
			_, err := p.Chat(stepCtx, engine.ChatRequest{Model: b.Model, Prompt: "hello", WorkDir: st.workspaceDir, SuppressTools: true})
			done <- err
		}()
		t.Cleanup(func() { cancel(); <-done })
		require.Eventually(t, func() bool {
			var n int
			err := st.ds.db.QueryRow(`SELECT COUNT(*) FROM runner_jobs WHERE run_id = ? AND state = 'queued' AND wait IS NOT NULL`, run.String()).Scan(&n)
			return err == nil && n == 1
		}, 10*time.Second, 20*time.Millisecond)
		var kind string
		require.NoError(t, st.ds.db.QueryRow(`SELECT group_kind FROM runner_jobs WHERE run_id = ?`, run.String()).Scan(&kind))
		assert.Equal(t, string(runner.CredServerConnection), kind)
		assert.Zero(t, exec.count(), "a real configured generic connection never dispatches to the same-named remote source")
	})
}

type rrStreamExecutor struct {
	*rrExecutor
}

func (e rrStreamExecutor) Supports(d runneragent.Dispatch) (bool, string) {
	return d.Op == runneragent.OpStream && d.Chat != nil, "stream chat required"
}
func (e rrStreamExecutor) Execute(ctx context.Context, d runneragent.Dispatch, emit runneragent.EmitFunc) runneragent.Result {
	e.started <- d
	for _, token := range []string{"one", "two", "three"} {
		if err := emit(ctx, token); err != nil {
			return runneragent.Result{ErrorText: err.Error()}
		}
	}
	select {
	case <-e.release:
	case <-ctx.Done():
		return runneragent.Result{ErrorText: ctx.Err().Error()}
	}
	for _, token := range []string{"four", "five"} {
		if err := emit(ctx, token); err != nil {
			return runneragent.Result{ErrorText: err.Error()}
		}
	}
	return runneragent.Result{OK: true, Chat: &runneragent.ChatResult{Output: "onetwothreefourfive", Model: d.Chat.Model, SessionID: "rr-stream"}}
}
func TestRemoteRunnersCompositionRoot_OutputCursorSurvivesRestart(t *testing.T) {
	manifest := `{"name":"rr-stream","inputs":[],"nodes":[{"id":"talk","type":"interactive_agent","entrypoint":true,"config":{"model_profile":"rr-stream","orchestrationFile":"templates/o.md","templateFile":"templates/p.md"}}],"edges":[]}`
	st := bootRemoteRunnerStack(t, map[string]string{"rr-stream": manifest}, false)
	ctx := adminCtxForLocalUser(t, st.ds)
	require.NoError(t, os.WriteFile(filepath.Join(st.workspaceDir, "users", localDefaultUser.String(), "rr-stream", "templates", "o.md"), []byte("orchestrate"), 0o600))
	_, _, err := st.ds.profileSvc.Create(ctx, provider.Profile{Alias: "rr-stream", Kind: provider.ProfileKindAgent, Backend: "claude-code", Model: rrModel})
	require.NoError(t, err)
	plain, rid, _ := rrCreateKey(t, st, "stream")
	exec := rrStreamExecutor{newRRExecutor(t, "", true)}
	_, spool := startRemoteAgent(t, st, plain, st.workspaceDir, exec, runner.Telemetry{})
	rrAwaitLiveness(t, st, rid, "online")
	code, body := rrDo(t, http.MethodPost, st.srv.URL+"/api/v1/runners/builtin/cordon", nil)
	require.Less(t, code, 300, string(body))
	id, _, err := st.ds.eng.Trigger(ctx, "rr-stream", map[string]any{}, "normal")
	require.NoError(t, err)
	t.Cleanup(func() {
		rrDo(t, http.MethodPost, st.srv.URL+"/api/v1/runners/builtin/uncordon", nil)
		_ = st.ds.eng.Cancel(context.Background(), id)
	})
	d := rrAwaitDispatch(t, exec.rrExecutor)
	ref := runneragent.JobRef{JobID: d.JobID, Generation: d.Generation}
	require.Eventually(t, func() bool { job, ok, err := spool.Get(ctx, ref); return err == nil && ok && job.LastAcked >= 3 }, 20*time.Second, 20*time.Millisecond)
	rrRestart(t, st)
	require.Eventually(t, func() bool { job, ok, err := spool.Get(ctx, ref); return err == nil && ok && job.LastAcked >= 3 }, 20*time.Second, 20*time.Millisecond)
	exec.releaseAll()
	require.Eventually(t, func() bool {
		outs := rrStrings(t, st.ds, `SELECT COALESCE(output,'') FROM node_executions WHERE instance_id = ? AND node_id = 'talk'`, id.String())
		return len(outs) == 1 && strings.Contains(outs[0], "onetwothreefourfive")
	}, 20*time.Second, 20*time.Millisecond, "persisted output has all five tokens exactly once")
}

var _ runneragent.JobExecutor = (*rrExecutor)(nil)
