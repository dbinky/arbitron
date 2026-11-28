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
        if [ -f '@DIR@/logged-in' ]; then echo ok >> '@DIR@/auth.log'; echo '{"loggedIn":true}'; exit 0; fi
        echo logged_out >> '@DIR@/auth.log'; echo '{"loggedIn":false}'; exit 1 ;;
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

// rhAuthAnswers counts the fake claude's `auth status` answers with this result.
func rhAuthAnswers(dir, result string) int {
	b, _ := os.ReadFile(filepath.Join(dir, "auth.log"))
	return strings.Count(string(b), result+"\n")
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
	// The server's periodic remote check runs once as soon as the source is
	// admitted, then every probeintervalseconds (5). Let that first check
	// finish (hello + periodic = two logged-in answers) so the cached-heartbeat
	// window below lies wholly between periodic checks.
	require.Eventually(t, func() bool { return rhAuthAnswers(r.dir, "ok") >= 2 }, 30*time.Second, 20*time.Millisecond,
		"the first periodic remote check completed while still logged in")
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

func rhCalls(dir, contains string) int {
	b, err := os.ReadFile(filepath.Join(dir, "calls.log"))
	if err != nil {
		return 0
	}
	n := 0
	for _, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, contains) {
			n++
		}
	}
	return n
}

// rhCallsWith counts calls.log lines containing every substring.
func rhCallsWith(dir string, subs ...string) int {
	b, _ := os.ReadFile(filepath.Join(dir, "calls.log"))
	n := 0
	for _, l := range strings.Split(string(b), "\n") {
		all := l != ""
		for _, s := range subs {
			all = all && strings.Contains(l, s)
		}
		if all {
			n++
		}
	}
	return n
}

// rhAwaitAdmissionVerification waits until the runner has natively verified
// both models the server's profiles request at admission (contracts D1:
// rh-opus and rh-sonnet), and returns the --safe-mode call count so far.
func rhAwaitAdmissionVerification(t *testing.T, st *rrStack, r *rhRunner) int {
	t.Helper()
	require.Eventually(t, func() bool {
		source, err := st.ds.runners.registry.GetSource(context.Background(), r.source())
		if err != nil {
			return false
		}
		// calls.log records starts. Only the advertised capability proves the
		// native result was verified and reached the server (contracts D1).
		verified := map[string]bool{}
		for _, capability := range source.Capabilities {
			if capability.Op == runner.OpChat && capability.ExecutedModel {
				for _, model := range capability.Models {
					verified[model] = true
				}
			}
		}
		return rhCallsWith(r.dir, "--safe-mode", "claude-opus-4-1") >= 1 && rhCallsWith(r.dir, "--safe-mode", "claude-sonnet-4-5") >= 1 &&
			verified["claude-opus-4-1"] && verified["claude-sonnet-4-5"]
	}, 30*time.Second, 50*time.Millisecond, "admission verification of the requested models")
	return rhCalls(r.dir, "--safe-mode")
}

func rhMaxConcurrent(dir string) int {
	b, _ := os.ReadFile(filepath.Join(dir, "events.log"))
	depth, peak := 0, 0
	for _, l := range strings.Fields(string(b)) {
		if l == "start" {
			depth++
		} else {
			depth--
		}
		if depth > peak {
			peak = depth
		}
	}
	return peak
}

func TestRemoteConfirmation_UnknownResetRecoversExactSourceAutomatically(t *testing.T) {
	st := rhBoot(t)
	r := rhStartRunner(t, st, "mac-quota")
	ctx := adminCtxForLocalUser(t, st.ds)
	sid := r.source()
	base := rhAwaitAdmissionVerification(t, st, r)
	baseSonnet := rhCallsWith(r.dir, "--safe-mode", "claude-sonnet")

	rwTouch(t, r.dir, "exhausted")
	first, _, err := st.ds.eng.Trigger(ctx, "rh-ask", map[string]any{}, "normal")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		h := rwSourceHealth(t, st.ds, sid)
		return h.Capacity == runner.CapacityExhausted && h.ResetAt == nil && h.ConfirmationModel == "claude-opus-4-1"
	}, 30*time.Second, 50*time.Millisecond, "a real workflow exhausted the remote source with an unknown reset")
	require.NoError(t, st.ds.eng.Cancel(ctx, first))
	require.NoError(t, os.Remove(filepath.Join(r.dir, "exhausted")))
	// The unknown reset is re-checked by the bounded native confirmation
	// (phase-1 design §3.11 "Exhausted with unknown reset"). A same-model ordinary
	// step that wins the single probation slot may also recover the source
	// (§3.11, §8 "A probation source returns to available after one successful step"),
	// so the ordinary workflow is queued only once the confirmation is placed.
	require.Eventually(t, func() bool { return rwCountJobs(st.ds.db, "operation_id='probe/confirm'") > 0 },
		45*time.Second, 100*time.Millisecond, "the monitor placed the native confirmation on its own")
	queued, _, err := st.ds.eng.Trigger(ctx, "rh-ask", map[string]any{}, "normal") // waits; no later user action
	require.NoError(t, err)

	require.Eventually(t, func() bool { return rwSourceHealth(t, st.ds, sid).Capacity == runner.CapacityAvailable },
		45*time.Second, 100*time.Millisecond, "probation + native confirmation return the source automatically")
	assert.GreaterOrEqual(t, rhCalls(r.dir, "--safe-mode")-base, 1, "the runner's own restricted native verifier ran")
	assert.Equal(t, baseSonnet, rhCallsWith(r.dir, "--safe-mode", "claude-sonnet"), "never a same-family substitute")

	var src, assigned, state, op string
	require.NoError(t, st.ds.db.QueryRow(`SELECT source_id, runner_id, state, op FROM runner_jobs WHERE operation_id='probe/confirm' ORDER BY created_at DESC LIMIT 1`).
		Scan(&src, &assigned, &state, &op))
	assert.Equal(t, string(sid), src, "confirmed on exactly the exhausted source")
	assert.Equal(t, r.runnerID, assigned)
	assert.Equal(t, "completed", state)
	assert.Equal(t, "confirm", op)
	rrRequireCompleted(t, st.ds, queued) // the ordinary queued workflow then executes
	jobs, err := st.ds.runners.ledger.ListActive(ctx)
	require.NoError(t, err)
	assert.Empty(t, jobs, "the probe job slot was released")
	assert.NotEmpty(t, rrStrings(t, st.ds, `SELECT kind FROM pool_events WHERE kind='source.recovered' AND source_id=?`, string(sid)))
}

func TestRemoteConfirmation_FullRunnerWaitsWithoutOversubscription(t *testing.T) {
	st := rhBoot(t) // step limit 1 on every runner (rrRunnersYAML)
	r := rhStartRunner(t, st, "mac-full")
	ctx := adminCtxForLocalUser(t, st.ds)
	sid := r.source()
	require.Eventually(t, func() bool { return rwSourceHealth(t, st.ds, sid).Auth == runner.AuthOK }, 30*time.Second, 50*time.Millisecond)
	base := rhAwaitAdmissionVerification(t, st, r)

	rwTouch(t, r.dir, "hold")
	held, _, err := st.ds.eng.Trigger(ctx, "rh-sonnet", map[string]any{}, "normal")
	require.NoError(t, err)
	require.Eventually(t, func() bool { // the held workflow step, not the admission verification
		return rhCalls(r.dir, "claude-sonnet-4-5")-rhCallsWith(r.dir, "--safe-mode", "claude-sonnet-4-5") == 1
	}, 30*time.Second, 50*time.Millisecond)
	past := time.Now().Add(-time.Minute)
	require.NoError(t, st.ds.runners.monitor.ApplyOutcome(ctx, sid, runner.SourceOutcome{Class: runner.OutcomeExhausted, Model: "claude-opus-4-1", ResetAt: &past}))
	time.Sleep(6 * time.Second) // more than one health cadence while the runner is full
	assert.Zero(t, rwCountJobs(st.ds.db, "operation_id='probe/confirm' AND state IN ('offered','running','completed')"), "no confirmation runs while the slot is held")
	assert.Equal(t, runner.CapacityProbation, rwSourceHealth(t, st.ds, sid).Capacity)

	require.NoError(t, os.Remove(filepath.Join(r.dir, "hold")))
	rrRequireCompleted(t, st.ds, held)
	require.Eventually(t, func() bool { return rwSourceHealth(t, st.ds, sid).Capacity == runner.CapacityAvailable }, 30*time.Second, 100*time.Millisecond)
	assert.Equal(t, 1, rhMaxConcurrent(r.dir), "the confirmation never ran beside the held step")
	assert.Equal(t, 1, rhCalls(r.dir, "--safe-mode")-base, "exactly one bounded confirmation after admission verification")
}
