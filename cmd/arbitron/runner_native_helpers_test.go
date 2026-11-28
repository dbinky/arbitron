package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

var (
	runnerBinOnce sync.Once
	runnerBin     string
	runnerBinErr  error
)

// arbitronRunnerBinary builds the real runner composition root once per test binary.
func arbitronRunnerBinary(t *testing.T) string {
	t.Helper()
	runnerBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "arbitron-runner-bin-")
		if err != nil {
			runnerBinErr = err
			return
		}
		runnerBin = filepath.Join(dir, "arbitron-runner")
		out, err := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-ldflags", "-X main.version="+rrReleasedVersion, "-o", runnerBin, "github.com/arbitron/arbitron/cmd/arbitron-runner").CombinedOutput()
		if err != nil {
			runnerBinErr = err
			runnerBin = string(out)
		}
	})
	require.NoError(t, runnerBinErr, runnerBin)
	return runnerBin
}

// nativeCodexScript: version/login/rateLimits, and `exec` that runs in --cd,
// writes a file, and records a rollout naming the model it "ran". A prompt
// tagged P3-WRONG-MODEL makes codex run gpt-fallback instead.
const nativeCodexScript = `#!/bin/sh
echo "$0 $*" >> '@LOG@'
case "$1" in
  --version) echo 'codex-cli 0.171.0'; exit 0 ;;
  login) echo 'Logged in using ChatGPT'; exit 0 ;;
  app-server)
    while IFS= read -r l; do case "$l" in
      *'"id":1'*) echo '{"id":1,"result":{"ordinaryUsageAllowed":true,"rateLimits":{"limitId":"codex","primary":{"usedPercent":42,"windowDurationMins":300,"resetsAt":1900000000}}}}' ;;
      *'"id":0'*) echo '{"id":0,"result":{}}' ;;
    esac; done; exit 0 ;;
  exec) ;;
  *) exit 2 ;;
esac
model=''; dir=''
while [ $# -gt 0 ]; do case "$1" in -m) model="$2"; shift ;; --cd) dir="$2"; shift ;; esac; shift; done
prompt=$(cat)
cd "$dir" || exit 3
ran="$model"
case "$prompt" in *P3-WRONG-MODEL*) ran='gpt-fallback' ;; esac
case "$prompt" in *P3-HOLD*) touch '@GATE@/codex.started'; n=0; while [ ! -f '@GATE@/codex.release' ]; do n=$((n+1)); [ "$n" -lt 2400 ] || exit 91; sleep .05; done ;; esac
printf 'codex ran %s\n' "$ran" > codex-output.txt
thread="th-$$-$(date +%s)"
d="$CODEX_HOME/sessions/2026/11/23"; mkdir -p "$d"
printf '%s\n%s\n' "{\"type\":\"session_meta\",\"payload\":{\"id\":\"$thread\",\"cwd\":\"$dir\"}}" "{\"type\":\"turn_context\",\"payload\":{\"turn_id\":\"t\",\"model\":\"$ran\"}}" > "$d/rollout-2026-11-23T10-00-00-$thread.jsonl"
echo "{\"type\":\"thread.started\",\"thread_id\":\"$thread\"}"
echo '{"type":"turn.started"}'
echo '{"type":"item.completed","item":{"type":"agent_message","text":"codex done"}}'
echo '{"type":"turn.completed","usage":{"input_tokens":2,"output_tokens":1}}'
`

// nativeClaudeScript: version/auth, chat and agent turns; logs argv and the
// axis env, and reports the --model it was given as executed.
const nativeClaudeScript = `#!/bin/sh
echo "$0 $* | effort=${CLAUDE_CODE_EFFORT_LEVEL:-} window=${CLAUDE_CODE_AUTO_COMPACT_WINDOW:-}" >> '@LOG@'
case "$1" in
  --version) echo '@VERSION@ (Claude Code)'; exit 0 ;;
  auth) echo '{"loggedIn":true,"authMethod":"claude.ai"}'; exit 0 ;;
esac
cat > /dev/null
m=$(echo "$*" | sed -n 's/.*--model \([^ ]*\).*/\1/p')
echo 'claude was here' > claude-output.txt 2>/dev/null || true
printf '{"type":"result","is_error":false,"result":"claude done","session_id":"s","usage":{"input_tokens":1,"output_tokens":1},"modelUsage":{"%s":{}}}\n' "$m"
`

type nativeRunnerOpts struct {
	name          string
	codex, claude bool   // install the fake harness
	claudeVersion string // default 2.1.400
	extraHeaders  string // ARBITRON_EXTRA_HEADERS
}

type nativeRunner struct {
	id, dir, codexHome, codexLog, claudeLog, binDir, codexBin, claudeBin, gate string
	cmd                                                                        *exec.Cmd
}

// startNativeRunner writes a real runner config dir and starts the real binary.
func startNativeRunner(t *testing.T, st *p3Stack, o nativeRunnerOpts) *nativeRunner {
	t.Helper()
	bin := arbitronRunnerBinary(t)
	plaintext, runnerID, _ := rrCreateKey(t, st.rr(), o.name)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	r := &nativeRunner{id: runnerID, dir: dir, codexHome: filepath.Join(dir, "codex-home"), codexLog: filepath.Join(dir, "codex.log"),
		claudeLog: filepath.Join(dir, "claude.log"), binDir: filepath.Join(dir, "bin"), gate: filepath.Join(dir, "gate")}
	for _, d := range []string{r.codexHome, r.binDir, r.gate} {
		require.NoError(t, os.MkdirAll(d, 0o700))
	}
	require.NoError(t, os.WriteFile(filepath.Join(r.codexHome, "models_cache.json"), []byte(`{"models":[{"slug":"gpt-6.1-sol"}]}`), 0o600))
	r.codexBin, r.claudeBin = filepath.Join(r.binDir, "codex"), filepath.Join(r.binDir, "claude")
	if o.codex {
		r.installCodex(t)
	}
	if o.claude {
		r.installClaude(t, o.claudeVersion)
	}
	env := "ARBITRON_SERVER_URL=" + st.srv.URL + "\nARBITRON_RUNNER_KEY=" + plaintext + "\n"
	if o.extraHeaders != "" {
		env += "ARBITRON_EXTRA_HEADERS=" + o.extraHeaders + "\n"
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "runner.env"), []byte(env), 0o600))
	cfg := "workspace_root: \"" + st.workspaceDir + "\"\nheartbeat_seconds: 1\nprobe_interval_seconds: 5\n" +
		"claude_binary: \"" + r.claudeBin + "\"\ncodex_binary: \"" + r.codexBin + "\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o600))

	ctx, cancel := context.WithCancel(context.Background())
	r.cmd = exec.CommandContext(ctx, bin, "run", "--config", filepath.Join(dir, "config.yaml"))
	r.cmd.Env = []string{"HOME=" + dir, "CODEX_HOME=" + r.codexHome, "PATH=" + r.binDir + ":/usr/bin:/bin"}
	r.cmd.Stdout, r.cmd.Stderr = os.Stderr, os.Stderr
	require.NoError(t, r.cmd.Start())
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(r.gate, "codex.release"), nil, 0o600)
		cancel()
		_ = r.cmd.Wait()
	})
	// An intentionally old harness must be visible as incompatible before the
	// upgrade test proceeds; it cannot be online until its version is upgraded.
	require.Eventually(t, func() bool {
		return len(rrStrings(t, st.ds, `SELECT id FROM runners WHERE id = ? AND (liveness = 'online' OR compat_state = 'incompatible')`, runnerID)) == 1
	}, 30*time.Second, 50*time.Millisecond, "the real runner hello reached the registry")
	return r
}

func (r *nativeRunner) installCodex(t *testing.T) {
	s := strings.NewReplacer("@LOG@", r.codexLog, "@GATE@", r.gate).Replace(nativeCodexScript)
	require.NoError(t, os.WriteFile(r.codexBin, []byte(s), 0o700))
}

func (r *nativeRunner) installClaude(t *testing.T, version string) {
	if version == "" {
		version = "2.1.400"
	}
	s := strings.NewReplacer("@LOG@", r.claudeLog, "@VERSION@", version).Replace(nativeClaudeScript)
	require.NoError(t, os.WriteFile(r.claudeBin, []byte(s), 0o700))
}

func readLog(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return ""
	}
	require.NoError(t, err)
	return string(b)
}

func uuidMust(t *testing.T, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	require.NoError(t, err)
	return id
}
