package runner_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Success never advances Latest: the candidate is PREPARED; the previous
// committed ref survives a restart until P3 T5's transaction.
func TestWorkspaceSync_PrepareSuccessDoesNotAdvanceLatest(t *testing.T) {
	f := newWSFixture(t)
	base := f.seedBaseline(t, map[string]string{"a.txt": "v1"})
	a := f.reserve(t, builtin)
	att := f.begin(t, a, stay(builtin))
	f.write(t, "a.txt", "v2")

	c, err := f.sync.FinishSuccess(context.Background(), a, att)
	require.NoError(t, err)
	ref, err := runner.ParseCheckpointRef(c.Ref)
	require.NoError(t, err)
	assert.Equal(t, base.Version+1, ref.Version)
	assert.Equal(t, base, f.latest(t))

	reopened := f.openStore() // a restart: new store over the same archive dir and DB
	got, ok, err := reopened.Latest(context.Background(), f.run)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, base, got)
	h := f.home(t)
	assert.True(t, h.Dirty, "only the step transaction clears dirty")
	assert.Equal(t, base.Version, h.CheckpointVersion)
	assert.Equal(t, "v1", readIn(t, f.restoreTo(t, base), "a.txt"))
}

// The built-in candidate row carries the exact job AND resource fences.
func TestWorkspaceSync_BuiltInCandidateHasExactFence(t *testing.T) {
	f := newWSFixture(t)
	f.seedBaseline(t, map[string]string{"a.txt": "v1"})
	a := f.reserve(t, builtin)
	att := f.begin(t, a, stay(builtin))
	f.write(t, "a.txt", "v2")
	c, err := f.sync.FinishSuccess(context.Background(), a, att)
	require.NoError(t, err)
	assert.Equal(t, att.Resource, c.Resource)
	assert.False(t, c.NonPortable)

	ref, err := runner.ParseCheckpointRef(c.Ref)
	require.NoError(t, err)
	state, jobID, gen, token, found := f.preparedRow(t, ref.Version)
	require.True(t, found)
	assert.Equal(t, "prepared", state)
	assert.Equal(t, a.Fence.JobID.String(), jobID)
	assert.Equal(t, a.Fence.Generation, gen)
	assert.Equal(t, att.Resource.Token.String(), token)
	assert.Equal(t, "v2", readIn(t, f.restorePreparedTo(t, ref), "a.txt"))
}

// A remote step whose push never landed cannot succeed; after the push it
// returns exactly that prepared candidate (never a Latest lookup).
func TestWorkspaceSync_RemoteSuccessRequiresPreparedPush(t *testing.T) {
	f := newWSFixture(t)
	f.seedBaseline(t, map[string]string{"a.txt": "v1"})
	f.addRemote(t, "mac-1", 2)
	a := f.reserve(t, "mac-1")
	att := f.begin(t, a, moveTo("mac-1", runner.TierShouldMove))

	_, err := f.sync.FinishSuccess(context.Background(), a, att)
	require.ErrorIs(t, err, runner.ErrPushMissing)

	ref, err := f.sync.AcceptPush(context.Background(), a.Fence, f.run, f.mustStage(t, att, map[string]string{"a.txt": "remote"}))
	require.NoError(t, err)
	c, err := f.sync.FinishSuccess(context.Background(), a, att)
	require.NoError(t, err)
	assert.Equal(t, engine.WorkspaceCompletion{Ref: ref.String(), Resource: att.Resource}, c)
	assert.Equal(t, att.Base, f.latest(t), "accepting a push prepares only")
	assert.Equal(t, "remote", readIn(t, f.restorePreparedTo(t, ref), "a.txt"))
}

// G3 (design §8 "on a server restart, remote jobs re-attach with no repeat"):
// a brand-new WorkspaceSync over the same DB/archive (empty in-memory cache)
// still resolves the exact pushed candidate through PreparedFor.
func TestWorkspaceSync_FinishSuccessFindsPushedCandidateAfterRestart(t *testing.T) {
	f := newWSFixture(t)
	base := f.seedBaseline(t, map[string]string{"a.txt": "v1"})
	f.addRemote(t, "mac-1", 2)
	a := f.reserve(t, "mac-1")
	att := f.begin(t, a, moveTo("mac-1", runner.TierShouldMove))
	ref, err := f.sync.AcceptPush(context.Background(), a.Fence, f.run, f.mustStage(t, att, map[string]string{"a.txt": "pushed before restart"}))
	require.NoError(t, err)

	d := f.syncDeps()
	d.Checkpoints = &storeSpy{t: t, CheckpointStore: f.openStore()} // restart: fresh store, fresh sync, same catalog
	restarted := runner.NewWorkspaceSync(d)
	c, err := restarted.FinishSuccess(context.Background(), a, att)
	require.NoError(t, err)
	assert.Equal(t, engine.WorkspaceCompletion{Ref: ref.String(), Resource: att.Resource}, c)
	assert.Equal(t, base, f.latest(t), "still prepared only")

	// Another job fence never sees this candidate (no newest-row guess).
	a2 := f.reserve(t, "mac-1")
	_, err = restarted.FinishSuccess(context.Background(), a2, runner.WorkspaceAttempt{RunID: f.run, Runner: "mac-1", Base: base, Resource: att.Resource})
	require.ErrorIs(t, err, runner.ErrPushMissing)
}

// A push from a fenced generation is refused and its staging is discarded.
func TestWorkspaceSync_StalePushRejectedAndDiscarded(t *testing.T) {
	f := newWSFixture(t)
	base := f.seedBaseline(t, map[string]string{"a.txt": "v1"})
	f.addRemote(t, "mac-1", 2)
	a1 := f.reserve(t, "mac-1")
	att1 := f.begin(t, a1, moveTo("mac-1", runner.TierShouldMove))
	staged := f.mustStage(t, att1, map[string]string{"a.txt": "stale"})
	ctx := context.Background()
	_, err := f.pool.Ledger.Requeue(ctx, a1.Fence, runner.OutcomeRunnerLost)
	require.NoError(t, err)
	require.NoError(t, f.leases.Release(ctx, att1.Resource))

	_, err = f.sync.AcceptPush(ctx, a1.Fence, f.run, staged)
	require.ErrorIs(t, err, runner.ErrFenced)
	_, _, discards := f.store.counts()
	assert.Equal(t, 1, discards)
	_, _, _, _, found := f.preparedRow(t, base.Version+1)
	assert.False(t, found, "no candidate was prepared")
}

// A stale push cannot clear the new owner's dirty state, even when it carries
// the new owner's resource fence.
func TestWorkspaceSync_StalePushCannotClearNewOwnerDirty(t *testing.T) {
	f := newWSFixture(t)
	base := f.seedBaseline(t, map[string]string{"a.txt": "v1"})
	f.addRemote(t, "mac-1", 2)
	ctx := context.Background()
	a1 := f.reserve(t, "mac-1")
	att1 := f.begin(t, a1, moveTo("mac-1", runner.TierShouldMove))
	staged := f.mustStage(t, att1, map[string]string{"a.txt": "stale"})
	nj, err := f.pool.Ledger.Requeue(ctx, a1.Fence, runner.OutcomeRunnerLost)
	require.NoError(t, err)
	require.NoError(t, f.leases.Release(ctx, att1.Resource))

	a2 := f.reserveJob(t, nj.ID, builtin)
	att2 := f.begin(t, a2, moveTo(builtin, runner.TierMustMove))
	f.write(t, "a.txt", "new owner editing")
	staged.Resource = att2.Resource // forged: the stale writer claims the new owner's resource

	_, err = f.sync.AcceptPush(ctx, a1.Fence, f.run, staged)
	require.ErrorIs(t, err, runner.ErrFenced)
	assert.True(t, f.home(t).Dirty)
	assert.Equal(t, "new owner editing", f.read(t, "a.txt"))
	assert.Equal(t, base, f.latest(t))
}

// The pull plan pins ONE committed ref and the exact claim into both tokens.
func TestWorkspaceSync_PreparePullPinsBaseAndResource(t *testing.T) {
	f := newWSFixture(t)
	base := f.seedBaseline(t, map[string]string{"a.txt": "v1"})
	f.addRemote(t, "mac-1", 2)
	a := f.reserve(t, "mac-1")
	att := f.begin(t, a, moveTo("mac-1", runner.TierShouldMove))

	plan, err := f.sync.PreparePull(context.Background(), a, f.run)
	require.NoError(t, err)
	assert.Equal(t, base, plan.Ref)
	assert.Equal(t, att.Resource, plan.Resource)
	assert.Equal(t, runner.RunnerID("mac-1"), plan.Home.RunnerID)
	assert.Equal(t, f.home(t).Generation, plan.Home.Generation)
	scopes := f.tokens.issued()
	require.Len(t, scopes, 2)
	for i, dir := range []string{runner.TransferPull, runner.TransferPush} {
		assert.Equal(t, runner.TransferScope{Fence: a.Fence, RunID: f.run, Direction: dir, BaseRef: base.String(), Resource: att.Resource}, scopes[i])
	}
	assert.Equal(t, scopes[0], plan.PullToken.Scope())
	assert.Equal(t, scopes[1], plan.PushToken.Scope())

	// A fenced job gets no plan and no tokens.
	_, err = f.pool.Ledger.Requeue(context.Background(), a.Fence, runner.OutcomeRunnerLost)
	require.NoError(t, err)
	_, err = f.sync.PreparePull(context.Background(), a, f.run)
	require.ErrorIs(t, err, runner.ErrFenced)
	assert.Len(t, f.tokens.issued(), 2)
}

func TestWorkspaceSync_PreparePullWithoutTokensFailsClosed(t *testing.T) {
	f := newWSFixture(t)
	f.seedBaseline(t, map[string]string{"a.txt": "v1"})
	f.addRemote(t, "mac-1", 2)
	d := f.syncDeps()
	d.Tokens = nil
	s := runner.NewWorkspaceSync(d)
	a := f.reserve(t, "mac-1")
	_, err := s.BeginAttempt(context.Background(), a, f.req(), moveTo("mac-1", runner.TierShouldMove), f.job())
	require.NoError(t, err)
	_, err = s.PreparePull(context.Background(), a, f.run)
	require.ErrorIs(t, err, runner.ErrNoTransferTokens)
}

// Failed validation after preparation: the prior committed ref is restored,
// the claim freed, and the candidate never becomes progress.
func TestWorkspaceSync_FailedValidationRestoresPriorCommittedRef(t *testing.T) {
	f := newWSFixture(t)
	base := f.seedBaseline(t, map[string]string{"a.txt": "v1"})
	a := f.reserve(t, builtin)
	att := f.begin(t, a, stay(builtin))
	f.write(t, "a.txt", "v2")
	f.write(t, "new.txt", "created by the failed step")
	c, err := f.sync.FinishSuccess(context.Background(), a, att)
	require.NoError(t, err)

	require.NoError(t, f.sync.FinishFailure(context.Background(), a, att))
	assert.Equal(t, "v1", f.read(t, "a.txt"))
	assert.NoFileExists(t, filepath.Join(f.dir(), "new.txt"))
	assert.Equal(t, base, f.latest(t))
	assert.False(t, f.home(t).Dirty)
	_, ok := f.active(t)
	assert.False(t, ok, "the exact claim is released")
	ref, err := runner.ParseCheckpointRef(c.Ref)
	require.NoError(t, err)
	state, _, _, _, found := f.preparedRow(t, ref.Version)
	require.True(t, found)
	assert.Equal(t, "prepared", state, "never committed")
}

// Progress reaches execution status with real bytes; a stale fence reports
// nothing; the size input is the committed manifest size.
func TestWorkspaceSync_ProgressAndSize(t *testing.T) {
	f := newWSFixture(t)
	base := f.seedBaseline(t, map[string]string{"a.txt": "0123456789"})
	f.addRemote(t, "mac-1", 2)
	a := f.reserve(t, "mac-1")
	f.begin(t, a, moveTo("mac-1", runner.TierShouldMove))

	f.sync.ReportProgress(context.Background(), a.Fence, f.run, 4, 10)
	st := statuses(f.bus, f.run)
	last := st[len(st)-1]
	assert.Equal(t, instance.ExecMovingWorkspace, last.State)
	require.NotNil(t, last.Progress)
	assert.Equal(t, instance.MoveProgress{BytesDone: 4, BytesTotal: 10}, *last.Progress)
	assert.Equal(t, "mac-1", last.HomeRunnerID)

	n := len(statuses(f.bus, f.run))
	f.sync.ReportProgress(context.Background(), a.Fence, f.run, 11, 10)
	_, err := f.pool.Ledger.Requeue(context.Background(), a.Fence, runner.OutcomeRunnerLost)
	require.NoError(t, err)
	f.sync.ReportProgress(context.Background(), a.Fence, f.run, 5, 10)
	assert.Len(t, statuses(f.bus, f.run), n, "invalid progress and stale fences publish nothing")

	m, err := f.store.Manifest(context.Background(), base)
	require.NoError(t, err)
	assert.Equal(t, m.TotalBytes(), f.sync.WorkspaceBytes(context.Background(), f.run))
}

// P3 T16: a promisor miss stages the actual workspace as a local receipt.
func TestWorkspaceSync_PromisorMissingStagesActualWorkspace(t *testing.T) {
	f := newWSFixture(t)
	base := f.seedBaseline(t, map[string]string{"a.txt": "v1"})
	a := f.reserve(t, builtin)
	att := f.begin(t, a, stay(builtin))
	f.store.set(func(s *storeSpy) { s.promisor = true })

	c, err := f.sync.FinishSuccess(context.Background(), a, att)
	require.NoError(t, err, "a promisor miss is never a step failure")
	assert.True(t, c.NonPortable)
	assert.Empty(t, c.Ref)
	assert.Equal(t, att.Resource, c.Resource)
	localRef, err := runner.ParseLocalCheckpointRef(c.LocalRef)
	require.NoError(t, err)
	assert.Equal(t, f.run, localRef.RunID)
	assert.Equal(t, int64(1), localRef.Version)
	dst := t.TempDir()
	require.NoError(t, f.local.RestoreLocal(context.Background(), localRef, dst))
	assert.Equal(t, "v1", readIn(t, dst, "a.txt"))
	assert.Equal(t, runner.NonPortablePromisorReason, f.home(t).NonPortableReason)
	assert.Equal(t, base, f.latest(t))
	_, _, _, _, found := f.preparedRow(t, base.Version+1)
	assert.False(t, found, "no candidate is recorded")
}

// RR-08: once all objects are present again the normal candidate flows on
// (P3 T5's TestStepCommitRepo_CandidatePromotionClearsNonPortable proves the
// promotion clears the marker).
func TestWorkspaceSync_ObjectsPresentAgainStagesNormalCandidate(t *testing.T) {
	f := newWSFixture(t)
	base := f.seedBaseline(t, map[string]string{"a.txt": "v1"})
	require.NoError(t, f.homes.SetNonPortable(context.Background(), f.run, runner.NonPortablePromisorReason))
	a := f.reserve(t, builtin)
	att := f.begin(t, a, stay(builtin))
	f.write(t, "a.txt", "v2")

	c, err := f.sync.FinishSuccess(context.Background(), a, att)
	require.NoError(t, err)
	assert.False(t, c.NonPortable)
	ref, err := runner.ParseCheckpointRef(c.Ref)
	require.NoError(t, err)
	assert.Equal(t, base.Version+1, ref.Version)
	state, _, _, _, found := f.preparedRow(t, ref.Version)
	require.True(t, found)
	assert.Equal(t, "prepared", state)
}

// CommitBuiltIn (contract name) prepares only, and only for the job holding the claim.
func TestWorkspaceSync_CommitBuiltInRequiresTheActiveClaim(t *testing.T) {
	f := newWSFixture(t)
	base := f.seedBaseline(t, map[string]string{"a.txt": "v1"})
	a1 := f.reserve(t, builtin)
	a2 := f.reserve(t, builtin)
	f.begin(t, a1, stay(builtin))
	f.write(t, "a.txt", "v2")

	_, err := f.sync.CommitBuiltIn(context.Background(), a2.Fence, f.run, f.dir())
	require.ErrorIs(t, err, runner.ErrResourceFenced)
	ref, err := f.sync.CommitBuiltIn(context.Background(), a1.Fence, f.run, f.dir())
	require.NoError(t, err)
	assert.Equal(t, base.Version+1, ref.Version)
	assert.Equal(t, base, f.latest(t))
}

// The P3 T10 restorer rewrites the directory to the committed base.
func TestWorkspaceSync_RestoreBuiltInRestoresCommittedBase(t *testing.T) {
	f := newWSFixture(t)
	f.seedBaseline(t, map[string]string{"a.txt": "v1"})
	f.write(t, "a.txt", "dirty")
	require.NoError(t, f.homes.MarkDirty(context.Background(), f.run, true))
	require.NoError(t, f.sync.RestoreBuiltIn(context.Background(), f.run, f.dir()))
	assert.Equal(t, "v1", f.read(t, "a.txt"))
	assert.False(t, f.home(t).Dirty)

	g := newWSFixture(t) // no baseline at all
	require.ErrorIs(t, g.sync.RestoreBuiltIn(context.Background(), g.run, g.dir()), runner.ErrCheckpointNotFound)
}
