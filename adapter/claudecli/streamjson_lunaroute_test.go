package claudecli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLunaRouteTerminalEvidenceProducer(t *testing.T) {
	ev, ok, err := parseEvent([]byte(lrResult("glm-5.3-flash", "glm-5.3")))
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, []engine.ModelEvidence{{Model: "glm-5.3", Source: "claude_model_usage"}, {Model: "glm-5.3-flash", Source: "claude_model_usage"}}, ev.modelEvidence())
	assert.False(t, lunaRouteModelsAccepted("glm-5.3", ev.ModelUsage), "all billed models, not just the main model")
	for _, reported := range []string{"", "glm-5.3\x00", "glm-5.3\n", "glm-5.3\u0085", strings.Repeat("x", 257), "claude-" + lrTestKey, "glm-5.3[1m]"} {
		assert.False(t, lunaRouteModelsAccepted("glm-5.3", []string{reported}))
	}
	assert.True(t, lunaRouteModelsAccepted("glm-5.3", []string{"glm-5.3"}))
	assert.False(t, lunaRouteModelsAccepted("claude-sonnet-5-lr1", []string{"claude-sonnet-5-6"}), "R3 revoked this alias")
}

func TestLunaRouteCollectorSanitizesWithoutWantModel(t *testing.T) {
	boom := errors.New("process " + lrTestKey)
	for _, fr := range []*fakeRunner{{startErr: boom}, {waitErr: boom}, {lines: []string{`{"type":"result","is_error":true,"result":"` + lrTestKey + `"}`}}, {lines: []string{`{"type":"assistant","message":"` + lrTestKey + `"}`}}} {
		_, _, err := collectResultWithPolicy(context.Background(), fr, RunSpec{}, "", collectPolicy{sanitize: true})
		require.Error(t, err)
		assert.NotContains(t, err.Error(), lrTestKey)
		if fr.startErr != nil || fr.waitErr != nil {
			assert.ErrorIs(t, err, boom)
		}
	}
}

func TestLunaRouteStreamingErrorsAreSafe(t *testing.T) {
	boom := errors.New("process " + lrTestKey)
	for _, fr := range []*fakeRunner{{startErr: boom}, {waitErr: boom}, {lines: []string{`{"type":"result","is_error":true,"result":"` + lrTestKey + `"}`}}, {lines: []string{`{"type":"assistant","message":"` + lrTestKey + `"}`}}, {lines: []string{lrResult("glm-5.3"), lrResult("glm-5.3")}}} {
		calls := 0
		b := lrBinding("glm-5.3", lrTestKey, nil, &calls)
		ch, err := NewStreamAgentAdapterForBinding(Config{Runner: fr, CapabilityRules: lrRules(lrModel(b.Model))}, b).StreamChat(context.Background(), engine.ChatRequest{WorkDir: "/tmp/ws", Prompt: "nonce"})
		if err == nil {
			for chunk := range ch {
				assert.False(t, chunk.Done)
				if chunk.Error != nil {
					err = chunk.Error
				}
			}
		}
		require.Error(t, err)
		assert.NotContains(t, err.Error(), lrTestKey)
		if fr.startErr != nil || fr.waitErr != nil {
			assert.ErrorIs(t, err, boom)
		}
	}
}
