package runnerproto

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestProbeResult_ObservationRoundTrip(t *testing.T) {
	for _, allowed := range []*bool{nil, boolPointer(false), boolPointer(true)} {
		result := ProbeResult{RequestID: uuid.NewString(), Source: WireSource{Kind: "openai_subscription", CredentialRef: "local-account", ObservationSeq: 7, SampleAgeMs: 12, Observation: &WireProbeObservation{Auth: "ok", Capacity: &WireCapacityObservation{State: "available", ModelScope: "gpt-5", OrdinaryUsageAllowed: allowed}}}}
		env, err := NewEnvelope(KindProbeResult, result)
		require.NoError(t, err)
		raw, err := Encode(env)
		require.NoError(t, err)
		decoded, err := Decode(raw)
		require.NoError(t, err)
		var got ProbeResult
		require.NoError(t, DecodePayload(decoded, &got))
		require.Equal(t, result, got)
	}
}
func boolPointer(v bool) *bool { return &v }

func TestPayloadTypes_ValidationContract(t *testing.T) {
	// Every declared payload has its own validation entry point, including
	// nested data and reserved descriptors.
	var _ = []Payload{&WireWindow{}, &WireTelemetry{}, &WireChatResult{}, &WireOutcome{}, &WireAgent{}, &WireProcess{}, &WireAgentResult{}, &WireProcessResult{}}
	raw, err := json.Marshal(Probe{RequestID: uuid.NewString(), SourceKind: "lunaroute", CredentialRef: "account"})
	require.NoError(t, err)
	var probe Probe
	require.NoError(t, json.Unmarshal(raw, &probe))
	require.NoError(t, probe.Validate())
	require.NotEmpty(t, probe.RequestID)
}
