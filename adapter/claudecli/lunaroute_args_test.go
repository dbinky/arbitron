package claudecli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lrResult(models ...string) string {
	usage := map[string]any{}
	for _, m := range models {
		usage[m] = map[string]any{}
	}
	b, _ := json.Marshal(map[string]any{"type": "result", "result": "done", "session_id": "lr-session", "modelUsage": usage})
	return string(b)
}
func TestLunaRouteSettingsIsolation(t *testing.T) {
	for _, entry := range []string{"agent", "probe", "chat", "stream", "stream-probe"} {
		t.Run(entry, func(t *testing.T) {
			calls := 0
			b := lrBinding("glm-5.3", lrTestKey, nil, &calls)
			fr := &fakeRunner{lines: []string{lrResult(b.Model)}}
			cfg := Config{Runner: fr, CapabilityRules: lrRules(lrModel(b.Model))}
			req := engine.ChatRequest{Model: b.Model, Prompt: "nonce", WorkDir: "/tmp/ws"}
			var err error
			switch entry {
			case "agent":
				_, err = NewAgentAdapterForBinding(cfg, b).RunAgent(context.Background(), engine.AgentMission{Model: b.Model, Prompt: req.Prompt, WorkDir: req.WorkDir})
			case "probe":
				_, _, err = NewAgentAdapterForBinding(cfg, b).ProbeExecutedModel(context.Background(), req.Prompt, req.WorkDir)
			case "chat":
				_, err = NewStreamAgentAdapterForBinding(cfg, b).Chat(context.Background(), req)
			default:
				req.SuppressTools = entry == "stream-probe"
				var ch <-chan engine.ChatChunk
				ch, err = NewStreamAgentAdapterForBinding(cfg, b).StreamChat(context.Background(), req)
				require.NoError(t, err)
				done := false
				for c := range ch {
					require.NoError(t, c.Error)
					done = done || c.Done
				}
				require.True(t, done)
			}
			require.NoError(t, err)
			n := 0
			for i, s := range fr.gotSpec.Args {
				if s == "--setting-sources" {
					n++
					require.Less(t, i+1, len(fr.gotSpec.Args))
					assert.Equal(t, "", fr.gotSpec.Args[i+1])
				}
			}
			assert.Equal(t, 1, n)
			assert.Equal(t, "nonce", fr.gotSpec.Stdin)
			assert.Equal(t, "/tmp/ws", fr.gotSpec.Dir)
		})
	}
}
func TestLunaRouteNonLunaRouteArgsGolden(t *testing.T) {
	t.Setenv("ANTHROPIC_MODEL", "glm-5.3")
	t.Setenv("CLAUDE_CODE_GATEWAY_HINT_HEADERS", "1")
	fr := &fakeRunner{lines: []string{lrResult("claude-sonnet-4-6")}}
	_, err := NewAgentAdapter(Config{Runner: fr}).RunAgent(context.Background(), engine.AgentMission{Model: "claude-sonnet-4-6", Prompt: "p", WorkDir: "/tmp/ws", ToolScope: []string{"Read"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"--print", "--verbose", "--output-format", "stream-json", "--dangerously-skip-permissions", "--allowedTools", "Read", "--model", "claude-sonnet-4-6"}, fr.gotSpec.Args)
}
func TestLunaRouteAdapterPreflight(t *testing.T) {
	for _, entry := range []string{"agent", "probe", "chat", "stream"} {
		for _, tc := range []struct {
			name    string
			mut     func(*provider.ResolvedBinding, *provider.LunaRouteModel)
			mission string
			want    error
		}{
			{"mismatch", nil, "deepseek-4.1-flash", errModelExecutionEvidence},
			{"unreviewed", func(b *provider.ResolvedBinding, m *provider.LunaRouteModel) { b.Model = "glm-999.9" }, "", provider.ErrLunaRouteModelSpelling},
			{"revoked", func(b *provider.ResolvedBinding, m *provider.LunaRouteModel) { b.Model = "claude-sonnet-5-lr1" }, "", provider.ErrLunaRouteModelSpelling},
			{"missing limit", func(b *provider.ResolvedBinding, m *provider.LunaRouteModel) { m.OutputTokens = provider.LimitFact{} }, "", provider.ErrLunaRouteLimitsUnknown},
			{"task", func(b *provider.ResolvedBinding, m *provider.LunaRouteModel) {
				m.Tasks = map[provider.ProfileKind]provider.SupportFact{"agent": {Support: provider.SupportUnsupported, Evidence: "ev#task"}}
			}, "", provider.ErrLunaRouteIncompatible},
			{"backend", func(b *provider.ResolvedBinding, m *provider.LunaRouteModel) {
				m.Backends = map[string]provider.SupportFact{"claude-code": {Support: provider.SupportUnsupported, Evidence: "ev#backend"}}
			}, "", provider.ErrLunaRouteIncompatible},
			{"effort", func(b *provider.ResolvedBinding, m *provider.LunaRouteModel) { b.Effort = provider.EffortMedium }, "", provider.ErrLunaRouteAxisUnsupported},
			{"context", func(b *provider.ResolvedBinding, m *provider.LunaRouteModel) { b.ContextWindow = "invalid" }, "", provider.ErrLunaRouteAxisUnsupported},
		} {
			// ProbeExecutedModel has no caller-supplied model to mismatch.
			if entry == "probe" && tc.name == "mismatch" {
				continue
			}
			t.Run(entry+"/"+tc.name, func(t *testing.T) {
				calls := 0
				b := lrBinding("glm-5.3", lrTestKey, nil, &calls)
				m := lrModel(b.Model)
				if tc.mut != nil {
					tc.mut(&b, &m)
				}
				fr := &fakeRunner{lines: []string{lrResult(b.Model)}}
				cfg := Config{Runner: fr, CapabilityRules: lrRules(m)}
				var err error
				req := engine.ChatRequest{Model: tc.mission, Prompt: "nonce", WorkDir: "/tmp/ws"}
				switch entry {
				case "agent":
					_, err = NewAgentAdapterForBinding(cfg, b).RunAgent(context.Background(), engine.AgentMission{Model: tc.mission, Prompt: "nonce", WorkDir: "/tmp/ws"})
				case "probe":
					_, _, err = NewAgentAdapterForBinding(cfg, b).ProbeExecutedModel(context.Background(), "nonce", "/tmp/ws")
				case "chat":
					_, err = NewStreamAgentAdapterForBinding(cfg, b).Chat(context.Background(), req)
				default:
					_, err = NewStreamAgentAdapterForBinding(cfg, b).StreamChat(context.Background(), req)
				}
				require.ErrorIs(t, err, tc.want)
				assert.ErrorIs(t, err, provider.ErrLunaRouteLocalFailure)
				assert.Zero(t, calls)
				assert.False(t, fr.runCalled)
			})
		}
	}
}
func TestLunaRouteModelUsageEveryEntry(t *testing.T) {
	for _, entry := range []string{"agent", "probe", "chat", "stream"} {
		for _, pinned := range []bool{false, true} {
			for _, models := range [][]string{nil, {"glm-5.3", "claude-haiku-4-5"}, {"glm-5.3-flash"}, {"glm-5.3[1m]"}, {"glm-5.3 ", "glm-5.3"}, {"glm-5.3", "claude-" + lrTestKey}, {"glm-5.3"}} {
				t.Run(entry+"/"+strings.Join(models, ",")+"/"+map[bool]string{true: "pinned", false: "legacy"}[pinned], func(t *testing.T) {
					calls := 0
					b := lrBinding("glm-5.3", lrTestKey, nil, &calls)
					b.Pinned = pinned
					fr := &fakeRunner{lines: []string{lrResult(models...)}}
					cfg := Config{Runner: fr, CapabilityRules: lrRules(lrModel(b.Model))}
					var err error
					req := engine.ChatRequest{Model: b.Model, WorkDir: "/tmp/ws", Prompt: "nonce"}
					switch entry {
					case "agent":
						_, err = NewAgentAdapterForBinding(cfg, b).RunAgent(context.Background(), engine.AgentMission{Model: b.Model, WorkDir: req.WorkDir, Prompt: req.Prompt})
					case "probe":
						_, _, err = NewAgentAdapterForBinding(cfg, b).ProbeExecutedModel(context.Background(), req.Prompt, req.WorkDir)
					case "chat":
						_, err = NewStreamAgentAdapterForBinding(cfg, b).Chat(context.Background(), req)
					default:
						var ch <-chan engine.ChatChunk
						ch, err = NewStreamAgentAdapterForBinding(cfg, b).StreamChat(context.Background(), req)
						require.NoError(t, err)
						done := false
						for c := range ch {
							if c.Error != nil {
								err = c.Error
							}
							done = done || c.Done
						}
						if len(models) != 1 || models[0] != b.Model {
							assert.False(t, done)
						}
					}
					if len(models) == 1 && models[0] == b.Model {
						require.NoError(t, err)
					} else {
						require.Error(t, err)
						assert.NotContains(t, err.Error(), lrTestKey)
					}
				})
			}
		}
	}
}
func TestLunaRouteClaudeErrorsAreSafeUnpinned(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	boom := errors.New("failure " + lrTestKey)
	for _, fr := range []*fakeRunner{{startErr: boom}, {waitErr: boom}, {lines: []string{`{"type":"assistant","message":"` + lrTestKey + `"}`}}, {lines: []string{`{"type":"result","is_error":true,"result":"` + lrTestKey + `"}`}}, {lines: []string{lrResult("glm-5.3"), lrResult("glm-5.3")}}} {
		calls := 0
		b := lrBinding("glm-5.3", lrTestKey, nil, &calls)
		res, err := NewAgentAdapterForBinding(Config{Runner: fr, CapabilityRules: lrRules(lrModel(b.Model))}, b).RunAgent(context.Background(), engine.AgentMission{WorkDir: "/tmp/ws", Prompt: "nonce"})
		require.Error(t, err)
		assert.Nil(t, res)
		assert.NotContains(t, err.Error(), lrTestKey)
		assert.NotContains(t, logs.String(), lrTestKey)
		if fr.startErr != nil || fr.waitErr != nil {
			assert.ErrorIs(t, err, boom)
		}
	}
}
func TestLunaRouteCollectorPolicyBeforeAcceptance(t *testing.T) {
	policy := collectPolicy{sanitize: true, acceptModels: func(want string, used []string) bool {
		return want == "opaque-reviewed-fixture" && len(used) == 1 && used[0] == "claude-sonnet-5-6"
	}}
	result, _, err := collectResultWithPolicy(context.Background(), &fakeRunner{lines: []string{lrResult("claude-sonnet-5-6")}}, RunSpec{}, "opaque-reviewed-fixture", policy)
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-sonnet-5-6"}, result.ModelUsage)
	_, _, err = collectResultWithPolicy(context.Background(), &fakeRunner{lines: []string{lrResult("claude-sonnet-5-6", "foreign-helper")}}, RunSpec{}, "opaque-reviewed-fixture", policy)
	require.ErrorIs(t, err, errModelExecutionEvidence)
}
