// adapter/runnerproto/payloads_test.go
package runnerproto

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWireTelemetry_SampleAgeRoundTripsAndRejectsNegative(t *testing.T) {
	hb := Heartbeat{Telemetry: WireTelemetry{SampledAt: time.Unix(1700000000, 0).UTC(), SampleAgeMs: 1500}}
	require.NoError(t, hb.Validate())
	env, err := NewEnvelope(KindHeartbeat, hb)
	require.NoError(t, err)
	raw, err := Encode(env)
	require.NoError(t, err)
	got, err := Decode(raw)
	require.NoError(t, err)
	var back Heartbeat
	require.NoError(t, DecodePayload(got, &back))
	assert.Equal(t, int64(1500), back.Telemetry.SampleAgeMs)
	assert.Error(t, (&Heartbeat{Telemetry: WireTelemetry{SampleAgeMs: -1}}).Validate())
	env, err = NewEnvelope(KindHeartbeat, Heartbeat{Telemetry: WireTelemetry{SampleAgeMs: -1}})
	require.NoError(t, err)
	assert.ErrorIs(t, DecodePayload(env, &back), ErrMalformed)
}

func validHello() Hello {
	return Hello{OS: "darwin", Arch: "arm64", ProtocolVersion: 1, RunnerVersion: "0.1.0",
		WorkspaceRoot: "/srv/arbitron/workspaces", ScratchRoot: "/Users/op/.arbitron-runner/scratch",
		Sources: []WireSource{{Kind: "anthropic_subscription", Families: []string{"anthropic"}, Health: WireHealth{Auth: "ok", Capacity: "available"}}},
		Held:    []HeldJob{{JobID: uuid.NewString(), Generation: 1, State: "running", LastOutputSeq: 3}}}
}

func TestHello_Validate(t *testing.T) {
	h := validHello()
	require.NoError(t, h.Validate())

	bad := []func(*Hello){
		func(h *Hello) { h.OS = "" },
		func(h *Hello) { h.ProtocolVersion = 0 },
		func(h *Hello) { h.RunnerVersion = "" },
		func(h *Hello) { h.Sources = make([]WireSource, maxSources+1) },
		func(h *Hello) { h.Held[0].State = "paused" },
		func(h *Hello) { h.Held[0].JobID = "x" },
		func(h *Hello) { h.Sources[0].Kind = "server_connection" },
		func(h *Hello) { h.Sources[0].Health.Capacity = "infinite" },
	}
	for i, mutate := range bad {
		h := validHello()
		mutate(&h)
		assert.Error(t, h.Validate(), "case %d", i)
	}
}

func TestChallenge_ValidateSizes(t *testing.T) {
	nonce := make([]byte, 32)
	_, _ = rand.Read(nonce)
	c := Challenge{KeyID: uuid.NewString(), Nonce: base64.StdEncoding.EncodeToString(nonce)}
	require.NoError(t, c.Validate())
	c.Nonce = base64.StdEncoding.EncodeToString(nonce[:16])
	assert.Error(t, c.Validate())

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sig := ed25519.Sign(priv, []byte("m"))
	cr := ChallengeResponse{PublicKey: base64.StdEncoding.EncodeToString(pub), Signature: base64.StdEncoding.EncodeToString(sig)}
	require.NoError(t, cr.Validate())
	gotPub, gotSig, err := cr.Decoded()
	require.NoError(t, err)
	assert.Equal(t, []byte(pub), []byte(gotPub))
	assert.Equal(t, sig, gotSig)

	for _, bad := range []ChallengeResponse{
		{PublicKey: "%%%", Signature: cr.Signature},
		{PublicKey: base64.StdEncoding.EncodeToString(pub[:31]), Signature: cr.Signature},
		{PublicKey: cr.PublicKey, Signature: base64.StdEncoding.EncodeToString(sig[:63])},
	} {
		assert.Error(t, bad.Validate())
	}
}

func TestDispatch_Validate(t *testing.T) {
	d := Dispatch{Op: "chat", LeaseUntil: time.Now().Add(30 * time.Second),
		Chat: &WireChat{Model: "claude-opus-5-5", Prompt: "hi", CredentialKind: "anthropic_subscription"}}
	require.NoError(t, d.Validate())

	stream := d
	stream.Op = "stream"
	require.NoError(t, stream.Validate())

	cases := map[string]Dispatch{
		"agent op is phase 3":   {Op: "agent", Agent: &WireAgent{}},
		"process op is phase 3": {Op: "process", Process: &WireProcess{}},
		"chat without payload":  {Op: "chat"},
		"chat plus agent":       {Op: "chat", Chat: d.Chat, Agent: &WireAgent{}},
		"missing model":         {Op: "chat", Chat: &WireChat{Prompt: "p", CredentialKind: "lunaroute"}},
		"missing prompt":        {Op: "chat", Chat: &WireChat{Model: "m", CredentialKind: "lunaroute"}},
		"server credential":     {Op: "chat", Chat: &WireChat{Model: "m", Prompt: "p", CredentialKind: "server_connection"}},
		"local harness":         {Op: "chat", Chat: &WireChat{Model: "m", Prompt: "p", CredentialKind: "local_harness"}},
	}
	for name, c := range cases {
		c := c
		assert.Error(t, c.Validate(), name)
	}
}

func TestResultAndOutput_Validate(t *testing.T) {
	ok := Result{OK: true, Chat: &WireChatResult{Output: "x"}}
	require.NoError(t, ok.Validate())
	assert.Error(t, (&Result{OK: true}).Validate(), "ok result must carry exactly one payload")
	assert.Error(t, (&Result{OK: true, Chat: &WireChatResult{}, Agent: &WireAgentResult{}}).Validate())

	reset := time.Now().Add(time.Hour)
	fail := Result{OK: false, Outcome: WireOutcome{Class: "exhausted", ResetAt: &reset}, ErrorText: "usage limit"}
	require.NoError(t, fail.Validate())
	assert.Error(t, (&Result{OK: false, Outcome: WireOutcome{Class: "angry"}}).Validate())
	assert.Error(t, (&Result{OK: false, ErrorText: strings.Repeat("e", maxErrorText+1)}).Validate())

	assert.Error(t, (&Output{Seq: 0, Token: "t"}).Validate())
	require.NoError(t, (&Output{Seq: 1, Token: ""}).Validate())
}

func TestSmallPayloads_Validate(t *testing.T) {
	require.NoError(t, (&Heartbeat{}).Validate())
	assert.Error(t, (&Heartbeat{Jobs: []HeldJob{{JobID: "x", Generation: 1, State: "running"}}}).Validate())
	require.NoError(t, (&Ack{Seq: 0}).Validate())
	require.NoError(t, (&Admitted{RunnerID: "mac-a", HeartbeatSeconds: 5, ProtocolVersion: 1}).Validate())
	assert.Error(t, (&Admitted{RunnerID: "", HeartbeatSeconds: 5}).Validate())
	require.NoError(t, (&Refused{Code: "incompatible", Reason: "protocol v2 unsupported"}).Validate())
	assert.Error(t, (&Refused{}).Validate())
	require.NoError(t, (&Reject{Reason: "capability_missing"}).Validate())
	require.NoError(t, (&Fenced{}).Validate())
	require.NoError(t, (&Cancel{}).Validate())
	require.NoError(t, (&Probe{RequestID: "0f9d6a3e-8c1b-4b7e-9a2d-5e6f7a8b9c0d", SourceKind: "lunaroute"}).Validate())
	assert.Error(t, (&Probe{SourceKind: "server_connection"}).Validate())
	require.NoError(t, (&ProbeResult{RequestID: "0f9d6a3e-8c1b-4b7e-9a2d-5e6f7a8b9c0d", Source: WireSource{Kind: "lunaroute"}}).Validate())
	assert.Error(t, (&Probe{SourceKind: "lunaroute"}).Validate(), "an uncorrelated probe is malformed")
	assert.Error(t, (&ProbeResult{Source: WireSource{Kind: "lunaroute"}}).Validate(), "an uncorrelated probe result is malformed")
}
