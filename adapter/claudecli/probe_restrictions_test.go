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

type restrictedProbeRunner struct {
	fakeRunner
	outcome     string
	credentials bool
}

func (r *restrictedProbeRunner) Run(ctx context.Context, spec RunSpec) (<-chan string, func() error, error) {
	for flag, want := range map[string]string{"--tools": "", "--setting-sources": "", "--mcp-config": `{"mcpServers":{}}`, "--model": proofModel, "--output-format": "stream-json"} {
		got, found := argValue(spec.Args, flag)
		if !found || got != want {
			return nil, nil, fmt.Errorf("missing probe restriction/input %s", flag)
		}
	}
	for _, flag := range []string{"--safe-mode", "--strict-mcp-config", "--no-chrome", "--print"} {
		if !containsArg(spec.Args, flag) {
			return nil, nil, fmt.Errorf("missing probe restriction %s", flag)
		}
	}
	for _, flag := range []string{"--bare", "--dangerously-skip-permissions", "--fallback-model", "--plugin-dir"} {
		if containsArg(spec.Args, flag) {
			return nil, nil, fmt.Errorf("unexpected probe option %s", flag)
		}
	}
	for _, env := range []string{"DISABLE_UPDATES=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "CLAUDE_CODE_DISABLE_OFFICIAL_MARKETPLACE_AUTOINSTALL=1"} {
		if !containsArg(spec.Env, env) {
			return nil, nil, fmt.Errorf("missing probe environment restriction")
		}
	}
	if spec.Bin != "claude" || spec.Stdin != "probe prompt" || spec.Dir != "/tmp/probe-workspace/.model-probe" {
		return nil, nil, fmt.Errorf("missing binary/prompt/cwd")
	}
	if r.credentials && (!containsArg(spec.Env, "ANTHROPIC_API_KEY=probe-secret") || !containsArg(spec.Env, "ANTHROPIC_BASE_URL=https://gateway.example.test")) {
		return nil, nil, fmt.Errorf("missing connection credentials")
	}
	if r.outcome == "rejected config" {
		return nil, nil, fmt.Errorf("unsupported restriction configuration")
	}
	actual := proofModel
	if r.outcome == "wrong model" {
		actual = "fallback"
	}
	r.lines = []string{fmt.Sprintf(`{"type":"result","result":"ok","session_id":"s","modelUsage":{%q:{}}}`, actual)}
	return r.fakeRunner.Run(ctx, spec)
}

func TestModelProbeClaudeRestrictions(t *testing.T) {
	for _, route := range []string{"batch agent", "interactive agent", "batch chat", "interactive chat"} {
		for _, outcome := range []string{"exact", "wrong model", "rejected config"} {
			t.Run(route+"/"+outcome, func(t *testing.T) {
				r := &restrictedProbeRunner{outcome: outcome, credentials: route == "batch agent" || route == "interactive agent"}
				cfg := Config{BinaryPath: "claude", Runner: r, AllowedTools: []string{"Bash", "Write"}}
				req := engine.ChatRequest{Model: proofModel, Prompt: "probe prompt", WorkDir: "/tmp/probe-workspace/.model-probe", SuppressTools: true, RequireExecutedModel: true}
				var err error
				if route == "batch agent" {
					var model string
					model, _, err = NewAgentAdapterForBinding(cfg, probeBinding(proofModel)).ProbeExecutedModel(context.Background(), req.Prompt, req.WorkDir)
					if outcome == "exact" {
						assert.Equal(t, proofModel, model)
					} else {
						assert.Empty(t, model)
					}
				} else {
					var a engine.ChatProvider = NewChatAdapter(cfg)
					if route == "interactive agent" {
						a = NewStreamAgentAdapterForBinding(cfg, probeBinding(proofModel))
					}
					if route == "batch chat" {
						_, err = a.Chat(context.Background(), req)
					} else {
						var chunks <-chan engine.ChatChunk
						chunks, err = a.StreamChat(context.Background(), req)
						if err == nil {
							for chunk := range chunks {
								if chunk.Error != nil {
									err = chunk.Error
								}
							}
						}
					}
				}
				if outcome == "exact" {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
				if outcome != "rejected config" {
					require.True(t, r.runCalled, "identity controls must reach actual proof parsing")
				}
				if outcome == "exact" && (route == "batch agent" || route == "interactive agent") {
					assert.Contains(t, r.gotSpec.Env, "ANTHROPIC_API_KEY=probe-secret")
					assert.Contains(t, r.gotSpec.Env, "ANTHROPIC_BASE_URL=https://gateway.example.test")
				}
			})
		}
	}
}

func TestModelProbeClaudeRestrictionsLeaveOrdinaryRunsUnchanged(t *testing.T) {
	r := &fakeRunner{lines: []string{fmt.Sprintf(`{"type":"result","result":"ok","modelUsage":{%q:{}}}`, proofModel)}}
	cfg := Config{BinaryPath: "claude", Runner: r, AllowedTools: []string{"Read"}}
	_, err := NewAgentAdapterForBinding(cfg, probeBinding(proofModel)).RunAgent(context.Background(), engine.AgentMission{Model: proofModel, Prompt: "ordinary", WorkDir: "/tmp/probe-workspace"})
	require.NoError(t, err)
	assert.Contains(t, r.gotSpec.Args, "--dangerously-skip-permissions")
	assert.NotContains(t, r.gotSpec.Args, "--safe-mode")
	assert.NotContains(t, r.gotSpec.Env, "DISABLE_UPDATES=1")
	_, err = NewChatAdapter(cfg).Chat(context.Background(), engine.ChatRequest{Model: proofModel, Prompt: "ordinary", WorkDir: "/tmp/probe-workspace", RequireExecutedModel: true})
	require.NoError(t, err)
	assert.NotContains(t, r.gotSpec.Args, "--safe-mode", "required identity alone must not select probe isolation")
	assert.NotContains(t, r.gotSpec.Env, "DISABLE_UPDATES=1")
}

// Batch llm_call and the spawn_workflow input preparer both reach a per-binding
// chat adapter with a PINNED binding and no RequireExecutedModel; agent_call
// reaches a per-binding agent adapter. A pin must keep the harness-evidence
// gate but never select the probe recipe: only SuppressTools may do that.
func TestModelProbeClaudeRestrictionsLeaveOrdinaryPinnedRunsUnchanged(t *testing.T) {
	probeOnlyArgs := []string{"--tools", "--strict-mcp-config", "--mcp-config", "--safe-mode", "--setting-sources", "--no-chrome"}
	probeOnlyEnv := []string{"DISABLE_UPDATES=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "CLAUDE_CODE_DISABLE_OFFICIAL_MARKETPLACE_AUTOINSTALL=1"}
	assertOrdinary := func(t *testing.T, spec RunSpec) {
		t.Helper()
		for _, flag := range probeOnlyArgs {
			assert.NotContains(t, spec.Args, flag, "a pin must not select probe isolation")
		}
		for _, env := range probeOnlyEnv {
			assert.NotContains(t, spec.Env, env, "a pin must not select the probe environment")
		}
		got, found := argValue(spec.Args, "--model")
		assert.True(t, found)
		assert.Equal(t, proofModel, got)
	}
	result := func(model string) []string {
		return []string{fmt.Sprintf(`{"type":"result","result":"ok","session_id":"s-1","modelUsage":{%q:{}}}`, model)}
	}
	chatBinding := provider.ResolvedBinding{Kind: provider.ProfileKindChat, Backend: "claude-cli", Model: proofModel, Pinned: true}
	ordinaryChat := engine.ChatRequest{Model: proofModel, Prompt: "ordinary", WorkDir: "/tmp/probe-workspace"}

	t.Run("llm_call and child input preparation keep the ordinary chat recipe", func(t *testing.T) {
		r := &fakeRunner{lines: result(proofModel)}
		_, err := NewChatAdapterForBinding(Config{BinaryPath: "claude", Runner: r}, chatBinding).Chat(context.Background(), ordinaryChat)
		require.NoError(t, err)
		assertOrdinary(t, r.gotSpec)
		got, _ := argValue(r.gotSpec.Args, "--allowedTools")
		assert.Empty(t, got, "an ordinary chat call stays tool-suppressed by allowlist")
	})

	t.Run("pinned stream chat keeps the ordinary chat recipe", func(t *testing.T) {
		r := &fakeRunner{lines: result(proofModel)}
		chunks, err := NewChatAdapterForBinding(Config{BinaryPath: "claude", Runner: r}, chatBinding).StreamChat(context.Background(), ordinaryChat)
		require.NoError(t, err)
		var done bool
		for c := range chunks {
			require.NoError(t, c.Error)
			done = done || c.Done
		}
		assert.True(t, done)
		assertOrdinary(t, r.gotSpec)
	})

	t.Run("a pinned chat still refuses a fallback without the request flag", func(t *testing.T) {
		r := &fakeRunner{lines: result("claude-other")}
		_, err := NewChatAdapterForBinding(Config{BinaryPath: "claude", Runner: r}, chatBinding).Chat(context.Background(), ordinaryChat)
		require.Error(t, err)
		assertOrdinary(t, r.gotSpec)
	})

	t.Run("a pinned chat with an empty request model never launches", func(t *testing.T) {
		r := &fakeRunner{lines: result(proofModel)}
		_, err := NewChatAdapterForBinding(Config{BinaryPath: "claude", Runner: r}, chatBinding).Chat(context.Background(), engine.ChatRequest{Prompt: "ordinary", WorkDir: "/tmp/probe-workspace"})
		require.Error(t, err)
		assert.False(t, r.runCalled)
	})

	t.Run("agent_call keeps the profile's tools and permissions", func(t *testing.T) {
		r := &fakeRunner{lines: result(proofModel)}
		cfg := Config{BinaryPath: "claude", Runner: r, AllowedTools: []string{"Read", "Grep"}}
		_, err := NewAgentAdapterForBinding(cfg, probeBinding(proofModel)).RunAgent(context.Background(), engine.AgentMission{Model: proofModel, Prompt: "ordinary", WorkDir: "/tmp/probe-workspace"})
		require.NoError(t, err)
		assertOrdinary(t, r.gotSpec)
		assert.Contains(t, r.gotSpec.Args, "--dangerously-skip-permissions")
		got, _ := argValue(r.gotSpec.Args, "--allowedTools")
		assert.Equal(t, "Read,Grep", got)
	})
}
