package runner_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/adapter/checkpoint"
	"github.com/arbitron/arbitron/adapter/runner/hubtest"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func grWrite(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(c), 0o644))
	}
}

func grRead(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	}))
	return out
}

// grRig is a REAL home repo, lease repo, resource committer and checkpoint
// store on one migrated pool database.
type grRig struct {
	ctx     context.Context
	pool    *hubtest.Pool
	homes   *adaptsqlite.WorkspaceHomeRepo
	leases  *adaptsqlite.ResourceLeaseRepo
	commits *adaptsqlite.ResourceCommitRepo
	store   *checkpoint.Store
	res     *runner.ServerWorkspaceResource
	run     uuid.UUID
	dir     string
}

func newGRRig(t *testing.T) *grRig {
	t.Helper()
	p := hubtest.NewPool(t)
	homes := adaptsqlite.NewWorkspaceHomeRepo(p.DB)
	leases := adaptsqlite.NewResourceLeaseRepo(p.DB, "boot-1")
	commits := adaptsqlite.NewResourceCommitRepo(p.DB)
	store := checkpoint.NewStore(t.TempDir(), checkpoint.WithCatalog(adaptsqlite.NewCheckpointCatalogRepo(p.DB)))
	res, err := runner.NewServerWorkspaceResource(runner.ServerWorkspaceDeps{
		Leases: leases, Commits: commits, Homes: homes, Checkpoints: store, Ledger: p.Ledger})
	require.NoError(t, err)
	p.SeedRunner(t, "mac-1", 2)
	return &grRig{ctx: context.Background(), pool: p, homes: homes, leases: leases, commits: commits, store: store,
		res: res, run: p.SeedRun(t), dir: t.TempDir()}
}

// builtinCheckpoint makes the built-in runner the clean owner of files at
// committed version 1 (the P3 T6 bootstrap path).
func (r *grRig) builtinCheckpoint(t *testing.T, files map[string]string) {
	t.Helper()
	grWrite(t, r.dir, files)
	_, err := r.homes.AcquireResource(r.ctx, r.run, runner.BuiltInRunnerID, 0)
	require.NoError(t, err)
	_, _, err = r.store.Commit(r.ctx, r.run, r.dir)
	require.NoError(t, err)
}

// commitAs publishes files as the run's next committed checkpoint under a
// claim owned by owner: the stand-in for a remote push.
func (r *grRig) commitAs(t *testing.T, owner runner.RunnerID, files map[string]string) {
	t.Helper()
	src := t.TempDir()
	grWrite(t, src, files)
	claim, err := r.leases.Claim(r.ctx, "run:"+r.run.String(), "test:push", owner)
	require.NoError(t, err)
	base, _, err := r.store.Latest(r.ctx, r.run)
	require.NoError(t, err)
	staged, err := r.store.StageResource(r.ctx, r.run, src, base)
	require.NoError(t, err)
	staged.Resource = claim
	require.NoError(t, r.commits.CommitResource(r.ctx, claim, staged))
}

// homeOnMac1 commits base on the built-in runner, hands the home to mac-1 and,
// when pushed is non-nil, publishes it as mac-1's next checkpoint. The server
// directory keeps the stale base content.
func (r *grRig) homeOnMac1(t *testing.T, base, pushed map[string]string) {
	t.Helper()
	r.builtinCheckpoint(t, base)
	h, err := r.homes.GetHome(r.ctx, r.run)
	require.NoError(t, err)
	_, err = r.homes.HandOff(r.ctx, r.run, "mac-1", "test: placed on mac-1", 0, h.Generation, runner.HomeMoveCause{})
	require.NoError(t, err)
	if pushed != nil {
		r.commitAs(t, "mac-1", pushed)
	}
}

func (r *grRig) activeLease(t *testing.T) bool {
	t.Helper()
	_, ok, err := r.leases.Active(r.ctx, "run:"+r.run.String())
	require.NoError(t, err)
	return ok
}

func TestNewServerWorkspaceResource_RequiresEveryDependency(t *testing.T) {
	p := hubtest.NewPool(t)
	homes := adaptsqlite.NewWorkspaceHomeRepo(p.DB)
	leases := adaptsqlite.NewResourceLeaseRepo(p.DB, "boot-1")
	commits := adaptsqlite.NewResourceCommitRepo(p.DB)
	store := checkpoint.NewStore(t.TempDir())
	for _, d := range []runner.ServerWorkspaceDeps{
		{Commits: commits, Homes: homes, Checkpoints: store, Ledger: p.Ledger},
		{Leases: leases, Homes: homes, Checkpoints: store, Ledger: p.Ledger},
		{Leases: leases, Commits: commits, Checkpoints: store, Ledger: p.Ledger},
		{Leases: leases, Commits: commits, Homes: homes, Ledger: p.Ledger},
		{Leases: leases, Commits: commits, Homes: homes, Checkpoints: store},
	} {
		_, err := runner.NewServerWorkspaceResource(d)
		require.Error(t, err)
	}
}

func TestServerWorkspaceResource_AcquireRestoresARemoteHomeAndTakesOwnership(t *testing.T) {
	r := newGRRig(t)
	r.homeOnMac1(t, map[string]string{"a.txt": "v1"}, map[string]string{"a.txt": "v2", "src/b.txt": "new"})

	claim, err := r.res.Acquire(r.ctx, r.run, r.dir, "group snapshot")
	require.NoError(t, err)
	defer func() { require.NoError(t, r.res.Release(r.ctx, claim)) }()

	assert.Equal(t, map[string]string{"a.txt": "v2", "src/b.txt": "new"}, grRead(t, r.dir),
		"SINK: the server directory now holds the content mac-1 committed")
	h, err := r.homes.GetHome(r.ctx, r.run)
	require.NoError(t, err)
	assert.Equal(t, runner.BuiltInRunnerID, h.RunnerID)
	assert.False(t, h.Dirty)
	assert.Equal(t, int64(2), h.CheckpointVersion)
	hist, err := r.homes.History(r.ctx, r.run)
	require.NoError(t, err)
	last := hist[len(hist)-1]
	assert.Equal(t, runner.RunnerID("mac-1"), last.From)
	assert.Equal(t, runner.BuiltInRunnerID, last.To)
	assert.Equal(t, "server: group snapshot", last.Reason)
	assert.Equal(t, runner.GroupHolder(r.run, "group snapshot"), last.OperationID, "the move names the durable group operation")
	assert.Nil(t, last.TargetJobID, "a server mutation has no target job")
	assert.Equal(t, "run:"+r.run.String(), claim.Key)
	assert.Equal(t, string(runner.BuiltInRunnerID), claim.Owner)
	rec, ok, err := r.leases.Active(r.ctx, claim.Key)
	require.NoError(t, err)
	require.True(t, ok, "SINK: the mutation holds the run's single writer lease")
	assert.Equal(t, runner.GroupHolder(r.run, "group snapshot"), rec.Holder)
	assert.Nil(t, rec.Job)
	assert.Equal(t, "boot-1", rec.BootID)
}

func TestServerWorkspaceResource_CleanBuiltInHomeIsUntouched(t *testing.T) {
	r := newGRRig(t)
	r.builtinCheckpoint(t, map[string]string{"a.txt": "v1"})
	grWrite(t, r.dir, map[string]string{"local.txt": "server-side"})
	before, err := r.homes.History(r.ctx, r.run)
	require.NoError(t, err)

	claim, err := r.res.Acquire(r.ctx, r.run, r.dir, "group gather")
	require.NoError(t, err)
	require.NoError(t, r.res.Release(r.ctx, claim))

	assert.Equal(t, map[string]string{"a.txt": "v1", "local.txt": "server-side"}, grRead(t, r.dir), "no restore over a clean server home")
	after, err := r.homes.History(r.ctx, r.run)
	require.NoError(t, err)
	assert.Len(t, after, len(before), "no ownership change")
	assert.False(t, r.activeLease(t), "Release frees an uncommitted claim")
}

func TestServerWorkspaceResource_DirtyBuiltInHomeIsRestored(t *testing.T) {
	r := newGRRig(t)
	r.builtinCheckpoint(t, map[string]string{"a.txt": "v1"})
	require.NoError(t, r.homes.MarkDirty(r.ctx, r.run, true))
	grWrite(t, r.dir, map[string]string{"a.txt": "half-edited", "junk.txt": "interrupted step"})

	claim, err := r.res.Acquire(r.ctx, r.run, r.dir, "group finalize")
	require.NoError(t, err)
	defer func() { require.NoError(t, r.res.Release(r.ctx, claim)) }()

	assert.Equal(t, map[string]string{"a.txt": "v1"}, grRead(t, r.dir), "an interrupted edit never reaches a group mutation")
	h, err := r.homes.GetHome(r.ctx, r.run)
	require.NoError(t, err)
	assert.False(t, h.Dirty)
}

func TestServerWorkspaceResource_RefusesWhileAStepUsesTheWorkspace(t *testing.T) {
	r := newGRRig(t)
	r.homeOnMac1(t, map[string]string{"a.txt": "v1"}, nil)
	a := r.pool.ReserveJob(t, r.run, "mac-1", runner.MakeSourceID("mac-1", runner.CredAnthropicSubscription, ""), runner.OpAgent)
	require.NoError(t, r.pool.Ledger.MarkRunning(r.ctx, a.Fence))
	stepClaim, err := r.leases.ClaimJob(r.ctx, "run:"+r.run.String(), runner.JobHolder(a.Job), a.Fence)
	require.NoError(t, err)

	_, err = r.res.Acquire(r.ctx, r.run, r.dir, "group snapshot")
	require.ErrorIs(t, err, engine.ErrWorkspaceBusy)
	h, err := r.homes.GetHome(r.ctx, r.run)
	require.NoError(t, err)
	assert.Equal(t, runner.RunnerID("mac-1"), h.RunnerID, "a refused acquire moves nothing")
	rec, ok, err := r.leases.Active(r.ctx, stepClaim.Key)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, stepClaim, rec.Fence, "the step keeps its claim")

	require.NoError(t, r.pool.Ledger.Complete(r.ctx, a.Fence, ""))
	require.NoError(t, r.leases.Release(r.ctx, stepClaim))
	claim, err := r.res.Acquire(r.ctx, r.run, r.dir, "group snapshot")
	require.NoError(t, err)
	defer func() { require.NoError(t, r.res.Release(r.ctx, claim)) }()
	h, err = r.homes.GetHome(r.ctx, r.run)
	require.NoError(t, err)
	assert.Equal(t, runner.BuiltInRunnerID, h.RunnerID)
}

func TestServerWorkspaceResource_ChatJobsDoNotHoldTheWorkspace(t *testing.T) {
	r := newGRRig(t)
	r.builtinCheckpoint(t, map[string]string{"a.txt": "v1"})
	r.pool.ReserveJob(t, r.run, "mac-1", runner.MakeSourceID("mac-1", runner.CredAnthropicSubscription, ""), runner.OpChat)

	claim, err := r.res.Acquire(r.ctx, r.run, r.dir, "group snapshot")
	require.NoError(t, err)
	require.NoError(t, r.res.Release(r.ctx, claim))
}

func TestServerWorkspaceResource_NoCheckpointKeepsTheServerDirectory(t *testing.T) {
	r := newGRRig(t)
	grWrite(t, r.dir, map[string]string{"seed.txt": "s"})

	claim, err := r.res.Acquire(r.ctx, r.run, r.dir, "group snapshot")
	require.NoError(t, err)
	defer func() { require.NoError(t, r.res.Release(r.ctx, claim)) }()

	assert.Equal(t, map[string]string{"seed.txt": "s"}, grRead(t, r.dir))
	h, err := r.homes.GetHome(r.ctx, r.run)
	require.NoError(t, err)
	assert.Equal(t, runner.BuiltInRunnerID, h.RunnerID, "the server owns the home from now on")
	_, ok, err := r.store.Latest(r.ctx, r.run)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestServerWorkspaceResource_RemoteHomeWithoutACheckpointFailsClosed(t *testing.T) {
	r := newGRRig(t)
	h, err := r.homes.AcquireResource(r.ctx, r.run, runner.BuiltInRunnerID, 0)
	require.NoError(t, err)
	_, err = r.homes.HandOff(r.ctx, r.run, "mac-1", "test", 0, h.Generation, runner.HomeMoveCause{})
	require.NoError(t, err)

	_, err = r.res.Acquire(r.ctx, r.run, r.dir, "group snapshot")
	require.ErrorIs(t, err, runner.ErrCheckpointNotFound)
	h, err = r.homes.GetHome(r.ctx, r.run)
	require.NoError(t, err)
	assert.Equal(t, runner.RunnerID("mac-1"), h.RunnerID, "the only copy is never orphaned")
	assert.False(t, r.activeLease(t), "a failed acquire releases its claim")
}

func TestServerWorkspaceResource_CommitPublishesTheNextVersion(t *testing.T) {
	r := newGRRig(t)
	claim, err := r.res.Acquire(r.ctx, r.run, r.dir, "group gather")
	require.NoError(t, err)
	grWrite(t, r.dir, map[string]string{"merged.txt": "m"})

	require.NoError(t, r.res.Commit(r.ctx, claim, r.run, r.dir))
	require.NoError(t, r.res.Release(r.ctx, claim), "Release after a commit is a no-op")
	assert.False(t, r.activeLease(t), "SINK: the commit released the claim in its own transaction")
	ref, ok, err := r.store.Latest(r.ctx, r.run)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, int64(1), ref.Version)
	h, err := r.homes.GetHome(r.ctx, r.run)
	require.NoError(t, err)
	assert.Equal(t, int64(1), h.CheckpointVersion)
	assert.False(t, h.Dirty)
	fresh := t.TempDir()
	require.NoError(t, r.store.Restore(r.ctx, ref, fresh))
	assert.Equal(t, map[string]string{"merged.txt": "m"}, grRead(t, fresh), "SINK: the next pull sees the gathered files")

	claim, err = r.res.Acquire(r.ctx, r.run, r.dir, "group gather")
	require.NoError(t, err)
	grWrite(t, r.dir, map[string]string{"more.txt": "x"})
	require.NoError(t, r.res.Commit(r.ctx, claim, r.run, r.dir))
	ref, _, err = r.store.Latest(r.ctx, r.run)
	require.NoError(t, err)
	assert.Equal(t, int64(2), ref.Version)
}

func TestServerWorkspaceResource_CommitRefusesWhenTheServerDoesNotOwnTheHome(t *testing.T) {
	r := newGRRig(t)
	r.builtinCheckpoint(t, map[string]string{"a.txt": "v1"})
	claim, err := r.res.Acquire(r.ctx, r.run, r.dir, "group gather")
	require.NoError(t, err)
	h, err := r.homes.GetHome(r.ctx, r.run)
	require.NoError(t, err)
	_, err = r.homes.HandOff(r.ctx, r.run, "mac-1", "test: bypass", 0, h.Generation, runner.HomeMoveCause{})
	require.NoError(t, err)
	grWrite(t, r.dir, map[string]string{"a.txt": "server write"})

	err = r.res.Commit(r.ctx, claim, r.run, r.dir)
	require.ErrorIs(t, err, runner.ErrHomeOwned)
	require.NoError(t, r.res.Release(r.ctx, claim))
	ref, _, err := r.store.Latest(r.ctx, r.run)
	require.NoError(t, err)
	assert.Equal(t, int64(1), ref.Version, "a server write never bypasses checkpoint generations")
}

func TestServerWorkspaceResource_StaleClaimCannotCommit(t *testing.T) {
	r := newGRRig(t)
	claim, err := r.res.Acquire(r.ctx, r.run, r.dir, "group gather")
	require.NoError(t, err)
	require.NoError(t, r.res.Release(r.ctx, claim))
	grWrite(t, r.dir, map[string]string{"late.txt": "after release"})

	err = r.res.Commit(r.ctx, claim, r.run, r.dir)
	require.ErrorIs(t, err, runner.ErrResourceFenced)
	_, ok, err := r.store.Latest(r.ctx, r.run)
	require.NoError(t, err)
	assert.False(t, ok, "a released claim publishes nothing")

	other := claim
	other.Key = "run:" + uuid.NewString()
	err = r.res.Commit(r.ctx, other, r.run, r.dir)
	require.ErrorIs(t, err, runner.ErrResourceFenced, "a claim on another run is never accepted")
}
