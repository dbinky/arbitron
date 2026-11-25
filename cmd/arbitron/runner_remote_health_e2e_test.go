package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/notification"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
)

const rhRunnersYAML = rrRunnersYAML + "  probeintervalseconds: 5\n  probejitterseconds: 0\n"

// rhBoot is bootRemoteRunnerStack (P2 T10) with a 5-second health cadence so
// unknown-reset probation is reached in real time on the started root.
func rhBoot(t *testing.T) *rrStack {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("remote execution requires a darwin runner")
	}
	pinSinkRootEnv(t)
	rrPinReleasedVersion(t)
	root := t.TempDir()
	ws := filepath.Join(root, "workspaces")
	require.NoError(t, os.MkdirAll(ws, 0o700))
	writeWorkflow(t, ws, "rh-ask", `{"name": "rh-ask", "inputs": [], "nodes": [
    {"id": "ask", "type": "llm_call", "entrypoint": true, "config": {"model_profile": "rh-opus", "templateFile": "templates/p.md"}}], "edges": []}`)
	writePinTemplate(t, ws, "rh-ask")
	writeWorkflow(t, ws, "rh-sonnet", `{"name": "rh-sonnet", "inputs": [], "nodes": [
    {"id": "ask", "type": "llm_call", "entrypoint": true, "config": {"model_profile": "rh-sonnet", "templateFile": "templates/p.md"}}], "edges": []}`)
	writePinTemplate(t, ws, "rh-sonnet")
	goFile := filepath.Join(root, "builtin-go")
	require.NoError(t, os.WriteFile(goFile, nil, 0o600))
	bin := rrFakeClaude(t, root, goFile)
	cfgPath := filepath.Join(root, "config.yaml")
	cfg := "server:\n  host: \"127.0.0.1\"\n  port: 0\nauth:\n  mode: \"local\"\n" +
		"database:\n  path: " + yamlQuote(filepath.Join(root, "arbitron.db")) + "\n" +
		"workspace:\n  path: " + yamlQuote(ws) + "\n" +
		"samples:\n  seed: false\n  sourcedir: \"\"\n" +
		"logging:\n  level: \"error\"\n  file: " + yamlQuote(filepath.Join(root, "arbitron.log")) + "\n" +
		"queue:\n  globalcap: 5\n  perusercap: 5\n" +
		"llm:\n  binarypath: " + yamlQuote(bin) + "\n" + rhRunnersYAML
	require.NoError(t, os.WriteFile(cfgPath, []byte(cfg), 0o600))
	ds, err := initStartedDomain(context.Background(), cfgPath)
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close(context.Background()) })
	st := &rrStack{ds: ds, srv: gsServeAPI(t, ds), workspaceDir: ws, goFile: goFile}
	ctx := adminCtxForLocalUser(t, ds)
	for alias, model := range map[string]string{"rh-opus": "claude-opus-4-1", "rh-sonnet": "claude-sonnet-4-5"} {
		_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: alias, Kind: provider.ProfileKindChat, Model: model})
		require.NoError(t, err)
	}
	code, body := rrDo(t, http.MethodPost, st.srv.URL+"/api/v1/runners/builtin/cordon", nil)
	require.Equal(t, http.StatusNoContent, code, string(body)) // work and confirmations must use the remote source
	return st
}

var (
	rhBinOnce sync.Once
	rhBin     string
	rhBinErr  error
)

func rhRunnerBinary(t *testing.T) string {
	t.Helper()
	rhBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "arbitron-runner-e2e-")
		if err != nil {
			rhBinErr = err
			return
		}
		rhBin = filepath.Join(dir, "arbitron-runner")
		if out, err := exec.Command("go", "build", "-ldflags", "-X main.version="+rrReleasedVersion, "-o", rhBin, "github.com/arbitron/arbitron/cmd/arbitron-runner").CombinedOutput(); err != nil {
			rhBinErr = fmt.Errorf("%v: %s", err, out)
		}
	})
	require.NoError(t, rhBinErr)
	return rhBin
}

// The RUNNER machine's claude: logged in only while logged-in exists; quota
// rejected (unknown reset) while exhausted exists; holds while hold exists;
// logs every model call with start/end markers for concurrency checks.
const rhRunnerClaudeScript = `#!/bin/sh
case "$1" in
  --version) echo "2.1.330 (Claude Code)"; exit 0 ;;
  auth) while [ -f '@DIR@/hang-auth' ]; do sleep 0.05; done
        if [ -f '@DIR@/logged-in' ]; then echo '{"loggedIn":true}'; exit 0; fi
        echo '{"loggedIn":false}'; exit 1 ;;
esac
cat > /dev/null
echo "$*" >> '@DIR@/calls.log'
echo start >> '@DIR@/events.log'
m=$(echo "$*" | sed -n 's/.*--model \([^ ]*\).*/\1/p')
if [ -f '@DIR@/exhausted' ]; then
  echo end >> '@DIR@/events.log'
  echo '{"type":"assistant","error":"rate_limit","apiErrorStatus":429,"quotaLimits":{"status":"rejected","rateLimitType":"seven_day"},"message":{"content":[]}}'
  echo '{"type":"result","is_error":true,"result":"usage limit reached","session_id":"rs"}'
  exit 1
fi
while [ -f '@DIR@/hold' ]; do sleep 0.05; done
echo end >> '@DIR@/events.log'
echo '{"type":"result","is_error":false,"result":"runner-answer","session_id":"rs","usage":{"input_tokens":1,"output_tokens":1},"modelUsage":{"'"$m"'":{}}}'
`

type rhRunner struct {
	dir, runnerID string
	cmd           *exec.Cmd
	done          chan error
}

func rhStartRunner(t *testing.T, st *rrStack, name string) *rhRunner {
	t.Helper()
	plaintext, runnerID, _ := rrCreateKey(t, st, name)
	home, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	fake := filepath.Join(home, "fake")
	require.NoError(t, os.MkdirAll(fake, 0o700))
	claude := filepath.Join(fake, "claude")
	require.NoError(t, os.WriteFile(claude, []byte(strings.ReplaceAll(rhRunnerClaudeScript, "@DIR@", fake)), 0o700))
	rwTouch(t, fake, "logged-in")
	cfgDir := filepath.Join(home, ".arbitron-runner")
	ws := filepath.Join(home, "workspaces")
	require.NoError(t, os.MkdirAll(ws, 0o700))
	require.NoError(t, os.MkdirAll(cfgDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte("workspace_root: "+yamlQuote(ws)+
		"\nheartbeat_seconds: 1\nprobe_interval_seconds: 3600\nclaude_binary: "+yamlQuote(claude)+"\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "runner.env"), []byte("ARBITRON_SERVER_URL="+st.srv.URL+
		"\nARBITRON_RUNNER_KEY="+plaintext+"\n"), 0o600))
	log, err := os.Create(filepath.Join(home, "runner.log"))
	require.NoError(t, err)
	cmd := exec.Command(rhRunnerBinary(t), "run", "--config", filepath.Join(cfgDir, "config.yaml"))
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
	cmd.Stdout, cmd.Stderr = log, log
	require.NoError(t, cmd.Start())
	r := &rhRunner{dir: fake, runnerID: runnerID, cmd: cmd, done: make(chan error, 1)}
	go func() { r.done <- cmd.Wait(); _ = log.Close() }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-r.done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-r.done
		}
	})
	rrAwaitLiveness(t, st, runnerID, "online")
	return r
}

func (r *rhRunner) source() runner.SourceID {
	return runner.MakeSourceID(runner.RunnerID(r.runnerID), runner.CredAnthropicSubscription, "")
}

func rhProbeREST(t *testing.T, st *rrStack, sid runner.SourceID) (int, map[string]any) {
	t.Helper()
	code, body := rrDo(t, http.MethodPost, st.srv.URL+"/api/v1/runners/sources/"+url.PathEscape(string(sid))+"/probe", nil)
	var out map[string]any
	_ = json.Unmarshal(body, &out)
	return code, out
}

func TestRemoteProbe_OperatorReadsOwningRunnerNow(t *testing.T) {
	st := rhBoot(t)
	r := rhStartRunner(t, st, "mac-probe")
	ctx := adminCtxForLocalUser(t, st.ds)
	sid := r.source()
	require.Eventually(t, func() bool { return rwSourceHealth(t, st.ds, sid).Auth == runner.AuthOK }, 30*time.Second, 50*time.Millisecond,
		"the hello observation recorded the runner's own login")
	before := rwSourceHealth(t, st.ds, sid)
	require.NotNil(t, before.LastProbeAt)
	builtinAuth := rwSourceHealth(t, st.ds, rwAnthropic).Auth

	require.NoError(t, os.Remove(filepath.Join(r.dir, "logged-in"))) // logged out on the runner, no heartbeat reports it
	time.Sleep(1500 * time.Millisecond)
	assert.Equal(t, runner.AuthOK, rwSourceHealth(t, st.ds, sid).Auth, "heartbeats re-send the old sample; cached data is not a fresh check")

	code, rest := rhProbeREST(t, st, sid)
	require.Equal(t, http.StatusOK, code, "%v", rest)
	assert.Equal(t, "logged_out", rest["auth"])
	h1 := rwSourceHealth(t, st.ds, sid)
	assert.Equal(t, runner.AuthLoggedOut, h1.Auth)
	require.NotNil(t, h1.LastProbeAt)
	assert.True(t, h1.LastProbeAt.After(*before.LastProbeAt))

	time.Sleep(1100 * time.Millisecond)
	isErr, text := mcpToolCallerAs(t, st.ds, ctx)("runner_source_probe", map[string]any{"sourceId": string(sid)})
	require.False(t, isErr, text)
	assert.Contains(t, text, "logged_out")
	h2 := rwSourceHealth(t, st.ds, sid)
	assert.True(t, h2.LastProbeAt.After(*h1.LastProbeAt), "the MCP check is a second fresh check")

	var notes int
	require.NoError(t, st.ds.db.QueryRow(`SELECT COUNT(*) FROM notifications WHERE type = ?`, string(notification.TypeSourceLoggedOut)).Scan(&notes))
	assert.Equal(t, 1, notes, "one durable notification for one logout")
	assert.Equal(t, builtinAuth, rwSourceHealth(t, st.ds, rwAnthropic).Auth, "the server's own (logged-in) credential is never consulted")
}

func TestRemoteHealth_MissingRootWiringFailsSink(t *testing.T) {
	st := rhBoot(t)
	ctx := adminCtxForLocalUser(t, st.ds)
	_, runnerID, _ := rrCreateKey(t, st, "mac-offline")
	sid := runner.MakeSourceID(runner.RunnerID(runnerID), runner.CredAnthropicSubscription, "")
	require.NoError(t, st.ds.runners.registry.ReplaceSources(ctx, runner.RunnerID(runnerID), []runner.Source{{ID: sid, RunnerID: runner.RunnerID(runnerID), Kind: runner.CredAnthropicSubscription}}))
	_, err := st.ds.runners.monitor.ProbeNow(ctx, sid)
	require.ErrorIs(t, err, runner.ErrRemoteProbeUnavailable, "an offline runner is unavailable, never cached success")
	assert.NotErrorIs(t, err, runner.ErrRemoteProberNotWired, "the root installed the transport prober")

	code, body := rhProbeREST(t, st, sid)
	assert.NotEqual(t, http.StatusOK, code, "the operator sink reports the failure: %v", body)

	_, err = wireRemoteRunners(remoteRunnerInputs{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Health", "a missing monitor (and so the remote prober) fails production initialization")
	// Mutation checks (contracts §20), run once after GREEN and recorded in the
	// verification note: dropping SetRemoteProber in wireRemoteRunners, dropping
	// observationToDomain/wireObservation translation, or the builtin
	// Confirmations wiring each makes TestRemoteProbe_OperatorReadsOwningRunnerNow /
	// TestRunnerWiring_ConfirmationUsesRestrictedNativeProbe fail.
}

func TestRemoteHealthRoot_ShutdownJoinsPendingChecks(t *testing.T) {
	st := rhBoot(t)
	r := rhStartRunner(t, st, "mac-hang")
	sid := r.source()
	require.Eventually(t, func() bool { return rwSourceHealth(t, st.ds, sid).Auth == runner.AuthOK }, 30*time.Second, 50*time.Millisecond)
	rwTouch(t, r.dir, "hang-auth")
	codeCh := make(chan int, 1)
	go func() { code, _ := rhProbeREST(t, st, sid); codeCh <- code }()
	time.Sleep(4 * time.Second) // beyond suspectafterseconds (3)
	assert.Equal(t, "online", rrLiveness(st.srv, r.runnerID), "a slow probe never makes a healthy runner suspect")
	closed := make(chan struct{})
	go func() { st.ds.Close(context.Background()); close(closed) }()
	select {
	case <-closed:
	case <-time.After(20 * time.Second):
		t.Fatal("root shutdown did not join the pending remote check")
	}
	select {
	case code := <-codeCh:
		assert.NotEqual(t, http.StatusOK, code)
	case <-time.After(5 * time.Second):
		t.Fatal("the operator call never returned")
	}
	assert.Zero(t, st.ds.remote.Hub.PendingProbes())
}
