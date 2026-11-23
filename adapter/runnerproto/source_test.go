package runnerproto

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func provenSource() WireSource {
	return WireSource{Kind: "lunaroute", Capabilities: []ExecutionCapability{{Op: "chat", Provider: "lunaroute", EndpointFingerprint: strings.Repeat("a", 64), Families: []string{"anthropic"}, Models: []string{"claude-opus-5-5"}, ContextWindows: []string{"1m"}, Efforts: []string{"high"}, Tools: []string{"Read"}, ExecutedModel: true}}}
}

func TestWireSource_CapabilityEvidenceRoundTrip(t *testing.T) {
	source := provenSource()
	env, err := NewEnvelope(KindHello, Hello{OS: "linux", ProtocolVersion: 1, RunnerVersion: "1", Sources: []WireSource{source}})
	require.NoError(t, err)
	raw, err := Encode(env)
	require.NoError(t, err)
	decoded, err := Decode(raw)
	require.NoError(t, err)
	var hello Hello
	require.NoError(t, DecodePayload(decoded, &hello))
	require.Equal(t, source, hello.Sources[0])
}
func TestWireSource_UnavailableReasonRoundTrip(t *testing.T) {
	source := provenSource()
	source.UnavailableReason = "unsupported harness version"
	raw, err := json.Marshal(source)
	require.NoError(t, err)
	var got WireSource
	require.NoError(t, json.Unmarshal(raw, &got))
	require.NoError(t, got.Validate())
	require.Equal(t, source, got)
}
func TestWireSource_RejectsMalformedCapabilities(t *testing.T) {
	for _, mutate := range []func(*ExecutionCapability){
		func(c *ExecutionCapability) { c.Op = "teleport" },
		func(c *ExecutionCapability) { c.Provider = "" },
		func(c *ExecutionCapability) { c.EndpointFingerprint = "https://secret.example" },
		func(c *ExecutionCapability) { c.Families = nil },
		func(c *ExecutionCapability) { c.Families = []string{"made-up"} },
		func(c *ExecutionCapability) { c.Models = nil },
		func(c *ExecutionCapability) { c.Tools = make([]string, 257) },
	} {
		s := provenSource()
		mutate(&s.Capabilities[0])
		require.Error(t, s.Validate())
	}
	s := provenSource()
	s.Capabilities = nil
	require.NoError(t, s.Validate(), "empty evidence is valid transport data, not proof of eligibility")
}
