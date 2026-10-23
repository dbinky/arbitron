package claudecli

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// agentProbeRunner emits the requested model as executed only when every
// load-bearing launch input of a tool-suppressed agent probe is present: the
// pinned --model, the empty allowlist without skip-permissions, the binding's
// credential env, the probe prompt and the probe directory.
type agentProbeRunner struct {
	fakeRunner
	usage string
}

func (r *agentProbeRunner) Run(ctx context.Context, spec RunSpec) (<-chan string, func() error, error) {
	model, _ := argValue(spec.Args, "--model")
	tools, toolsFound := argValue(spec.Args, "--allowedTools")
	usage := r.usage
	if usage == "" {
		usage = fmt.Sprintf(`{%q:{}}`, model)
	}
	switch {
	case !toolsFound || tools != "",
		containsArg(spec.Args, "--dangerously-skip-permissions"),
		!containsArg(spec.Env, "ANTHROPIC_API_KEY=probe-secret"),
		!containsArg(spec.Env, "ANTHROPIC_BASE_URL=https://gateway.example.test"),
		spec.Stdin != "probe prompt",
		spec.Dir != "/tmp/probe-workspace/.model-probe":
		usage = `{"claude-haiku-4-5":{}}`
	}
	session := `"session_id":"s",`
	if r.usage == "noSession" {
		session, usage = "", fmt.Sprintf(`{%q:{}}`, model)
	}
	r.lines = []string{`{"type":"result","is_error":false,"result":"ok",` + session + `"modelUsage":` + usage + `}`}
	return r.fakeRunner.Run(ctx, spec)
}

func containsArg(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func probeBinding(model string) provider.ResolvedBinding {
	return provider.ResolvedBinding{
		Kind: provider.ProfileKindAgent, Backend: "claude-code", Provider: "anthropic-api",
		BaseURL: "https://gateway.example.test", Model: model, Pinned: true,
		RevealKey: func(context.Context) (string, error) { return "probe-secret", nil },
	}
}

func TestAgentUsabilityProbeProvesPinnedModelWithToolsSuppressed(t *testing.T) {
	r := &agentProbeRunner{}
	cfg := Config{BinaryPath: "claude", Runner: r, WorkspaceRoot: "/tmp/probe-workspace", AllowedTools: []string{"Bash", "Write"}, SkipPermissions: true}
	got, session, err := NewAgentAdapterForBinding(cfg, probeBinding(proofModel)).ProbeExecutedModel(context.Background(), "probe prompt", "/tmp/probe-workspace/.model-probe")
	require.NoError(t, err)
	assert.Equal(t, proofModel, got)
	assert.Equal(t, "s", session, "proof carries the CLI's own session id")
	assert.Equal(t, "claude", r.gotSpec.Bin)
	assert.NotContains(t, r.gotSpec.Args, "Bash,Write", "the agent tool scope must not reach a probe")
}

func TestAgentUsabilityProbeRejectsMissingOrWrongEvidence(t *testing.T) {
	for _, tc := range []struct{ name, usage string }{
		{"silent fallback", `{"claude-haiku-4-5":{}}`},
		{"missing usage", `null`},
		{"empty usage", `{}`},
		{"secret-bearing model", `{"probe-secret":{}}`},
		{"no session id", "noSession"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &agentProbeRunner{usage: tc.usage}
			cfg := Config{BinaryPath: "claude", Runner: r, WorkspaceRoot: "/tmp/probe-workspace"}
			got, session, err := NewAgentAdapterForBinding(cfg, probeBinding(proofModel)).ProbeExecutedModel(context.Background(), "probe prompt", "/tmp/probe-workspace/.model-probe")
			require.ErrorIs(t, err, errModelExecutionEvidence)
			assert.Empty(t, got)
			assert.Empty(t, session)
			assert.NotContains(t, err.Error(), "probe-secret")
		})
	}
}

func TestAgentUsabilityProbeRefusesBeforeLaunch(t *testing.T) {
	revealFails := probeBinding(proofModel)
	revealFails.RevealKey = func(context.Context) (string, error) { return "", errors.New("vault sealed") }
	for _, tc := range []struct {
		name    string
		binding provider.ResolvedBinding
		workDir string
	}{
		{"empty model", probeBinding(""), "/tmp/probe-workspace/.model-probe"},
		{"empty work dir", probeBinding(proofModel), ""},
		{"work dir outside the workspace root", probeBinding(proofModel), "/etc"},
		{"credential reveal fails", revealFails, "/tmp/probe-workspace/.model-probe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &agentProbeRunner{}
			cfg := Config{BinaryPath: "claude", Runner: r, WorkspaceRoot: "/tmp/probe-workspace"}
			got, _, err := NewAgentAdapterForBinding(cfg, tc.binding).ProbeExecutedModel(context.Background(), "probe prompt", tc.workDir)
			require.Error(t, err)
			assert.Empty(t, got)
			assert.False(t, r.runCalled, "nothing may launch without an explicit model, an isolated work dir and the credential env")
		})
	}
}
