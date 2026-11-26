package runneragent_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeMaintenancePuller struct {
	mu   sync.Mutex
	jobs []runneragent.WorkspaceJob
	err  error
}

func (p *fakeMaintenancePuller) Pull(_ context.Context, ws runneragent.WorkspaceJob) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.jobs = append(p.jobs, ws)
	return "/local/copy", p.err
}

func pullJob() runneragent.WorkspaceJob {
	return runneragent.WorkspaceJob{RunID: "r", WorkDir: "/srv/arbitron/workspaces/instances/r", HomeGeneration: 4, CheckpointVersion: 2,
		ManifestHash: "h", BaseRef: "r@2:h", FenceKey: "run:r", FenceOwner: "mac-studio", FenceGeneration: 3, FenceToken: "t",
		PullToken: "atr_x", PartBytes: 9}
}

func TestAgentPull_ReceiptIsSentAndReplayedUntilAcknowledged(t *testing.T) {
	puller := &fakeMaintenancePuller{}
	h := newHarness(t, 64, withPulls(puller))
	h.start()
	s1 := h.admitted()
	s1.push(runneragent.Inbound{Kind: runneragent.KindPullWorkspace, PullWorkspace: &runneragent.PullWorkspace{OperationID: "op-1", Job: pullJob()}})

	rc := s1.expect(t, runneragent.KindPullReceipt)
	require.NotNil(t, rc.PullReceipt)
	assert.Equal(t, runneragent.PullReceipt{OperationID: "op-1", RunID: "r", BaseRef: "r@2:h", FenceKey: "run:r", FenceOwner: "mac-studio",
		FenceGeneration: 3, FenceToken: "t", OK: true}, *rc.PullReceipt)
	assert.Equal(t, []runneragent.WorkspaceJob{pullJob()}, puller.jobs, "the exact scoped job reaches the syncer")

	mark := h.clock.mark()
	require.NoError(t, s1.Close()) // never acknowledged
	h.clock.awaitAndAdvance(t, mark, 500*time.Millisecond)
	s2 := h.session()
	hello := admit(t, s2, h.key.Public().(ed25519.PublicKey))
	require.Len(t, hello.PullReceipts, 1, "an unacknowledged receipt is replayed in hello")
	assert.Equal(t, "op-1", hello.PullReceipts[0].OperationID)

	s2.push(runneragent.Inbound{Kind: runneragent.KindPullReceiptAck, PullReceiptAck: "op-1"})
	mark = h.clock.mark()
	require.Eventually(t, func() bool { return len(h.agent.PendingPullReceipts()) == 0 }, 5*time.Second, time.Millisecond)
	require.NoError(t, s2.Close())
	h.clock.awaitAndAdvance(t, mark, 500*time.Millisecond)
	s3 := h.session()
	hello = admit(t, s3, h.key.Public().(ed25519.PublicKey))
	assert.Empty(t, hello.PullReceipts, "an acknowledged receipt is never replayed")
}

func TestAgentPull_FailuresAreReceiptsNotSilence(t *testing.T) {
	puller := &fakeMaintenancePuller{err: errors.New("manifest hash mismatch")}
	h := newHarness(t, 64, withPulls(puller))
	h.start()
	s := h.admitted()
	s.push(runneragent.Inbound{Kind: runneragent.KindPullWorkspace, PullWorkspace: &runneragent.PullWorkspace{OperationID: "op-2", Job: pullJob()}})
	rc := s.expect(t, runneragent.KindPullReceipt)
	assert.False(t, rc.PullReceipt.OK)
	assert.Contains(t, rc.PullReceipt.Error, "manifest hash mismatch")

	h2 := newHarness(t, 64) // no puller configured
	h2.start()
	s2 := h2.admitted()
	s2.push(runneragent.Inbound{Kind: runneragent.KindPullWorkspace, PullWorkspace: &runneragent.PullWorkspace{OperationID: "op-3", Job: pullJob()}})
	rc = s2.expect(t, runneragent.KindPullReceipt)
	assert.False(t, rc.PullReceipt.OK)
	assert.Contains(t, rc.PullReceipt.Error, "workspace transfers are not configured")
}
