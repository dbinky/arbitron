package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/runner"
)

// rdtRecent reads the actual REST detail, without test assertions in polling.
func rdtRecent(st *rrStack, id string) []map[string]any {
	r, ok := rrRunner(st.srv, id)
	if !ok {
		return nil
	}
	raw, _ := r["recentTelemetry"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func TestRunnerDetailCompositionRoot_RecentTelemetryFromTheHubWindow(t *testing.T) {
	st := bootRemoteRunnerStack(t, nil, false)
	require.Eventually(t, func() bool { return len(rdtRecent(st, "builtin")) >= 2 },
		20*time.Second, 50*time.Millisecond, "SINK: GET /api/v1/runners/builtin lists the built-in loop's recent samples")
	samples := rdtRecent(st, "builtin")
	var prev time.Time
	for i, s := range samples {
		at, ok := s["sampledAt"].(string)
		require.True(t, ok, "sample %d carries sampledAt", i)
		ts, err := time.Parse(time.RFC3339, at)
		require.NoError(t, err)
		assert.False(t, ts.Before(prev), "samples are oldest first")
		prev = ts
	}
	isErr, text := mcpToolCallerAs(t, st.ds, adminCtxForLocalUser(t, st.ds))("runner_get", map[string]any{"id": "builtin"})
	require.False(t, isErr, text)
	var detail struct {
		RecentTelemetry []map[string]any `json:"recentTelemetry"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &detail))
	assert.GreaterOrEqual(t, len(detail.RecentTelemetry), 2, "SINK: MCP runner_get shows the same window")
}

func TestRunnerDetailCompositionRoot_RecentTelemetryIsPerRunner(t *testing.T) {
	st := bootRemoteRunnerStack(t, nil, false)
	_, idle, _ := rrCreateKey(t, st, "mac-idle")
	r, ok := rrRunner(st.srv, idle)
	require.True(t, ok)
	assert.Equal(t, []any{}, r["recentTelemetry"], "a runner with no samples renders an empty list")
	plain, id, _ := rrCreateKey(t, st, "mac-tel")
	cores := 977
	startRemoteAgent(t, st, plain, st.workspaceDir, newRRExecutor(t, "unused", false), runner.Telemetry{Cores: &cores})
	rrAwaitLiveness(t, st, id, "online")
	require.Eventually(t, func() bool { return len(rdtRecent(st, id)) >= 2 },
		20*time.Second, 50*time.Millisecond, "SINK: the remote runner's heartbeats fill its own recentTelemetry")
	for _, s := range rdtRecent(st, id) {
		assert.EqualValues(t, 977, s["cores"], "the remote detail shows the remote machine's samples")
	}
	require.Eventually(t, func() bool { return len(rdtRecent(st, "builtin")) >= 2 },
		20*time.Second, 50*time.Millisecond, "the built-in runner has its own samples")
	for _, s := range rdtRecent(st, "builtin") {
		assert.NotEqualValues(t, 977, s["cores"], "the built-in window never shows another runner's samples")
	}
}
