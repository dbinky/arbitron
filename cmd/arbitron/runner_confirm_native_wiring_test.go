package main

import (
	"encoding/json"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunnerWiring_ConfirmationUsesRestrictedNativeProbe(t *testing.T) {
	ds, fake := rwBoot(t, 1)
	ctx := adminCtxForLocalUser(t, ds)
	rwChatProfile(t, ctx, ds)
	script := `#!/bin/sh
case "$1" in
 --version) echo '2.1.330 (Claude Code)'; exit 0;;
 auth) echo '{"loggedIn":true}'; exit 0;;
esac
cat >/dev/null
echo "$*" >> '@DIR@/confirm.log'
echo "$PWD" >> '@DIR@/pwd.log'
echo '{"type":"result","is_error":false,"result":"OK","session_id":"s","modelUsage":{"claude-opus-4-1":{}}}'
`
	require.NoError(t, os.WriteFile(filepath.Join(fake, "claude"), []byte(strings.ReplaceAll(script, "@DIR@", fake)), 0o700))
	past := time.Now().Add(-time.Hour)
	require.NoError(t, ds.runners.monitor.ApplyOutcome(ctx, rwAnthropic, runner.SourceOutcome{Class: runner.OutcomeExhausted, Model: "claude-opus-4-1", ResetAt: &past}))
	require.NoError(t, ds.runners.monitor.Tick(ctx))
	ds.runners.monitor.Wait()
	assert.Equal(t, runner.CapacityAvailable, rwSourceHealth(t, ds, rwAnthropic).Capacity)
	argv, err := os.ReadFile(filepath.Join(fake, "confirm.log"))
	require.NoError(t, err)
	for _, want := range []string{"--model claude-opus-4-1", "--tools", "--strict-mcp-config", "--safe-mode"} {
		assert.Contains(t, string(argv), want)
	}
	pwd, err := os.ReadFile(filepath.Join(fake, "pwd.log"))
	require.NoError(t, err)
	dir := strings.TrimSpace(string(pwd))
	assert.Contains(t, dir, string(filepath.Separator)+".runner-confirm"+string(filepath.Separator)+"confirm-")
	_, err = os.Stat(dir)
	assert.True(t, os.IsNotExist(err))
	var op, raw string
	require.NoError(t, ds.db.QueryRow(`SELECT op, requirements FROM runner_jobs WHERE operation_id='probe/confirm'`).Scan(&op, &raw))
	assert.Equal(t, "confirm", op)
	var req runner.ExecRequirements
	require.NoError(t, json.Unmarshal([]byte(raw), &req))
	assert.Equal(t, runner.AffinityNone, req.Affinity)
	assert.Equal(t, runner.BuiltInRunnerID, req.PinnedRunner)
	assert.Equal(t, rwAnthropic, req.RequiredSource)
	jobs, err := ds.runners.ledger.ListActive(ctx)
	require.NoError(t, err)
	assert.Empty(t, jobs)
}
