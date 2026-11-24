package http

import (
	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestDispatchToWire_CopiesRoute(t *testing.T) {
	fp := strings.Repeat("a", 64)
	got := dispatchToWire(runner.DispatchPayload{Op: runner.OpChat, Chat: &runner.ChatDispatch{Model: "m", Prompt: "p", CredentialKind: runner.CredAnthropicSubscription, Provider: "anthropic", EndpointFingerprint: fp}})
	require.NotNil(t, got.Chat)
	assert.Equal(t, "anthropic", got.Chat.Provider)
	assert.Equal(t, fp, got.Chat.EndpointFingerprint)
}
func TestHeartbeatInfo_CopiesTelemetryAge(t *testing.T) {
	got, err := heartbeatInfo(runnerproto.Heartbeat{Telemetry: runnerproto.WireTelemetry{SampleAgeMs: 1500}})
	require.NoError(t, err)
	assert.Equal(t, int64(1500), got.TelemetryAgeMs)
}
