package acceptance

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/instance"
)

// TestAcceptance_03_BothAgentBackends proves spec §8 #3: an `agent_call`
// (editor) executes via BOTH agent backends — claude -p (Claude configuration)
// and opencode on a non-Claude / open-weight model (open-weight configuration) —
// proving the opencode AgentProvider path and its env-injected credential.
//
// Requires: Phase 1 claude-cli AgentAdapter; Phase 3 opencode AgentProvider +
// env-injection; Phase 2 connection-backed open-weight binding.
func TestAcceptance_03_BothAgentBackends(t *testing.T) {
	// --- Backend A: editor via claude -p (Claude binding) -------------------
	t.Run("editor via claude-cli agent", func(t *testing.T) {
		runner := newFakeRunner()
		runner.on("--dangerously-skip-permissions", claudeAgentLines...)
		// The claude-cli chat assessor exits on assessor.done on the second pass;
		// the loop terminates through the real conditional (no self-cancel guard).
		runner.withChatDoneScript(false, true)

		h := newHarness(t, claudeBinding(runner))
		res := h.runWorkedExample(t, 10)
		require.Equal(t, instance.StateCompleted, res.instance.State,
			"the claude agent run completes through the conditional on assessor.done")

		agentSpecs := runner.specsMatching("--dangerously-skip-permissions")
		require.NotEmpty(t, agentSpecs, "editor must run via the claude-cli AgentAdapter (claude -p, tools)")
		s := agentSpecs[0]
		joined := strings.Join(s.Args, " ")
		assert.Contains(t, joined, "--print")
		assert.Contains(t, joined, "--dangerously-skip-permissions")
		assert.NotEmpty(t, s.Dir, "the claude agent runs in the per-instance {{workspace}}")
		// No connection key is injected on the claude no-key path.
		for _, e := range s.Env {
			assert.False(t, strings.HasPrefix(e, "OPENROUTER_API_KEY="), "no key on the claude no-key path")
		}

		// Explicitly prove the done-exit branch fired, not the counter cap.
		// done-script = (false, true) → exactly 2 assessor passes before exit.
		chatSpecs := chatInvocationSpecs(runner)
		assert.Len(t, chatSpecs, 2,
			"the Claude assessor ran exactly 2 passes before done=true ended the loop")
		assert.Equal(t, float64(2), res.instance.Variables["iteration"],
			"iteration == 2: the loop ran 2 passes under the claude binding (counter cap at 10 did not fire)")
		assessor, ok := res.instance.Variables["assessor"].(map[string]any)
		require.True(t, ok, "assessor structured output must be a map in instance variables")
		assert.Equal(t, true, assessor["done"],
			"assessor.done is true: the done-exit branch fired under the claude binding, not the counter cap")
	})

	// --- Backend B: editor via opencode (open-weight binding) ---------------
	t.Run("editor via opencode agent with env-injected credential", func(t *testing.T) {
		runner := newFakeRunner().withFallback("Editing the workspace.", "Done.")
		transport := newFakeTransport(true) // assessor (openrouter) reports done → completes
		b := openWeightBinding(runner, transport)
		h := newHarness(t, b)

		res := h.runWorkedExample(t, 5)
		require.Equal(t, instance.StateCompleted, res.instance.State,
			"open-weight run completes (opencode editor + openrouter assessor)")

		// The opencode invocation: modern run contract (`run <prompt> -m
		// provider/model`), the cwd carried as cmd.Dir (the {{workspace}} pin, NOT
		// an arg), running in the instance {{workspace}}, NO claude --print flag.
		// The `--version` probe spec is skipped.
		var opencodeSpec *claudecli.RunSpec
		recorded := runner.recordedSpecs()
		for i := range recorded {
			joined := strings.Join(recorded[i].Args, " ")
			if strings.Contains(joined, "run") && strings.Contains(joined, "-m") && !strings.Contains(joined, "--print") {
				opencodeSpec = &recorded[i]
				break
			}
		}
		require.NotNil(t, opencodeSpec, "editor must run via the opencode AgentProvider")
		assert.Equal(t, "run", opencodeSpec.Args[0], "opencode uses the modern run subcommand")
		m, ok := argValue(opencodeSpec.Args, "-m")
		require.True(t, ok, "opencode routes the model via -m provider/model")
		assert.Equal(t, "openrouter/qwen/qwen3-coder", m,
			"the open-weight model is routed to the opencode provider id")
		assert.NotEmpty(t, opencodeSpec.Dir, "opencode runs in cmd.Dir = the instance {{workspace}}")

		// ENV-INJECTION: the connection key + base-url reach the opencode
		// subprocess as provider-keyed env vars (delivery half of #7).
		key, ok := envValue(opencodeSpec.Env, "OPENROUTER_API_KEY")
		require.True(t, ok, "the opencode env carries the provider-keyed API key")
		assert.Equal(t, opencodeKey, key, "the env-injected key is the connection's sealed key")
		baseURL, ok := envValue(opencodeSpec.Env, "OPENROUTER_BASE_URL")
		require.True(t, ok, "the opencode env carries the provider base-url")
		assert.Equal(t, "https://openweight.example/v1", baseURL)
		// The base allowlist is preserved.
		_, hasPath := envValue(opencodeSpec.Env, "PATH")
		assert.True(t, hasPath, "base allowlist PATH preserved in the injected env")
	})
}

// envValue returns the value of name=... in env, and whether it was present.
func envValue(env []string, name string) (string, bool) {
	prefix := name + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return strings.TrimPrefix(e, prefix), true
		}
	}
	return "", false
}
