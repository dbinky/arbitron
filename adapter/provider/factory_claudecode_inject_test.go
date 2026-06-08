package provider_test

import (
	"context"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/claudecli"
	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureRunner records the single RunSpec it is handed and replays a canned
// claude result line so RunAgent succeeds.
type captureRunner struct {
	spec   claudecli.RunSpec
	called bool
}

func (r *captureRunner) Run(_ context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	r.called = true
	r.spec = spec
	ch := make(chan string, 1)
	ch <- `{"type":"result","is_error":false,"result":"done","session_id":"s"}`
	close(ch)
	return ch, func() error { return nil }, nil
}

// integration — the factory's claude-code arm hands the binding to a
// binding-aware claude agent adapter so the gateway endpoint + key reach the
// subprocess env (closes the loop from Task 2's placeholder).
func TestFactory_ClaudeCodeArm_InjectsBindingEnv(t *testing.T) {
	rr := &captureRunner{}
	f := adapterprovider.NewFactory(adapterprovider.Deps{
		DefaultAgentBackend: "claude-code",
		ClaudeAgentConfig:   claudecli.Config{BinaryPath: "claude", Runner: rr},
	})

	b := provider.ResolvedBinding{
		Kind:      provider.ProfileKindAgent,
		Provider:  "litellm",
		Backend:   "claude-code",
		BaseURL:   "https://litellm.internal:4000",
		Model:     "gpt-4o",
		RevealKey: func(context.Context) (string, error) { return "sk-inject", nil },
	}

	ap, err := f.AgentProviderFor(b)
	require.NoError(t, err)
	require.IsType(t, (*claudecli.AgentAdapter)(nil), ap)

	_, err = ap.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-4o"})
	require.NoError(t, err)
	require.True(t, rr.called)

	joined := strings.Join(rr.spec.Env, "\n")
	assert.Contains(t, joined, "ANTHROPIC_BASE_URL=https://litellm.internal:4000")
	assert.Contains(t, joined, "ANTHROPIC_API_KEY=sk-inject")
	// model flag from binding.
	assert.Contains(t, strings.Join(rr.spec.Args, " "), "--model gpt-4o")
}

// subscription — an empty binding through the claude-code arm yields the
// subscription env (no endpoint/key), proving the per-binding path degrades to
// the local subscription cleanly.
func TestFactory_ClaudeCodeArm_EmptyBindingIsSubscription(t *testing.T) {
	rr := &captureRunner{}
	f := adapterprovider.NewFactory(adapterprovider.Deps{
		DefaultAgentBackend: "claude-code",
		ClaudeAgentConfig:   claudecli.Config{BinaryPath: "claude", Runner: rr},
	})

	b := provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Backend: "claude-code", Model: "m"}
	ap, err := f.AgentProviderFor(b)
	require.NoError(t, err)

	_, err = ap.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws", Model: "m"})
	require.NoError(t, err)

	joined := strings.Join(rr.spec.Env, "\n")
	assert.NotContains(t, joined, "ANTHROPIC_BASE_URL")
	assert.NotContains(t, joined, "ANTHROPIC_API_KEY")
}
