package acceptance

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/instance"
)

// TestAcceptance_01_LocalClaudeRun_NoKeys proves spec §8 #1: the worked-example
// pipeline runs locally on a Claude subscription with NO API keys — `llm_call`
// via tool-suppressed claude -p and `agent_call` via claude -p with tools — and
// the loop TERMINATES through the real conditional on `assessor.done`.
//
// Requires (Phase 1): the tool-suppressed claude-cli ChatAdapter (assessor) and
// the tools-enabled AgentAdapter (editor), plus the no-connection ambient-login
// path (Phase 2 empty ConnectionRef → no key). It also exercises the two
// correctness fixes the harness discovered: the executor now parses the
// claude-cli chat assessor's RAW JSON text into a map under its outputSchema
// (provider-agnostic structured output), so `assessor.done` resolves downstream
// just as it does on the HTTP chat backends.
//
// HERMETIC / PATH-PROVEN: provider output is canned stream-json from the fake
// CommandRunner. The assessor reports done=false for the first two passes and
// done=true on the third, so the loop runs exactly three real passes and exits
// through the loop-or-exit conditional's `assessor.done` branch — NOT via a
// counter cap and NOT via an external cancel. Assertions also cover DELIVERY
// (recorded args, no connections, no key reads).
func TestAcceptance_01_LocalClaudeRun_NoKeys(t *testing.T) {
	runner := newFakeRunner()
	// agent (editor): tools-enabled invocation → --dangerously-skip-permissions.
	runner.on("--dangerously-skip-permissions", claudeAgentLines...)
	// chat (assessor): tool-suppressed invocation. done: false, false, true →
	// the loop exits on `assessor.done` on the third pass via the real engine
	// path (no self-cancel guard).
	runner.withChatDoneScript(false, false, true)

	b := claudeBinding(runner)
	h := newHarness(t, b)

	// High max_iterations so the done-condition, not the counter cap, ends it.
	res := h.runWorkedExample(t, 10)
	require.Equal(t, instance.StateCompleted, res.instance.State,
		"the claude loop self-terminates through the conditional on assessor.done")

	// Exactly three passes: the assessor (claude-cli chat) was invoked once per
	// pass and the third reported done=true.
	chatSpecs := chatInvocationSpecs(runner)
	require.Len(t, chatSpecs, 3, "the loop ran exactly three assess passes before exiting on done")

	// The counter exited BELOW the cap, proving the done-branch (not gte) fired.
	require.Equal(t, float64(3), res.instance.Variables["iteration"],
		"iteration reached 3 (< max 10): the loop exited on done, not on the counter cap")

	// assessor.done is the parsed boolean in the persisted assessor output.
	assessor, ok := res.instance.Variables["assessor"].(map[string]any)
	require.True(t, ok, "assessor structured output is a map (provider-agnostic parse)")
	assert.Equal(t, true, assessor["done"], "the final assessor pass resolved done=true")

	specs := runner.recordedSpecs()

	// editor: tools-enabled claude -p with --dangerously-skip-permissions.
	agentSpecs := runner.specsMatching("--dangerously-skip-permissions")
	require.NotEmpty(t, agentSpecs, "editor (agent_call) must run claude -p with tools enabled")
	for _, s := range agentSpecs {
		joined := strings.Join(s.Args, " ")
		assert.Contains(t, joined, "--print", "agent runs headless --print")
		assert.Contains(t, joined, "--dangerously-skip-permissions")
		assert.NotEmpty(t, s.Dir, "agent runs in the per-instance {{workspace}}")
	}

	// assessor: tool-suppressed claude -p (--allowedTools "" and NO skip-permissions).
	var chatCount int
	for _, s := range specs {
		joined := strings.Join(s.Args, " ")
		if strings.Contains(joined, "--dangerously-skip-permissions") {
			continue
		}
		chatCount++
		val, ok := argValue(s.Args, "--allowedTools")
		assert.True(t, ok, "chat must pass --allowedTools (empty set)")
		assert.Equal(t, "", val, "chat must suppress tools")
		assert.NotContains(t, joined, "--dangerously-skip-permissions",
			"chat must never emit --dangerously-skip-permissions")
	}
	require.Positive(t, chatCount, "assessor (llm_call) must run tool-suppressed claude -p")

	// ZERO connections required and NO key read: every claude-cli invocation's
	// env carries only the base allowlist — no provider API-key var was injected
	// (the empty-ConnectionRef path never reveals a key).
	require.Empty(t, b.connections, "the Claude binding stores no connections")
	for _, s := range specs {
		for _, e := range s.Env {
			assert.False(t, strings.HasPrefix(e, "ANTHROPIC_API_KEY="),
				"no API key may be injected on the no-key subscription path")
			assert.False(t, strings.HasPrefix(e, "OPENROUTER_API_KEY="), "no key injected")
			assert.False(t, strings.HasPrefix(e, "AZURE_OPENAI_API_KEY="), "no key injected")
		}
	}
}

// argValue returns the value following flag in args, and whether flag was found.
func argValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag {
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", true
		}
	}
	return "", false
}
