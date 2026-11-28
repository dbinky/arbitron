package main

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/runneragent/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildAgent_ConfiguredCodexAlsoConfirms(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin") // never fall back to a developer's Codex
	srv, done := stubNativeHub(t, true)
	d := newNativeRunnerDir(t, srv.URL, "")
	b, err := os.ReadFile(d.codexBin)
	require.NoError(t, err)
	// Native confirmation gets its evidence from the model actually executed,
	// not the provider's request label. Parse the configured binary's arguments
	// and write the rollout the real Codex adapter reads.
	const execScript = `
case "$*" in *' mcp list --json') echo '[]'; exit 0 ;; esac
[ "$1" = exec ] || exit 2
model=''; dir=''
while [ $# -gt 0 ]; do
  case "$1" in -m) model="$2"; shift ;; --cd) dir="$2"; shift ;; esac
  shift
done
[ "$model" = gpt-6.1-sol ] && [ -d "$dir" ] || exit 3
cat >/dev/null
thread="confirm-$$"
d="$CODEX_HOME/sessions/2026/11/23"; mkdir -p "$d"
printf '%s\n%s\n' "{\"type\":\"session_meta\",\"payload\":{\"id\":\"$thread\",\"cwd\":\"$dir\"}}" "{\"type\":\"turn_context\",\"payload\":{\"turn_id\":\"t\",\"model\":\"$model\"}}" > "$d/rollout-2026-11-23T10-00-00-$thread.jsonl"
echo "{\"type\":\"thread.started\",\"thread_id\":\"$thread\"}"
echo '{"type":"item.completed","item":{"type":"agent_message","text":"OK"}}'
echo '{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}'
`
	require.NoError(t, os.WriteFile(d.codexBin, []byte(strings.TrimSuffix(string(b), "exit 2\n")+execScript), 0o700))
	cfg, err := config.Load(d.cfgPath)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	agent, err := buildAgent(ctx, cfg, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	stopped := make(chan error, 1)
	go func() { stopped <- agent.Run(ctx) }()
	defer func() { cancel(); <-stopped }()
	select {
	case got := <-done:
		require.Empty(t, got.rejected)
		require.True(t, got.result.OK, got.result.ErrorText)
		require.NotNil(t, got.result.Confirmation)
		assert.Equal(t, "gpt-6.1-sol", got.result.Confirmation.RequestedModel)
		assert.Equal(t, "gpt-6.1-sol", got.result.Confirmation.ExecutedModel)
		assert.NotEmpty(t, got.result.Confirmation.SessionID)
	case <-ctx.Done():
		t.Fatal("no native confirmation result reached the server")
	}
	b, err = os.ReadFile(d.codexLog)
	require.NoError(t, err)
	assert.Contains(t, string(b), d.codexBin+" exec --sandbox read-only -m gpt-6.1-sol", "the advertised verifier uses the same configured binary and a restricted probe")
}
