package acceptance

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/instance"
)

// TestAcceptance_01_LocalClaudeRun_NoKeys proves spec §8 #1: the worked-example
// pipeline runs locally on a Claude subscription with NO API keys — `llm_call`
// via tool-suppressed claude -p and `agent_call` via claude -p with tools.
//
// Requires (Phase 1): the tool-suppressed claude-cli ChatAdapter (assessor) and
// the tools-enabled AgentAdapter (editor), plus the no-connection ambient-login
// path (Phase 2 empty ConnectionRef → no key).
//
// HERMETIC / PATH-PROVEN: provider output is canned stream-json from the fake
// CommandRunner, so the assertions are on DELIVERY (recorded args, no
// connections, no key reads) over multiple real loop passes — not live model
// output.
//
// DOCUMENTED LIMITATION (path-only, not full completion): over the claude-cli
// CHAT path the assessor's structured output is delivered as RAW TEXT (claude
// -p returns the result string verbatim; processLLMOutput does not parse it), so
// `assessor.done` never resolves and the worked example's loop cannot
// self-terminate on the claude path. This scenario therefore bounds the run with
// a fake-runner self-guard that calls engine.Cancel after several passes — the
// FAKE, not the engine, stops the loop (the same technique loop_mechanics_test
// uses for the no-auto-kill case). End-to-end completion on a structured-output
// chat backend is proven to COMPLETION under #2/#4/#5 (the HTTP chat binding,
// whose adapters parse structured output into a map). Live sign-off against a
// real subscription is a manual / non-CI check.
func TestAcceptance_01_LocalClaudeRun_NoKeys(t *testing.T) {
	runner := newFakeRunner()
	// agent (editor): tools-enabled invocation → --dangerously-skip-permissions.
	runner.on("--dangerously-skip-permissions", claudeAgentLines...)
	// chat (assessor): tool-suppressed invocation → no skip-permissions, empty
	// allowed-tools. Never resolvably "done" over the claude-cli chat path.
	runner.withFallback(claudeAssessorLines(false)...)

	b := claudeBinding(runner)
	h := newHarness(t, b)

	// Bound the otherwise-non-terminating claude loop: after guard invocations
	// the FAKE cancels the run. idCh hands the id to the guard closure.
	const guard = 6
	idCh := make(chan uuid.UUID, 1)
	runner.armGuard(guard, func() { _ = h.engine.Cancel(<-idCh) })

	id := h.trigger(t, 2)
	idCh <- id

	inst := h.waitForTerminal(t, id, 10*time.Second)
	require.Equal(t, instance.StateCancelled, inst.State,
		"the claude loop is stopped by the fake guard, not the engine (no auto-kill)")

	specs := runner.recordedSpecs()
	require.GreaterOrEqual(t, len(specs), guard,
		"the engine kept re-entering the loop body across multiple passes")

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
