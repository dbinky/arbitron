package runner_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Reproduce the dispatcher releasing the stopped job's exact claim after the
// revoker's Active read, but before its Supersede transaction.
type releaseBeforeRevocation struct {
	runner.ResourceLeaseStore
	t           *testing.T
	replacement bool
	released    bool
	newClaim    engine.ResourceFenceRef
}

func (s *releaseBeforeRevocation) Supersede(ctx context.Context, old engine.ResourceFenceRef, proof runner.SupersedeProof, holder string, owner runner.RunnerID) (engine.ResourceFenceRef, error) {
	if !s.released {
		s.released = true
		require.NoError(s.t, s.ResourceLeaseStore.Release(ctx, old))
		if s.replacement {
			var err error
			s.newClaim, err = s.ResourceLeaseStore.Claim(ctx, old.Key, "replacement", "mac-2")
			require.NoError(s.t, err)
		}
	}
	return s.ResourceLeaseStore.Supersede(ctx, old, proof, holder, owner)
}

func TestRevocationHomeReleaser_ConcurrentStoppedClaimRelease(t *testing.T) {
	for _, tc := range []struct {
		name     string
		replaced bool
	}{
		{name: "released"},
		{name: "another writer claimed", replaced: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			r := newDrainRig(t)
			r.online(t, "mac-1")
			run, a := r.homedRun(t)
			_, err := r.leases.ClaimJob(ctx, "run:"+run.String(), runner.JobHolder(a.Job), a.Fence)
			require.NoError(t, err)
			leases := &releaseBeforeRevocation{ResourceLeaseStore: r.leases, t: t, replacement: tc.replaced}
			err = runner.NewRevocationHomeReleaser(r.hub, r.repo, leases).RevokeRunner(ctx, "mac-1")
			h, getErr := r.repo.GetHome(ctx, run)
			require.NoError(t, getErr)
			if tc.replaced {
				require.Error(t, err)
				assert.Equal(t, runner.RunnerID("mac-1"), h.RunnerID, "no handoff under somebody else's claim")
				require.NoError(t, r.leases.Check(ctx, leases.newClaim), "the replacement claim is not released")
			} else {
				require.NoError(t, err)
				assert.Equal(t, runner.ServerHome, h.RunnerID, "SINK: release takes its own fresh claim before handoff")
				assert.False(t, r.claimActive(t, run), "only the revoker's fresh claim is released")
			}
		})
	}
}
