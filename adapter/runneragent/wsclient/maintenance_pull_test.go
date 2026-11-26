package wsclient

import (
	"testing"

	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTranslate_PullMessages(t *testing.T) {
	pw := runnerproto.PullWorkspace{OperationID: "op-1", RunID: "r", WorkDir: "/srv/ws/instances/1", HomeGeneration: 4, CheckpointVersion: 2,
		ManifestHash: "h", BaseRef: "r@2:h", FenceKey: "run:r", FenceOwner: "mac-2", FenceGeneration: 3, FenceToken: "t", PullToken: "atr_x", PartBytes: 9}
	env, err := runnerproto.NewEnvelope(runnerproto.KindPullWorkspace, pw)
	require.NoError(t, err)
	in, err := fromEnvelope(env)
	require.NoError(t, err)
	require.NotNil(t, in.PullWorkspace)
	assert.Equal(t, "op-1", in.PullWorkspace.OperationID)
	assert.Equal(t, runneragent.WorkspaceJob{RunID: "r", WorkDir: "/srv/ws/instances/1", HomeGeneration: 4, CheckpointVersion: 2, ManifestHash: "h",
		BaseRef: "r@2:h", FenceKey: "run:r", FenceOwner: "mac-2", FenceGeneration: 3, FenceToken: "t", PullToken: "atr_x", PartBytes: 9}, in.PullWorkspace.Job)

	env, err = runnerproto.NewEnvelope(runnerproto.KindPullReceiptAck, runnerproto.PullReceiptAck{OperationID: "op-1"})
	require.NoError(t, err)
	in, err = fromEnvelope(env)
	require.NoError(t, err)
	assert.Equal(t, "op-1", in.PullReceiptAck)

	rc := runneragent.PullReceipt{OperationID: "op-1", RunID: "r", BaseRef: "r@2:h", FenceKey: "run:r", FenceOwner: "mac-2", FenceGeneration: 3, FenceToken: "t", OK: true}
	out, err := toEnvelope(runneragent.Outbound{Kind: runneragent.KindPullReceipt, PullReceipt: &rc})
	require.NoError(t, err)
	var wire runnerproto.WirePullReceipt
	require.NoError(t, runnerproto.DecodePayload(out, &wire))
	assert.Equal(t, wirePullReceipt(rc), wire)

	out, err = toEnvelope(runneragent.Outbound{Kind: runneragent.KindHello, Hello: &runneragent.Hello{OS: "darwin", ProtocolVersion: 1, RunnerVersion: "0.2.0",
		PullReceipts: []runneragent.PullReceipt{rc}}})
	require.NoError(t, err)
	var h runnerproto.Hello
	require.NoError(t, runnerproto.DecodePayload(out, &h))
	assert.Equal(t, []runnerproto.WirePullReceipt{wirePullReceipt(rc)}, h.PullReceipts)
}
