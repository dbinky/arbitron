package wsclient

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/runneragent"
)

func wireImport() runnerproto.Dispatch {
	return runnerproto.Dispatch{Op: runnerproto.OpSessionImport, Session: &runnerproto.WireSessionOp{Backend: "claude-code", SessionID: "s-old",
		WorkDir: "/srv/arbitron/workspaces/r1", TransferToken: "rtt_x", Bundle: &runnerproto.WireSessionBundle{Backend: "claude-code",
			SessionID: "s-old", HarnessVersion: "2.1.3", OriginWorkDir: "/srv/arbitron/workspaces/r1", Checksum: strings.Repeat("ab", 32), Size: 9}}}
}

func TestFromEnvelopeSessionKindsBecomeDispatches(t *testing.T) {
	in, err := fromEnvelope(mustEnv(t, runnerproto.KindSessionImport, tJob, 4, wireImport()))
	require.NoError(t, err)
	assert.Equal(t, runneragent.KindDispatch, in.Kind, "session frames run through the agent's ordinary dispatch path")
	require.NotNil(t, in.Dispatch)
	assert.Nil(t, in.Dispatch.Chat)
	assert.Equal(t, runneragent.OpSessionImport, in.Dispatch.Op)
	assert.Equal(t, &runneragent.SessionJob{Backend: "claude-code", SessionID: "s-old", WorkDir: "/srv/arbitron/workspaces/r1", TransferToken: "rtt_x",
		Bundle: &runneragent.SessionBundleMeta{Backend: "claude-code", SessionID: "s-old", HarnessVersion: "2.1.3",
			OriginWorkDir: "/srv/arbitron/workspaces/r1", Checksum: strings.Repeat("ab", 32), Size: 9}}, in.Dispatch.Session)

	_, err = fromEnvelope(mustEnv(t, runnerproto.KindDispatch, tJob, 4, wireImport()))
	assert.ErrorIs(t, err, runnerproto.ErrMalformed, "session ops never ride a plain dispatch frame")
}

func TestFromEnvelopeStreamCarriesSessionCheckpointAndResource(t *testing.T) {
	res := &runnerproto.WireResource{ID: "0f8fad5b-d9cb-469f-a165-70867728950e", Kind: "resolution", WorkDir: "/srv/arbitron/workspaces/.runner-resources/resolutions/0f8f",
		FenceKey: "resolution:0f8f", FenceOwner: "mac-a", FenceGeneration: 2, FenceToken: "7c9e6679-7425-40de-944b-e07fc1f90ae7", CheckpointRef: "0f8f@3:x"}
	in, err := fromEnvelope(mustEnv(t, runnerproto.KindDispatch, tJob, 2, runnerproto.Dispatch{Op: "stream", Resource: res, Chat: &runnerproto.WireChat{
		Model: "claude-opus-5-5", Prompt: "next", CredentialKind: "anthropic_subscription", Stream: true, SessionID: "s1",
		Backend: "claude-code", WorkDir: "/srv/arbitron/workspaces/r1", ProfileKind: "agent", NativeCheckpoint: true, SessionExportToken: "rtt_session"}}))
	require.NoError(t, err)
	c := in.Dispatch.Chat
	require.NotNil(t, c)
	assert.Equal(t, "/srv/arbitron/workspaces/r1", c.WorkDir)
	assert.Equal(t, "agent", c.ProfileKind)
	assert.True(t, c.NativeCheckpoint)
	assert.Equal(t, "rtt_session", c.SessionExportToken)
	assert.Equal(t, &runneragent.ResourceJob{ID: res.ID, Kind: "resolution", WorkDir: res.WorkDir, FenceKey: res.FenceKey,
		FenceOwner: "mac-a", FenceGeneration: 2, FenceToken: res.FenceToken, CheckpointRef: "0f8f@3:x"}, in.Dispatch.Resource)
}

func TestToEnvelopeSessionResults(t *testing.T) {
	env, err := toEnvelope(runneragent.Outbound{Kind: runneragent.KindResult, JobID: tJob, Generation: 4, Result: &runneragent.Result{
		OK: true, Session: &runneragent.SessionResult{SessionID: "s-new"}}})
	require.NoError(t, err)
	var r runnerproto.Result
	require.NoError(t, runnerproto.DecodePayload(env, &r))
	assert.Equal(t, &runnerproto.WireSessionResult{SessionID: "s-new"}, r.Session)

	sum := strings.Repeat("cd", 32)
	env, err = toEnvelope(runneragent.Outbound{Kind: runneragent.KindResult, JobID: tJob, Generation: 5, Result: &runneragent.Result{
		OK: true, Chat: &runneragent.ChatResult{Output: "done", SessionID: "s1", Session: &runneragent.SessionBundleMeta{Backend: "claude-code",
			SessionID: "s1", HarnessVersion: "2.1.3", OriginWorkDir: "/srv/arbitron/workspaces/r1", Checksum: sum, Size: 40}}}})
	require.NoError(t, err)
	r = runnerproto.Result{}
	require.NoError(t, runnerproto.DecodePayload(env, &r))
	assert.Equal(t, &runnerproto.WireSessionBundle{Backend: "claude-code", SessionID: "s1", HarnessVersion: "2.1.3",
		OriginWorkDir: "/srv/arbitron/workspaces/r1", Checksum: sum, Size: 40}, r.Chat.Session, "the in-turn export descriptor rides the stream result")
}
