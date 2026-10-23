package claudecli

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The usability probe is accepted only when it travels the launch path a real
// run uses. This pins every difference between the probe's `claude` launch and
// a pinned RunAgent's on the same binding: the empty tool allowlist without
// --dangerously-skip-permissions replaces the agent's tool scope and skip flag
// plus the source-verified tool/MCP/customization/update restrictions. Binary,
// --model, credentials, capability rules, cwd, stdin and response cap stay identical.
func TestUsabilityClaudeProbeLaunchMatchesPinnedRunExceptToolRestriction(t *testing.T) {
	const workDir = "/tmp/probe-workspace/.model-probe"
	cfg := func(r CommandRunner) Config {
		return Config{
			BinaryPath: "claude", Runner: r, WorkspaceRoot: "/tmp/probe-workspace", MaxResponseBytes: 1 << 20,
			AllowedTools: []string{"Bash", "Write"}, SkipPermissions: true,
		}
	}
	usage := `{"` + proofModel + `":{}}`
	line := `{"type":"result","is_error":false,"result":"ok","session_id":"s","modelUsage":` + usage + `}`

	realRunner := &fakeRunner{lines: []string{line}}
	_, err := NewAgentAdapterForBinding(cfg(realRunner), probeBinding(proofModel)).RunAgent(context.Background(),
		engine.AgentMission{Model: proofModel, Prompt: "probe prompt", WorkDir: workDir})
	require.NoError(t, err)
	probeRunner := &fakeRunner{lines: []string{line}}
	got, _, err := NewAgentAdapterForBinding(cfg(probeRunner), probeBinding(proofModel)).ProbeExecutedModel(context.Background(), "probe prompt", workDir)
	require.NoError(t, err)
	assert.Equal(t, proofModel, got)

	real, probe := realRunner.gotSpec, probeRunner.gotSpec
	require.Contains(t, real.Args, "--dangerously-skip-permissions")
	tools, _ := argValue(real.Args, "--allowedTools")
	require.Equal(t, "Bash,Write", tools)
	assert.NotContains(t, probe.Args, "--dangerously-skip-permissions")
	probeTools, found := argValue(probe.Args, "--allowedTools")
	assert.True(t, found)
	assert.Empty(t, probeTools, "the probe runs with the empty tool allowlist")

	wantArgs := append(restrictTools(real.Args), "--tools", "", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--safe-mode", "--setting-sources", "", "--no-chrome")
	assert.Equal(t, wantArgs, probe.Args, "only the documented probe recipe may differ")
	assert.Equal(t, real.Bin, probe.Bin)
	wantEnv := append(real.Env, "DISABLE_UPDATES=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "CLAUDE_CODE_DISABLE_OFFICIAL_MARKETPLACE_AUTOINSTALL=1")
	assert.Equal(t, wantEnv, probe.Env)
	assert.Equal(t, real.Dir, probe.Dir)
	assert.Equal(t, real.Stdin, probe.Stdin)
	assert.Equal(t, real.MaxResponseBytes, probe.MaxResponseBytes)
}

// restrictTools rewrites a real agent argv to its tool-restricted form: no
// --dangerously-skip-permissions and an empty --allowedTools value.
func restrictTools(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dangerously-skip-permissions":
		case "--allowedTools":
			out = append(out, args[i], "")
			i++
		default:
			out = append(out, args[i])
		}
	}
	return out
}
