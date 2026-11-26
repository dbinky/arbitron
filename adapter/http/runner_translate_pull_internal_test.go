package http

import (
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunnerTranslate_PullMessagesRoundTrip(t *testing.T) {
	run := uuid.New()
	ref := runner.CheckpointRef{RunID: run, Version: 2, ManifestHash: strings.Repeat("c", 64)}
	claim := engine.ResourceFenceRef{Key: "run:" + run.String(), Owner: "mac-2", Generation: 3, Token: uuid.New()}

	e, err := fromOutbound(runner.Outbound{Kind: runner.MsgPullWorkspace, PullWorkspace: &runner.PullWorkspacePayload{OperationID: "op-1", RunID: run,
		WorkDir: "/srv/ws/instances/1", HomeGeneration: 4, Ref: ref, Resource: claim, PullToken: "atr_x", PartBytes: 8 << 20}}, "mac-2", 5)
	require.NoError(t, err)
	var pw runnerproto.PullWorkspace
	require.NoError(t, runnerproto.DecodePayload(e, &pw))
	assert.Equal(t, ref.String(), pw.BaseRef)
	assert.Equal(t, ref.ManifestHash, pw.ManifestHash)
	assert.Equal(t, claim.Token.String(), pw.FenceToken)

	e, err = fromOutbound(runner.Outbound{Kind: runner.MsgPullReceiptAck, PullReceiptAck: "op-1"}, "mac-2", 5)
	require.NoError(t, err)
	assert.Equal(t, runnerproto.KindPullReceiptAck, e.Kind)

	wire := runnerproto.WirePullReceipt{OperationID: "op-1", RunID: run.String(), BaseRef: ref.String(), FenceKey: claim.Key, FenceOwner: claim.Owner,
		FenceGeneration: claim.Generation, FenceToken: claim.Token.String(), OK: true}
	env, err := runnerproto.NewEnvelope(runnerproto.KindPullReceipt, wire)
	require.NoError(t, err)
	in, err := toInbound(env)
	require.NoError(t, err)
	require.NotNil(t, in.PullReceipt)
	assert.Equal(t, runner.PullReceiptPayload{OperationID: "op-1", RunID: run, Ref: ref, Resource: claim, OK: true}, *in.PullReceipt)

	info, err := helloInfo(runnerproto.Hello{OS: "darwin", ProtocolVersion: 1, RunnerVersion: "0.2.0", PullReceipts: []runnerproto.WirePullReceipt{wire}})
	require.NoError(t, err)
	require.Len(t, info.PullReceipts, 1)
	assert.Equal(t, ref, info.PullReceipts[0].Ref)
}
