package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Uses P3 T15's native fake protocol: discovery reads login, rateLimits and the
// model cache; ProbeExecutedModel reads the rollout written by codex exec.
const rhCodexScript = `#!/bin/sh
case "$1" in
  --version) echo 'codex-cli 0.171.0'; exit 0 ;;
  login) echo 'Logged in using ChatGPT'; exit 0 ;;
  app-server) while IFS= read -r l; do case "$l" in
    *'"id":1'*) echo '{"id":1,"result":{"ordinaryUsageAllowed":true,"rateLimits":{"limitId":"codex","primary":{"usedPercent":42,"windowDurationMins":300,"resetsAt":1900000000}}}}' ;;
    *'"id":0'*) echo '{"id":0,"result":{}}' ;;
    esac; done; exit 0 ;;
esac
case "$*" in *' mcp list --json') echo '[]'; exit 0 ;; esac
[ "$1" = exec ] || exit 2
echo "$*" >> '@DIR@/codex-calls.log'
model=''; dir=''
while [ $# -gt 0 ]; do
  case "$1" in -m) model="$2"; shift ;; --cd) dir="$2"; shift ;; esac
  shift
done
[ "$model" = gpt-6.1-sol ] && [ -d "$dir" ] || exit 3
[ "$(cat)" = 'Reply with only your model identity on one line. Do not use tools or take any other action.' ] || exit 4
thread="confirm-$$"
d="$CODEX_HOME/sessions/2026/11/23"; mkdir -p "$d"
printf '%s\n%s\n' "{\"type\":\"session_meta\",\"payload\":{\"id\":\"$thread\",\"cwd\":\"$dir\"}}" "{\"type\":\"turn_context\",\"payload\":{\"turn_id\":\"t\",\"model\":\"$model\"}}" > "$d/rollout-2026-11-23T10-00-00-$thread.jsonl"
echo "{\"type\":\"thread.started\",\"thread_id\":\"$thread\"}"
echo '{"type":"item.completed","item":{"type":"agent_message","text":"OK"}}'
echo '{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}'
`

func rhStartDualNativeRunner(t *testing.T, st *rrStack) *rhRunner {
	t.Helper()
	plaintext, runnerID, _ := rrCreateKey(t, st, "mac-codex-confirm")
	home, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	fake := filepath.Join(home, "fake")
	cfgDir := filepath.Join(home, ".arbitron-runner")
	ws := filepath.Join(home, "workspaces")
	codexHome := filepath.Join(home, "codex-home")
	for _, dir := range []string{fake, cfgDir, ws, codexHome} {
		require.NoError(t, os.MkdirAll(dir, 0o700))
	}
	claude, codex := filepath.Join(fake, "claude"), filepath.Join(fake, "codex")
	require.NoError(t, os.WriteFile(claude, []byte(strings.ReplaceAll(rhRunnerClaudeScript, "@DIR@", fake)), 0o700))
	require.NoError(t, os.WriteFile(codex, []byte(strings.ReplaceAll(rhCodexScript, "@DIR@", fake)), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(codexHome, "models_cache.json"), []byte(`{"models":[{"slug":"gpt-6.1-sol"}]}`), 0o600))
	rwTouch(t, fake, "logged-in")
	cfgPath := filepath.Join(cfgDir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("workspace_root: "+yamlQuote(ws)+
		"\nheartbeat_seconds: 1\nprobe_interval_seconds: 3600\nclaude_binary: "+yamlQuote(claude)+
		"\ncodex_binary: "+yamlQuote(codex)+"\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "runner.env"), []byte("ARBITRON_SERVER_URL="+st.srv.URL+
		"\nARBITRON_RUNNER_KEY="+plaintext+"\n"), 0o600))
	log, err := os.Create(filepath.Join(home, "runner.log"))
	require.NoError(t, err)
	cmd := exec.Command(rhRunnerBinary(t), "run", "--config", cfgPath)
	cmd.Env = []string{"HOME=" + home, "CODEX_HOME=" + codexHome, "PATH=/usr/bin:/bin"}
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

func TestRemoteConfirmation_CodexSourceUsesCodexVerifier(t *testing.T) {
	st := rhBoot(t)
	ctx := adminCtxForLocalUser(t, st.ds)
	_, _, err := st.ds.profileSvc.Create(ctx, provider.Profile{Alias: "rh-codex", Kind: provider.ProfileKindAgent, Backend: "codex", Model: "gpt-6.1-sol"})
	require.NoError(t, err)
	r := rhStartDualNativeRunner(t, st)
	claudeBase := rhAwaitAdmissionVerification(t, st, r)
	sid := runner.MakeSourceID(runner.RunnerID(r.runnerID), runner.CredOpenAISubscription, "")
	require.Eventually(t, func() bool {
		s, err := st.ds.runners.registry.GetSource(ctx, sid)
		if err != nil || s.Health.Auth != runner.AuthOK {
			return false
		}
		for _, c := range s.Capabilities {
			if c.Op == runner.OpConfirm && c.Backend == "codex" && c.ExecutedModel {
				return true
			}
		}
		return false
	}, 30*time.Second, 50*time.Millisecond, "the real runner advertises its own native Codex verifier")
	before, err := os.ReadFile(filepath.Join(r.dir, "codex-calls.log"))
	if !os.IsNotExist(err) {
		require.NoError(t, err)
	}
	past := time.Now().Add(-time.Minute)
	require.NoError(t, st.ds.runners.monitor.ApplyOutcome(ctx, sid, runner.SourceOutcome{Class: runner.OutcomeExhausted, Model: "gpt-6.1-sol", ResetAt: &past}))
	require.Eventually(t, func() bool { return rwSourceHealth(t, st.ds, sid).Capacity == runner.CapacityAvailable },
		45*time.Second, 100*time.Millisecond, "native Codex evidence plus durable completion recovers exactly the Codex source")
	assert.Equal(t, claudeBase, rhCalls(r.dir, "--safe-mode"), "zero Claude verifier calls during Codex confirmation")
	assert.Equal(t, runner.CapacityAvailable, rwSourceHealth(t, st.ds, r.source()).Capacity, "the other credential stays healthy")
	calls, err := os.ReadFile(filepath.Join(r.dir, "codex-calls.log"))
	require.NoError(t, err)
	assert.Contains(t, string(calls), "exec --sandbox read-only -m gpt-6.1-sol", "the owning machine ran the restricted exact-model verifier")
	assert.Equal(t, strings.Count(string(before), "\n")+1, strings.Count(string(calls), "\n"), "confirmation launched exactly one new native Codex check after exhaustion")
	var source, assigned, state, result string
	require.NoError(t, st.ds.db.QueryRow(`SELECT source_id, runner_id, state, result_ref FROM runner_jobs WHERE operation_id='probe/confirm'`).Scan(&source, &assigned, &state, &result))
	assert.Equal(t, string(sid), source)
	assert.Equal(t, r.runnerID, assigned)
	assert.Equal(t, "completed", state)
	assert.Equal(t, "probe:gpt-6.1-sol", result, "the durable receipt contains the model actually executed")
	assert.NotEmpty(t, rrStrings(t, st.ds, `SELECT kind FROM pool_events WHERE kind='source.recovered' AND source_id=?`, string(sid)))
	active, err := st.ds.runners.ledger.ListActive(context.Background())
	require.NoError(t, err)
	assert.Empty(t, active)
}
