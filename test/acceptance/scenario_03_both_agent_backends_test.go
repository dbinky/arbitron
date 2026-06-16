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

		// ENV-INJECTION: the connection key reaches the opencode subprocess as a
		// provider-keyed env var (delivery half of #7). In opencode 1.17.x the
		// base-URL is NOT a standalone env var — it rides OPENCODE_CONFIG_CONTENT
		// as a provider block so opencode can route the call correctly.
		key, ok := envValue(opencodeSpec.Env, "OPENROUTER_API_KEY")
		require.True(t, ok, "the opencode env carries the provider-keyed API key")
		assert.Equal(t, opencodeKey, key, "the env-injected key is the connection's sealed key")
		_, hasBaseURLEnv := envValue(opencodeSpec.Env, "OPENROUTER_BASE_URL")
		assert.False(t, hasBaseURLEnv,
			"opencode 1.17.x must NOT set OPENROUTER_BASE_URL in env (base URL rides OPENCODE_CONFIG_CONTENT)")
		cfg, hasCfg := envValue(opencodeSpec.Env, "OPENCODE_CONFIG_CONTENT")
		require.True(t, hasCfg, "the opencode env carries the provider base-url via OPENCODE_CONFIG_CONTENT")
		assert.Contains(t, cfg, "https://openweight.example/v1",
			"OPENCODE_CONFIG_CONTENT must embed the connection base URL")
		// The base allowlist is preserved.
		_, hasPath := envValue(opencodeSpec.Env, "PATH")
		assert.True(t, hasPath, "base allowlist PATH preserved in the injected env")
	})

	// --- Backend C: editor via codex (subscription, NO API key) -------------
	// Proves the THIRD agent backend: an agent_call node with backend: codex runs
	// a mission through the shared fake runner on the ChatGPT subscription with no
	// API key configured (codex authenticates via its on-disk credential).
	t.Run("editor via codex agent with no API key", func(t *testing.T) {
		runner := newFakeRunner().on("exec", codexEditorLines...)
		transport := newFakeTransport(true) // assessor (openrouter) reports done → completes
		h := newHarness(t, codexBinding(runner, transport))

		res := h.runWorkedExample(t, 5)
		require.Equal(t, instance.StateCompleted, res.instance.State,
			"codex run completes (codex editor + openrouter assessor)")

		// The codex invocation: the pinned `codex exec` contract — the exec
		// subcommand, the bypass posture, -m <model>, --cd <WorkDir>, --json — with
		// the prompt on stdin (NOT argv) and NO claude --print flag. The `--version`
		// probe spec is skipped.
		var codexSpec *claudecli.RunSpec
		recorded := runner.recordedSpecs()
		for i := range recorded {
			joined := strings.Join(recorded[i].Args, " ")
			if strings.Contains(joined, "exec") && !strings.Contains(joined, "--version") {
				codexSpec = &recorded[i]
				break
			}
		}
		require.NotNil(t, codexSpec, "editor must run via the codex AgentProvider")
		assert.Equal(t, "exec", codexSpec.Args[0], "codex uses the exec subcommand")
		assert.Contains(t, codexSpec.Args, "--dangerously-bypass-approvals-and-sandbox",
			"codex runs under the fixed v1 bypass posture")
		assert.Contains(t, codexSpec.Args, "--json", "codex emits the machine-readable event stream")
		m, ok := argValue(codexSpec.Args, "-m")
		require.True(t, ok, "codex routes the model via -m")
		assert.Equal(t, "gpt-5-codex", m)
		cd, ok := argValue(codexSpec.Args, "--cd")
		require.True(t, ok, "codex carries the WorkDir via --cd")
		assert.NotEmpty(t, cd, "the --cd WorkDir is the instance {{workspace}}")
		assert.NotEmpty(t, codexSpec.Dir, "codex runs in cmd.Dir = the instance {{workspace}}")
		assert.NotEmpty(t, codexSpec.Stdin, "the mission prompt is delivered via stdin")

		// SECURITY: subscription path — NO OPENAI_API_KEY in the codex env, and the
		// prompt never appears in argv.
		_, hasKey := envValue(codexSpec.Env, "OPENAI_API_KEY")
		assert.False(t, hasKey, "codex subscription env must carry NO OPENAI_API_KEY")
		for _, a := range codexSpec.Args {
			assert.NotEqual(t, codexSpec.Stdin, a, "the prompt must never appear in argv")
		}
		// The base allowlist PATH is preserved.
		_, hasPath := envValue(codexSpec.Env, "PATH")
		assert.True(t, hasPath, "base allowlist PATH preserved in the codex env")
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
