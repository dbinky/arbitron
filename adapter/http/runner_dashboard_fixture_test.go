package http

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// This snapshot is consumed by dashboard decoder tests. Regenerate explicitly
// with UPDATE_RUNNER_DASHBOARD_FIXTURE=1 go test ./adapter/http -run TestRunnerDashboardFixture.
func TestRunnerDashboardFixture(t *testing.T) {
	at := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	run := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	invocation := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	id := uuid.MustParse("33333333-3333-4333-8333-333333333333")
	view := runner.RunnerView{Runner: runner.Runner{ID: runner.BuiltInRunnerID, Name: "server", BuiltIn: true, StepLimit: 8, Liveness: runner.LivenessOnline}, Sources: []runner.Source{{ID: "builtin/anthropic_subscription/-", Kind: runner.CredAnthropicSubscription, UnavailableReason: "harness unavailable"}}}
	pin := "builtin"
	trace := toNodeExecutionResponse(&instance.NodeExecution{ID: id, InstanceID: run, NodeID: "chat", Attempt: 1, Status: instance.NodeStatusSucceeded, StartedAt: at, SessionInvocationID: invocation, SessionPinRunner: &pin, SessionGeneration: 4, SessionMoves: []instance.SessionMoveRecord{{ID: id, RunID: run, InvocationID: invocation, NodeID: "chat", FromRunner: "mac-a", ToRunner: "builtin", Reason: "exhausted", Lossy: true, Notice: "tool details and reasoning may be lost", Generation: 4, At: at, RequestID: id, TurnIndex: 2}}})
	payload := map[string]any{"runner": toRunnerDetailResponse(runner.RunnerDetail{RunnerView: view}), "trace": []nodeExecutionResponse{trace}}
	data, err := json.MarshalIndent(payload, "", "  ")
	require.NoError(t, err)
	data = append(data, '\n')
	path := filepath.Join("..", "..", "dashboard", "src", "api", "fixtures", "runner-http.json")
	if os.Getenv("UPDATE_RUNNER_DASHBOARD_FIXTURE") == "1" {
		require.NoError(t, os.WriteFile(path, data, 0644))
	}
	saved, err := os.ReadFile(path)
	require.NoError(t, err)
	require.JSONEq(t, string(data), string(saved), "server wire DTO changed; regenerate and review dashboard contract")
}
