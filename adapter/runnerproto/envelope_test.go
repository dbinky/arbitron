// adapter/runnerproto/envelope_test.go
package runnerproto

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnvelope_RoundTripJobFrame(t *testing.T) {
	jobID := uuid.NewString()
	env, err := NewJobEnvelope(KindOutput, jobID, 2, 7, Output{Seq: 7, Token: "hello"})
	require.NoError(t, err)
	assert.Equal(t, ProtocolVersion, env.V)

	b, err := Encode(env)
	require.NoError(t, err)
	got, err := Decode(b)
	require.NoError(t, err)
	assert.Equal(t, env.Kind, got.Kind)
	assert.Equal(t, jobID, got.JobID)
	assert.Equal(t, int64(2), got.Generation)
	assert.Equal(t, uint64(7), got.Seq)

	var out Output
	require.NoError(t, DecodePayload(got, &out))
	assert.Equal(t, Output{Seq: 7, Token: "hello"}, out)
}

func TestEnvelope_RoundTripAdmissionFrame(t *testing.T) {
	env, err := NewEnvelope(KindHello, Hello{OS: "darwin", Arch: "arm64", ProtocolVersion: 1, RunnerVersion: "0.1.0",
		WorkspaceRoot: "/srv/arbitron/workspaces", ScratchRoot: "/Users/op/.arbitron-runner/scratch"})
	require.NoError(t, err)
	b, err := Encode(env)
	require.NoError(t, err)
	got, err := Decode(b)
	require.NoError(t, err)
	var h Hello
	require.NoError(t, DecodePayload(got, &h))
	assert.Equal(t, "darwin", h.OS)
	assert.Empty(t, got.JobID, "admission frames are not job scoped")
}

func TestDecode_RejectsMalformedFrames(t *testing.T) {
	job := uuid.NewString()
	cases := []struct {
		name string
		raw  string
		want error
	}{
		{"empty", ``, ErrMalformed},
		{"not json", `{"v":1,"kind":`, ErrMalformed},
		{"wrong type", `{"v":"one","kind":"hello"}`, ErrMalformed},
		{"missing version", `{"kind":"hello"}`, ErrMalformed},
		{"missing kind", `{"v":1}`, ErrMalformed},
		{"unknown kind", `{"v":1,"kind":"teleport"}`, ErrUnknownKind},
		{"job frame without job id", `{"v":1,"kind":"accept","generation":1}`, ErrMalformed},
		{"job frame with bad job id", `{"v":1,"kind":"result","jobId":"nope","generation":1}`, ErrMalformed},
		{"job frame generation zero", `{"v":1,"kind":"output","jobId":"` + job + `","generation":0,"seq":1}`, ErrMalformed},
		{"output seq zero", `{"v":1,"kind":"output","jobId":"` + job + `","generation":1}`, ErrMalformed},
		{"future version execution frame", `{"v":2,"kind":"dispatch","jobId":"` + job + `","generation":1}`, ErrUnsupportedVersion},
		{"future version heartbeat", `{"v":2,"kind":"heartbeat"}`, ErrUnsupportedVersion},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Decode([]byte(tc.raw))
			require.Error(t, err)
			assert.True(t, errors.Is(err, tc.want), "got %v want %v", err, tc.want)
		})
	}
}

// The minimal admission envelope stays readable across protocol versions so the
// server can explain a protocol refusal (design §3.4).
func TestDecode_FutureVersionAdmissionFramesStayReadable(t *testing.T) {
	for _, kind := range []string{KindHello, KindRefused} {
		raw := `{"v":9,"kind":"` + kind + `","payload":{"os":"darwin","protocolVersion":9,"runnerVersion":"9.0.0","newField":{"x":1}}}`
		env, err := Decode([]byte(raw))
		require.NoError(t, err, kind)
		assert.Equal(t, 9, env.V)
	}
	env, err := Decode([]byte(`{"v":9,"kind":"hello","payload":{"os":"darwin","protocolVersion":9,"runnerVersion":"9.0.0","workspaceRoot":"/w","scratchRoot":"/s","futureOnly":true}}`))
	require.NoError(t, err)
	var h Hello
	require.NoError(t, DecodePayload(env, &h), "unknown future fields are ignored, known ones decode")
	assert.Equal(t, 9, h.ProtocolVersion)
}

func TestDecode_RejectsOversizeFrames(t *testing.T) {
	big := `{"v":1,"kind":"heartbeat","payload":{"pad":"` + strings.Repeat("x", MaxFrameBytes) + `"}}`
	_, err := Decode([]byte(big))
	assert.ErrorIs(t, err, ErrFrameTooLarge)
}

func TestEncode_RejectsOversizeAndInvalid(t *testing.T) {
	env, err := NewJobEnvelope(KindOutput, uuid.NewString(), 1, 1, Output{Seq: 1, Token: strings.Repeat("y", MaxFrameBytes)})
	require.NoError(t, err)
	_, err = Encode(env)
	assert.ErrorIs(t, err, ErrFrameTooLarge)

	_, err = Encode(Envelope{V: 1})
	assert.ErrorIs(t, err, ErrMalformed, "missing kind")

	_, err = Encode(Envelope{V: 1, Kind: KindAck, JobID: "x", Generation: 1})
	assert.ErrorIs(t, err, ErrMalformed, "bad job id")
}

func TestEncode_DefaultsVersion(t *testing.T) {
	b, err := Encode(Envelope{Kind: KindHeartbeat, Payload: json.RawMessage(`{}`)})
	require.NoError(t, err)
	assert.Contains(t, string(b), `"v":1`)
}

func TestDecodePayload_RequiresPayloadAndValidation(t *testing.T) {
	err := DecodePayload(Envelope{V: 1, Kind: KindHello}, &Hello{})
	assert.ErrorIs(t, err, ErrMalformed, "missing payload")

	err = DecodePayload(Envelope{V: 1, Kind: KindHello, Payload: json.RawMessage(`{"os":""}`)}, &Hello{})
	assert.ErrorIs(t, err, ErrMalformed, "payload fails Validate")

	err = DecodePayload(Envelope{V: 1, Kind: KindHello, Payload: json.RawMessage(`[1,2]`)}, &Hello{})
	assert.ErrorIs(t, err, ErrMalformed, "payload not an object")
}
