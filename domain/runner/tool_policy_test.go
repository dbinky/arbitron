package runner_test

import (
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestToolScopeSatisfied(t *testing.T) {
	allow := runner.ExecutionCapability{Op: runner.OpAgent, Backend: "claude-code", Tools: []string{"Read", "Grep"}, ToolPolicy: runner.ToolPolicyAllowlist}
	open := runner.ExecutionCapability{Op: runner.OpAgent, Backend: "codex", ToolPolicy: runner.ToolPolicyUnrestricted}
	chat := runner.ExecutionCapability{Op: runner.OpChat, Tools: []string{"Read"}}
	claude := runner.ExecutionCapability{Op: runner.OpAgent, Backend: "claude-code", ToolPolicy: runner.ToolPolicyAllowlist, Tools: []string{"Bash", "Edit", "Glob", "Grep", "LS", "Read", "WebFetch", "WebSearch", "Write"}}
	for _, tc := range []struct {
		name  string
		c     runner.ExecutionCapability
		scope []string
		want  bool
	}{
		{"allowlist subset", allow, []string{"Read"}, true},
		{"allowlist full list in any order", allow, []string{"Grep", "Read"}, true},
		{"allowlist unadvertised tool", allow, []string{"Read", "Bash"}, false},
		{"allowlist empty scope is unresolved, never all tools", allow, nil, false},
		{"allowlist with an empty advertised list", runner.ExecutionCapability{Op: runner.OpAgent, ToolPolicy: runner.ToolPolicyAllowlist}, []string{"Read"}, false},
		{"unrestricted empty scope", open, nil, true},
		{"unrestricted never accepts a restrictive scope", open, []string{"Read"}, false},
		{"unset policy keeps the subset rule", chat, []string{"Read"}, true},
		{"unset policy empty request", chat, nil, true},
		{"unset policy unadvertised tool", chat, []string{"Bash"}, false},
		{"unknown policy", runner.ExecutionCapability{Op: runner.OpAgent, Tools: []string{"Read"}, ToolPolicy: "sometimes"}, []string{"Read"}, false},
		{"allowlist pattern-scoped bash (reviewer profile)", claude, []string{"Read", "Grep", "Glob", "Bash(gh:*)", "Bash(git:*)"}, true},
		{"allowlist pattern needs the base tool advertised", allow, []string{"Read", "Bash(gh:*)"}, false},
		{"allowlist empty pattern is malformed", claude, []string{"Bash()"}, false},
		{"allowlist unbalanced pattern is malformed", claude, []string{"Bash(gh:*"}, false},
		{"allowlist pattern on an unknown base", claude, []string{"Teleport(x)"}, false},
		{"unset policy never expands patterns", runner.ExecutionCapability{Op: runner.OpChat, Tools: []string{"Bash"}}, []string{"Bash(gh:*)"}, false},
		{"unrestricted still refuses a pattern scope", open, []string{"Bash(gh:*)"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) { assert.Equal(t, tc.want, runner.ToolScopeSatisfied(tc.c, tc.scope)) })
	}
}

func TestPlace_RemoteToolPolicyGatesTools(t *testing.T) {
	runners := []runner.Runner{online("mac-mini")}
	req := capReq()
	c := exactCap(req)
	c.ToolPolicy = runner.ToolPolicyUnrestricted
	assert.True(t, runner.Place(req, snap(runners, []runner.Source{remoteWith(c)}), runner.PlacementPolicy{}).Placed)
	req.Tools = []string{"Read"}
	assert.False(t, runner.Place(req, snap(runners, []runner.Source{remoteWith(c)}), runner.PlacementPolicy{}).Placed)
	c.ToolPolicy, c.Tools = runner.ToolPolicyAllowlist, []string{"Read"}
	assert.True(t, runner.Place(req, snap(runners, []runner.Source{remoteWith(c)}), runner.PlacementPolicy{}).Placed)
	req.Tools = nil
	assert.False(t, runner.Place(req, snap(runners, []runner.Source{remoteWith(c)}), runner.PlacementPolicy{}).Placed)
}
