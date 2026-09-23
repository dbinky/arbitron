package pi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/provider"
)

func contains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// assertAdjacent asserts args contains a immediately followed by b.
func assertAdjacent(t *testing.T, args []string, a, b string) {
	t.Helper()
	for i := 0; i+1 < len(args); i++ {
		if args[i] == a && args[i+1] == b {
			return
		}
	}
	t.Fatalf("expected adjacent %q %q in %v", a, b, args)
}

// HAPPY — the baseline argv pins the non-interactive JSON contract: print mode,
// JSON event stream, no session file, and the model.
func TestBuildArgs_BaselineContract(t *testing.T) {
	args, err := buildArgs(provider.ResolvedBinding{Model: "anthropic/claude-sonnet-5"})
	require.NoError(t, err)
	assert.True(t, contains(args, printFlag), "non-interactive print mode")
	assertAdjacent(t, args, modeFlag, modeJSON)
	assert.True(t, contains(args, noSessionFlag), "agent missions are independent; no session file")
	assertAdjacent(t, args, modelFlag, "anthropic/claude-sonnet-5")
}

// SUCCESS — a graded effort rides argv as pi's --thinking level, which shares
// arbitron's own vocabulary for low..max.
func TestBuildArgs_GradedEffortBecomesThinkingLevel(t *testing.T) {
	for _, e := range []provider.Effort{
		provider.EffortLow, provider.EffortMedium, provider.EffortHigh,
		provider.EffortXHigh, provider.EffortMax,
	} {
		args, err := buildArgs(provider.ResolvedBinding{Model: "m", Effort: e})
		require.NoError(t, err, "effort %q", e)
		assertAdjacent(t, args, thinkingFlag, string(e))
	}
}

// SUCCESS — EffortNone explicitly disables reasoning, which pi spells "off"
// rather than "none".
func TestBuildArgs_EffortNoneMapsToOff(t *testing.T) {
	args, err := buildArgs(provider.ResolvedBinding{Model: "m", Effort: provider.EffortNone})
	require.NoError(t, err)
	assertAdjacent(t, args, thinkingFlag, thinkingOff)
	assert.False(t, contains(args, "none"), "pi has no \"none\" level")
}

// FAILURE — an empty model fails closed with NO argv, so a mission can never
// launch against pi's own default model.
func TestBuildArgs_EmptyModelFailsClosed(t *testing.T) {
	args, err := buildArgs(provider.ResolvedBinding{Model: "   "})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrModelRequired)
	assert.Nil(t, args, "no argv on a failed build")
}

// ERROR — an effort value outside the axis fails closed rather than being
// passed through to pi, which would reject it at startup with a worse message.
func TestBuildArgs_UnknownEffortFailsClosed(t *testing.T) {
	_, err := buildArgs(provider.ResolvedBinding{Model: "m", Effort: provider.Effort("turbo")})
	require.Error(t, err)
	assert.ErrorIs(t, err, provider.ErrInvalidEffort)
}

// EDGE — the default (unset) effort emits no --thinking at all, leaving the
// model's own default. Both the explicit "default" and the empty value behave
// identically.
func TestBuildArgs_DefaultEffortEmitsNoThinkingFlag(t *testing.T) {
	for _, e := range []provider.Effort{provider.EffortDefault, ""} {
		args, err := buildArgs(provider.ResolvedBinding{Model: "m", Effort: e})
		require.NoError(t, err)
		assert.False(t, contains(args, thinkingFlag), "effort %q must emit no thinking flag: %v", e, args)
	}
}

// SECURITY — neither the prompt nor the working directory may reach argv. The
// prompt is stdin-only (argv is world-readable via ps) and the working
// directory is delivered as the subprocess cwd, not a flag.
func TestBuildArgs_NeverCarriesPromptOrWorkDir(t *testing.T) {
	args, err := buildArgs(provider.ResolvedBinding{Model: "m", Effort: provider.EffortHigh})
	require.NoError(t, err)
	for _, a := range args {
		assert.NotContains(t, a, "SECRET-PROMPT")
		assert.NotContains(t, a, "/tmp/ws")
	}
	assert.False(t, contains(args, "--cd"), "pi takes its working directory from the process cwd")
}
