package runnerproto

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validPullReceipt() WirePullReceipt {
	return WirePullReceipt{OperationID: "pull:run:x@3/mac-2", RunID: "4f1c2b3a-0000-4000-8000-000000000001",
		BaseRef:  "4f1c2b3a-0000-4000-8000-000000000001@2:" + strings.Repeat("c", 64),
		FenceKey: "run:4f1c2b3a-0000-4000-8000-000000000001", FenceOwner: "mac-2", FenceGeneration: 3,
		FenceToken: "9b2e7c1d-0000-4000-8000-000000000002", OK: true}
}

func TestPullMessages_KindsAreKnownAndPayloadsValidate(t *testing.T) {
	for _, k := range []string{KindPullWorkspace, KindPullReceipt, KindPullReceiptAck} {
		assert.True(t, knownKinds[k], k)
	}
	rc := validPullReceipt()
	require.NoError(t, rc.Validate())
	bad := rc
	bad.OperationID = ""
	require.Error(t, bad.Validate())
	bad = rc
	bad.OK, bad.Error = false, ""
	require.Error(t, bad.Validate(), "a failed receipt names its error")

	pw := PullWorkspace{OperationID: rc.OperationID, RunID: rc.RunID, WorkDir: "/srv/ws/instances/1", HomeGeneration: 4, CheckpointVersion: 2,
		ManifestHash: strings.Repeat("c", 64), BaseRef: rc.BaseRef, FenceKey: rc.FenceKey, FenceOwner: rc.FenceOwner,
		FenceGeneration: rc.FenceGeneration, FenceToken: rc.FenceToken, PullToken: "atr_x", PartBytes: 8 << 20}
	require.NoError(t, pw.Validate())
	pw.PullToken = ""
	require.Error(t, pw.Validate())
	require.NoError(t, (&PullReceiptAck{OperationID: rc.OperationID}).Validate())
	require.Error(t, (&PullReceiptAck{}).Validate())

	h := validHello()
	h.PullReceipts = make([]WirePullReceipt, maxPullReceipts+1)
	require.Error(t, h.Validate())
}
