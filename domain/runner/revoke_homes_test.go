// domain/runner/revoke_homes_test.go
package runner_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRevocationHomeReleaser_ReleasesHomesAndRecoversSteps(t *testing.T) {
	ctx := context.Background()
	r := newDrainRig(t)
	r.online(t, "mac-1")
	run, a := r.homedRun(t)
	_, err := r.leases.ClaimJob(ctx, "run:"+run.String(), runner.JobHolder(a.Job), a.Fence)
	require.NoError(t, err)
	var revoker runner.SessionRevoker = runner.NewRevocationHomeReleaser(r.hub, r.repo, r.leases)

	require.NoError(t, revoker.RevokeRunner(ctx, "mac-1"))
	h, err := r.repo.GetHome(ctx, run)
	require.NoError(t, err)
	assert.Equal(t, runner.ServerHome, h.RunnerID, "the committed server checkpoint becomes the home")
	require.ErrorIs(t, r.pool.Ledger.FenceCheck(ctx, a.Fence), runner.ErrFenced, "the revoked runner's step is recovered, its results fenced")
	rn, _, _ := r.pool.Registry.GetRunner(ctx, "mac-1")
	assert.Equal(t, runner.LivenessLost, rn.Liveness)
	assert.False(t, r.claimActive(t, run), "the takeover claim is released after the home is released")
	hist, err := r.repo.History(ctx, run)
	require.NoError(t, err)
	assert.Equal(t, "revoke:mac-1", hist[len(hist)-1].OperationID)
}

type failingRevoker struct{ called bool }

func (f *failingRevoker) RevokeRunner(context.Context, runner.RunnerID) error {
	f.called = true
	return errors.New("hub down")
}

type nopRevoker struct{}

func (nopRevoker) RevokeRunner(context.Context, runner.RunnerID) error { return nil }

func TestRevocationHomeReleaser_HubFailureReleasesNothing(t *testing.T) {
	ctx := context.Background()
	r := newDrainRig(t)
	r.online(t, "mac-1")
	run, a := r.homedRun(t)
	_, err := r.leases.ClaimJob(ctx, "run:"+run.String(), runner.JobHolder(a.Job), a.Fence)
	require.NoError(t, err)
	inner := &failingRevoker{}

	err = runner.NewRevocationHomeReleaser(inner, r.repo, r.leases).RevokeRunner(ctx, "mac-1")
	require.Error(t, err)
	assert.True(t, inner.called)
	h, _ := r.repo.GetHome(ctx, run)
	assert.Equal(t, runner.RunnerID("mac-1"), h.RunnerID, "no home is released before the runner's jobs are fenced")
	assert.True(t, r.claimActive(t, run), "the writer's claim is untouched")
}

func TestRevocationHomeReleaser_UnprovenTakeoverKeepsTheHome(t *testing.T) {
	ctx := context.Background()
	r := newDrainRig(t)
	r.online(t, "mac-1")
	run, a := r.homedRun(t)
	_, err := r.leases.ClaimJob(ctx, "run:"+run.String(), runner.JobHolder(a.Job), a.Fence)
	require.NoError(t, err)

	err = runner.NewRevocationHomeReleaser(nopRevoker{}, r.repo, r.leases).RevokeRunner(ctx, "mac-1") // nothing fenced the job
	require.ErrorIs(t, err, runner.ErrSupersedeUnproven)
	h, _ := r.repo.GetHome(ctx, run)
	assert.Equal(t, runner.RunnerID("mac-1"), h.RunnerID, "a failed proof leaves the home owned and visible")
}

// racingHomes lets recovery win a move between the releaser's read and its CAS.
type racingHomes struct {
	runner.HomeStore
	once sync.Once
}

func (h *racingHomes) HandOff(ctx context.Context, run uuid.UUID, to runner.RunnerID, reason string, bytes, gen int64, cause runner.HomeMoveCause) (runner.Home, error) {
	if to == runner.ServerHome {
		h.once.Do(func() { _, _ = h.HomeStore.HandOff(ctx, run, "mac-2", "recovered", 0, gen, runner.HomeMoveCause{}) })
	}
	return h.HomeStore.HandOff(ctx, run, to, reason, bytes, gen, cause)
}

func TestRevocation_StaleReleaseCannotClearRecoveredHome(t *testing.T) {
	ctx := context.Background()
	r := newDrainRig(t)
	r.online(t, "mac-1")
	run, a := r.homedRun(t)
	require.NoError(t, r.pool.Ledger.Complete(ctx, a.Fence, "result"))
	before, err := r.repo.GetHome(ctx, run)
	require.NoError(t, err)

	require.NoError(t, runner.NewRevocationHomeReleaser(r.hub, &racingHomes{HomeStore: r.repo}, r.leases).RevokeRunner(ctx, "mac-1"))
	h, err := r.repo.GetHome(ctx, run)
	require.NoError(t, err)
	assert.Equal(t, runner.RunnerID("mac-2"), h.RunnerID, "the recovered home is never cleared by a stale release")
	assert.Equal(t, before.Generation+1, h.Generation)
	assert.False(t, r.claimActive(t, run))
}
