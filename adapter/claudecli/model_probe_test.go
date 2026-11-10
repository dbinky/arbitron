package claudecli

import (
	"context"
	"fmt"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const proofModel = "claude-sonnet-4-6"

// Check the actual launch inputs before supplying any execution evidence.
type modelEvidenceRunner struct {
	t *testing.T
	fakeRunner
	agent  bool
	legacy bool
}

func (r *modelEvidenceRunner) Run(ctx context.Context, spec RunSpec) (<-chan string, func() error, error) {
	r.t.Helper()
	model, _ := argValue(spec.Args, "--model")
	require.Equal(r.t, proofModel, model)
	format, _ := argValue(spec.Args, "--output-format")
	require.Equal(r.t, "stream-json", format)
	require.NotContains(r.t, spec.Args, "--fallback-model")
	require.Contains(r.t, spec.Args, "--print")
	require.Equal(r.t, "prove this turn", spec.Stdin)
	require.Equal(r.t, "/tmp/proof-workspace", spec.Dir)
	tools, found := argValue(spec.Args, "--allowedTools")
	if r.legacy {
		require.False(r.t, found)
	} else {
		require.True(r.t, found)
	}
	if r.agent {
		require.Equal(r.t, "Read", tools)
		require.Contains(r.t, spec.Env, "ANTHROPIC_API_KEY=proof-secret")
	} else {
		require.Empty(r.t, tools)
		require.NotContains(r.t, spec.Args, "--dangerously-skip-permissions")
	}
	return r.fakeRunner.Run(ctx, spec)
}

func TestModelProbeClaudeNonStreamingExactExecution(t *testing.T) {
	for _, adapter := range []string{"chat", "agent", "stream-agent-chat", "legacy-provider"} {
		for _, tc := range []struct {
			name, usage string
			wantErr     bool
		}{
			{"exact", `{"claude-sonnet-4-6":{}}`, false},
			{"helper permitted", `{"claude-sonnet-4-6":{},"claude-haiku-4-5":{}}`, false},
			{"context window", `{"claude-sonnet-4-6[1m]":{}}`, false},
			{"unsupported suffix", `{"claude-sonnet-4-6[proof-secret]":{}}`, true},
			{"unterminated suffix", `{"claude-sonnet-4-6[1m":{}}`, true},
			{"suffix trailing data", `{"claude-sonnet-4-6[1m]-fallback":{}}`, true},
			{"repeated suffix", `{"claude-sonnet-4-6[1m][1m]":{}}`, true},
			{"wrong", `{"fallback-secret":{}}`, true},
			{"missing", `null`, true},
			{"empty", `{}`, true},
		} {
			t.Run(adapter+"/"+tc.name, func(t *testing.T) {
				r := &modelEvidenceRunner{t: t, agent: adapter == "agent" || adapter == "stream-agent-chat", legacy: adapter == "legacy-provider"}
				r.lines = []string{fmt.Sprintf(`{"type":"result","is_error":false,"result":"verified output","session_id":"s","modelUsage":%s}`, tc.usage)}
				cfg := Config{BinaryPath: "claude", Runner: r, AllowedTools: []string{"Read"}}
				binding := provider.ResolvedBinding{Model: proofModel, Pinned: true, RevealKey: func(context.Context) (string, error) { return "proof-secret", nil }}
				req := engine.ChatRequest{Model: proofModel, Prompt: "prove this turn", WorkDir: "/tmp/proof-workspace", RequireExecutedModel: true}
				var output any
				var err error
				switch adapter {
				case "agent":
					output, err = NewAgentAdapterForBinding(cfg, binding).RunAgent(context.Background(), engine.AgentMission{Model: req.Model, Prompt: req.Prompt, WorkDir: req.WorkDir})
				case "stream-agent-chat":
					output, err = NewStreamAgentAdapterForBinding(cfg, binding).Chat(context.Background(), req)
				case "legacy-provider":
					// The deprecated provider still implements the same public port.
					cfg.AllowedTools = nil
					output, err = New(cfg).Chat(context.Background(), req)
				default:
					output, err = NewChatAdapterForBinding(cfg, binding).Chat(context.Background(), req)
				}
				if tc.wantErr {
					require.ErrorContains(t, err, "model execution evidence")
					assert.NotContains(t, err.Error(), "fallback-secret")
					assert.NotContains(t, err.Error(), "proof-secret")
					assert.NotContains(t, fmt.Sprint(output), "verified output")
				} else {
					require.NoError(t, err)
					assert.Contains(t, fmt.Sprint(output), "verified output")
				}
			})
		}
	}
}

func TestModelProbeClaudeNonStreamingRequiredEvidence(t *testing.T) {
	for _, tc := range []struct {
		name             string
		binding          provider.ResolvedBinding
		req              engine.ChatRequest
		wantErr, noSpawn bool
	}{
		{"binding alone", provider.ResolvedBinding{Model: proofModel, Pinned: true}, engine.ChatRequest{Model: proofModel}, true, false},
		{"request alone", provider.ResolvedBinding{}, engine.ChatRequest{Model: proofModel, RequireExecutedModel: true}, true, false},
		{"legacy", provider.ResolvedBinding{Model: proofModel}, engine.ChatRequest{Model: proofModel}, false, false},
		{"empty explicit pin", provider.ResolvedBinding{}, engine.ChatRequest{RequireExecutedModel: true}, true, true},
		{"empty bound pin", provider.ResolvedBinding{Pinned: true}, engine.ChatRequest{}, true, true},
		{"stale binding", provider.ResolvedBinding{Model: "other-model", Pinned: true}, engine.ChatRequest{Model: proofModel}, true, true},
	} {
		for _, adapter := range []string{"chat", "stream-agent-chat", "agent"} {
			if adapter == "agent" && tc.req.RequireExecutedModel {
				continue // AgentMission has no request evidence flag; its binding owns the pin.
			}
			t.Run(adapter+"/"+tc.name, func(t *testing.T) {
				r := &modelEvidenceRunner{t: t, agent: adapter != "chat"}
				r.lines = []string{`{"type":"result","result":"unverified output","modelUsage":{"fallback":{}}}`}
				req := tc.req
				req.Prompt, req.WorkDir = "prove this turn", "/tmp/proof-workspace"
				binding := tc.binding
				binding.RevealKey = func(context.Context) (string, error) { return "proof-secret", nil }
				cfg := Config{BinaryPath: "claude", Runner: r, AllowedTools: []string{"Read"}}
				var out any
				var err error
				switch adapter {
				case "agent":
					out, err = NewAgentAdapterForBinding(cfg, binding).RunAgent(context.Background(), engine.AgentMission{Model: req.Model, Prompt: req.Prompt, WorkDir: req.WorkDir})
				case "stream-agent-chat":
					out, err = NewStreamAgentAdapterForBinding(cfg, binding).Chat(context.Background(), req)
				default:
					out, err = NewChatAdapterForBinding(cfg, binding).Chat(context.Background(), req)
				}
				if tc.wantErr {
					require.ErrorContains(t, err, "model execution evidence")
					assert.Nil(t, out)
				} else {
					require.NoError(t, err)
					assert.Contains(t, fmt.Sprint(out), "unverified output")
				}
				assert.Equal(t, !tc.noSpawn, r.runCalled)
			})
		}
	}
}

func TestModelProbeClaudeNonStreamingCannotReplaceFailedProof(t *testing.T) {
	for _, first := range []string{
		`{"type":"result","result":"fallback","modelUsage":{"fallback":{}}}`,
		`{"type":"result","result":"failed","is_error":true}`,
		`{"type":"result","result":"first"}`,
	} {
		t.Run(first, func(t *testing.T) {
			r := &modelEvidenceRunner{t: t}
			r.lines = []string{first, `{"type":"result","result":"later success","modelUsage":{"claude-sonnet-4-6":{}}}`}
			out, err := NewChatAdapter(Config{BinaryPath: "claude", Runner: r}).Chat(context.Background(), engine.ChatRequest{Model: proofModel, Prompt: "prove this turn", WorkDir: "/tmp/proof-workspace", RequireExecutedModel: true})
			require.Error(t, err)
			assert.Nil(t, out)
		})
	}
}

// A batch call (agent_call) whose session re-woke for background subagents
// returns the LAST proven result: that wake holds the final answer.
func TestModelProbeClaudeNonStreamingAcceptsRepeatedProof(t *testing.T) {
	r := &modelEvidenceRunner{t: t}
	r.lines = []string{
		`{"type":"result","result":"first","modelUsage":{"claude-sonnet-4-6":{}}}`,
		`{"type":"result","result":"later success","modelUsage":{"claude-sonnet-4-6":{}}}`,
	}
	out, err := NewChatAdapter(Config{BinaryPath: "claude", Runner: r}).Chat(context.Background(), engine.ChatRequest{Model: proofModel, Prompt: "prove this turn", WorkDir: "/tmp/proof-workspace", RequireExecutedModel: true})
	require.NoError(t, err)
	assert.Contains(t, fmt.Sprint(out), "later success")
}

func TestModelProbeClaudeNonStreamingRejectsLaterUnprovenResult(t *testing.T) {
	proven := `{"type":"result","result":"first","modelUsage":{"claude-sonnet-4-6":{}}}`
	for _, later := range []string{
		`{"type":"result","result":"fallback","modelUsage":{"fallback":{}}}`,
		`{"type":"result","result":"failed","is_error":true,"modelUsage":{"claude-sonnet-4-6":{}}}`,
		`{"type":"result","result":"missing"}`,
	} {
		t.Run(later, func(t *testing.T) {
			r := &modelEvidenceRunner{t: t}
			r.lines = []string{proven, later}
			out, err := NewChatAdapter(Config{BinaryPath: "claude", Runner: r}).Chat(context.Background(), engine.ChatRequest{Model: proofModel, Prompt: "prove this turn", WorkDir: "/tmp/proof-workspace", RequireExecutedModel: true})
			require.ErrorContains(t, err, "model execution evidence")
			assert.Nil(t, out)
		})
	}
}
