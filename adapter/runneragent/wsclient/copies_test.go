package wsclient

import (
	"testing"

	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWsclient_HelloCarriesCopiesAndDeleteCopyDecodes(t *testing.T) {
	run := uuid.NewString()
	env, err := toEnvelope(runneragent.Outbound{Kind: runneragent.KindHello, Hello: &runneragent.Hello{OS: "darwin", Arch: "arm64", ProtocolVersion: 1, RunnerVersion: "1.0.0", WorkspaceRoot: "/w", ScratchRoot: "/s", Copies: []runneragent.HeldCopy{{RunID: run, HomeGeneration: 7}}}})
	require.NoError(t, err)
	var h runnerproto.Hello
	require.NoError(t, runnerproto.DecodePayload(env, &h))
	assert.Equal(t, []runnerproto.WireCopy{{RunID: run, HomeGeneration: 7}}, h.Copies)
	del, err := runnerproto.NewEnvelope(runnerproto.KindDeleteCopy, runnerproto.DeleteCopy{RunID: run, HomeGeneration: 6, Reason: "home_moved"})
	require.NoError(t, err)
	in, err := fromEnvelope(del)
	require.NoError(t, err)
	assert.Equal(t, runneragent.KindDeleteCopy, in.Kind)
	require.NotNil(t, in.DeleteCopy)
	assert.Equal(t, runneragent.DeleteCopy{RunID: run, HomeGeneration: 6, Reason: "home_moved"}, *in.DeleteCopy)
}
