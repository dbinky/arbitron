package engine

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/arbitron/arbitron/domain/workflow"
)

func TestIsNodeTimeout(t *testing.T) {
	live := context.Background()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancelExpired := context.WithTimeout(context.Background(), 0)
	defer cancelExpired()
	<-expired.Done()

	script := workflow.Node{ID: "s", Type: workflow.NodeTypeScript, Config: workflow.NodeConfig{TimeoutSeconds: 60}}
	agent := workflow.Node{ID: "a", Type: workflow.NodeTypeAgentCall, Config: workflow.NodeConfig{TimeoutSeconds: 60}}
	deadline := fmt.Errorf("x: %w", context.DeadlineExceeded)
	// The agent providers wrap ctx.Err() as "<provider>: mission timed out: %w".
	agentDeadline := fmt.Errorf("agent_call %q: run agent: %w", "a",
		fmt.Errorf("claudecli: mission timed out: %w (%v)", context.DeadlineExceeded, errors.New("signal: killed")))

	cases := []struct {
		name string
		ctx  context.Context
		node workflow.Node
		err  error
		want bool
	}{
		{"script deadline", live, script, deadline, true},
		{"agent mission deadline", live, agent, agentDeadline, true},
		{"nil error", live, script, nil, false},
		{"plain error", live, script, errors.New("boom"), false},
		{"cancellation", live, script, fmt.Errorf("x: %w", context.Canceled), false},
		{"parent cancelled (pause/cancel)", cancelled, script, deadline, false},
		{"parent deadline expired", expired, script, deadline, false},
		{"no node timeout", live, workflow.Node{Type: workflow.NodeTypeScript}, deadline, false},
		{"llm_call ignores timeoutSeconds", live, workflow.Node{Type: workflow.NodeTypeLLMCall, Config: workflow.NodeConfig{TimeoutSeconds: 60}}, deadline, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isNodeTimeout(tc.ctx, tc.node, tc.err))
		})
	}
}

func TestNextTimeoutSeconds(t *testing.T) {
	cases := []struct {
		name                       string
		current, configured, scale int
		want                       int
	}{
		{"default doubles", 300, 300, 0, 600},
		{"doubles again", 600, 300, 0, 1200},
		{"capped at 4x", 1200, 300, 0, 1200},
		{"scale 1 unchanged", 300, 300, 1, 300},
		{"scale 3 then cap", 300, 100, 3, 400},
		{"negative scale treated as off", 300, 300, -2, 300},
		{"absolute ceiling", 20000, 20000, 0, maxScaledTimeoutSeconds},
		{"ceiling never shrinks a large configured timeout", 30000, 30000, 0, 30000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, nextTimeoutSeconds(tc.current, tc.configured, tc.scale))
		})
	}
}
