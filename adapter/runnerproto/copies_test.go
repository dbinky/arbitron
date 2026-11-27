package runnerproto_test

import (
	"testing"

	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeleteCopy_RoundTripAndValidation(t *testing.T) {
	run := uuid.NewString()
	env, err := runnerproto.NewEnvelope(runnerproto.KindDeleteCopy, runnerproto.DeleteCopy{RunID: run, HomeGeneration: 3, Reason: "home_moved"})
	require.NoError(t, err)
	b, err := runnerproto.Encode(env)
	require.NoError(t, err)
	back, err := runnerproto.Decode(b)
	require.NoError(t, err, "delete_copy is a known kind")
	var d runnerproto.DeleteCopy
	require.NoError(t, runnerproto.DecodePayload(back, &d))
	assert.Equal(t, runnerproto.DeleteCopy{RunID: run, HomeGeneration: 3, Reason: "home_moved"}, d)
	require.Error(t, (&runnerproto.DeleteCopy{RunID: "nope", HomeGeneration: 1}).Validate())
	require.Error(t, (&runnerproto.DeleteCopy{RunID: run, HomeGeneration: -1}).Validate())
}

func TestHello_CopiesAreValidated(t *testing.T) {
	h := runnerproto.Hello{OS: "darwin", Arch: "arm64", ProtocolVersion: 1, RunnerVersion: "1.0.0", WorkspaceRoot: "/w", ScratchRoot: "/s", Copies: []runnerproto.WireCopy{{RunID: uuid.NewString(), HomeGeneration: 2}}}
	require.NoError(t, h.Validate())
	h.Copies = append(h.Copies, runnerproto.WireCopy{RunID: "x"})
	require.Error(t, h.Validate())
}
