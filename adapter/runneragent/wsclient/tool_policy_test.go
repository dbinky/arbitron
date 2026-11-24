package wsclient

import (
	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestToEnvelopeHelloCarriesToolPolicy(t *testing.T) {
	fp := strings.Repeat("a", 64)
	env := roundTrip(t, runneragent.Outbound{Kind: runneragent.KindHello, Hello: &runneragent.Hello{
		OS: "darwin", Arch: "arm64", ProtocolVersion: 1, RunnerVersion: "1.4.0",
		Sources: []runneragent.Source{{Kind: runner.CredOpenAISubscription, Families: []modelidentity.Family{"openai"}, Backends: []string{"codex"},
			Capabilities: []runner.ExecutionCapability{{Op: runner.OpAgent, Backend: "codex", Provider: "claude-cli", EndpointFingerprint: fp,
				Families: []modelidentity.Family{"openai"}, Models: []string{"gpt-6.1-sol"}, ToolPolicy: runner.ToolPolicyUnrestricted, ExecutedModel: true}}}},
	}})
	var h runnerproto.Hello
	require.NoError(t, runnerproto.DecodePayload(env, &h))
	require.Len(t, h.Sources, 1)
	require.Len(t, h.Sources[0].Capabilities, 1)
	assert.Equal(t, "unrestricted", h.Sources[0].Capabilities[0].ToolPolicy)
}
