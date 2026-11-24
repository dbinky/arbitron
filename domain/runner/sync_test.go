package runner_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const builtin = runner.BuiltInRunnerID

func TestWorkspaceSync_ConstructionFailsClosed(t *testing.T) {
	f := newWSFixture(t)
	for name, mut := range map[string]func(*runner.SyncDeps){
		"Checkpoints": func(d *runner.SyncDeps) { d.Checkpoints = nil },
		"Homes":       func(d *runner.SyncDeps) { d.Homes = nil },
		"Leases":      func(d *runner.SyncDeps) { d.Leases = nil },
		"Ledger":      func(d *runner.SyncDeps) { d.Ledger = nil },
		"Events":      func(d *runner.SyncDeps) { d.Events = nil },
		"Status":      func(d *runner.SyncDeps) { d.Status = nil },
		// P3 T16 Cycle 5 adds: "Local": func(d *runner.SyncDeps) { d.Local = nil },
	} {
		t.Run(name, func(t *testing.T) {
			d := f.syncDeps()
			mut(&d)
			assert.PanicsWithValue(t, "runner: NewWorkspaceSync requires "+name, func() { runner.NewWorkspaceSync(d) })
		})
	}
	opt := f.syncDeps()
	opt.Tokens, opt.Sizes, opt.Clock = nil, nil, nil
	assert.NotPanics(t, func() { runner.NewWorkspaceSync(opt) }, "tokens and sizer are optional and fail closed at use")
}

// Happy path: a built-in attempt claims the run, keeps the committed base and
// marks the home dirty before real work starts.
func TestWorkspaceSync_BuiltInAttemptClaimsAndMarksDirty(t *testing.T) {
	f := newWSFixture(t)
	base := f.seedBaseline(t, map[string]string{"a.txt": "v1"})
	a := f.reserve(t, builtin)
	att := f.begin(t, a, stay(builtin))

	assert.Equal(t, runner.WorkspaceAttempt{RunID: f.run, WorkDir: f.dir(), Runner: builtin, Base: base, Resource: att.Resource}, att)
	require.NoError(t, att.Resource.Validate())
	rec, ok := f.active(t)
	require.True(t, ok)
	assert.Equal(t, att.Resource, rec.Fence)
	require.NotNil(t, rec.Job)
	assert.Equal(t, a.Fence, *rec.Job)
	assert.Equal(t, runner.JobHolder(a.Job), rec.Holder)
	h := f.home(t)
	assert.Equal(t, builtin, h.RunnerID)
	assert.True(t, h.Dirty)
	assert.Equal(t, "v1", f.read(t, "a.txt"))
}

// Edge: two writers of one run on the SAME runner cannot overlap.
func TestWorkspaceSync_ConcurrentSameRunnerAttemptIsBusy(t *testing.T) {
	f := newWSFixture(t)
	f.seedBaseline(t, map[string]string{"a.txt": "v1"})
	a1 := f.reserve(t, builtin)
	a2 := f.reserve(t, builtin)
	f.begin(t, a1, stay(builtin))
	f.write(t, "a.txt", "a1 is writing")

	_, err := f.sync.BeginAttempt(context.Background(), a2, f.req(), stay(builtin), f.job())
	require.ErrorIs(t, err, runner.ErrResourceBusy)
	rec, ok := f.active(t)
	require.True(t, ok)
	assert.Equal(t, a1.Fence, *rec.Job, "the first writer keeps the claim")
	assert.Equal(t, "a1 is writing", f.read(t, "a.txt"), "the busy attempt never touched the working copy")
}

// Regression (design §8): interrupted built-in edits roll back before the next attempt.
func TestWorkspaceSync_BuiltInInterruptedEditsRollBack(t *testing.T) {
	f := newWSFixture(t)
	base := f.seedBaseline(t, map[string]string{"a.txt": "v1"})
	a1 := f.reserve(t, builtin)
	att1 := f.begin(t, a1, stay(builtin))
	f.write(t, "a.txt", "half-written")
	f.write(t, "junk.txt", "partial")
	// The process died. Recovery (P3 T10) fences the job and frees its exact claim after the stop.
	ctx := context.Background()
	require.NoError(t, f.pool.Ledger.Fail(ctx, a1.Fence, runner.OutcomeRunnerLost))
	require.NoError(t, f.leases.Release(ctx, att1.Resource))

	a2 := f.reserve(t, builtin)
	att2 := f.begin(t, a2, stay(builtin))
	assert.Equal(t, base, att2.Base)
	assert.Equal(t, "v1", f.read(t, "a.txt"))
	assert.NoFileExists(t, filepath.Join(f.dir(), "junk.txt"))
	assert.True(t, f.home(t).Dirty)
}

// BeginAttempt only consumes the baseline: it never bootstraps after a claim.
func TestWorkspaceSync_BeginAttemptNeverBootstraps(t *testing.T) {
	f := newWSFixture(t)
	f.write(t, "a.txt", "never baselined")
	a := f.reserve(t, builtin)
	f.store.set(func(s *storeSpy) { s.forbidCommit = true })

	_, err := f.sync.BeginAttempt(context.Background(), a, f.req(), stay(builtin), f.job())
	require.ErrorIs(t, err, runner.ErrCheckpointNotFound)
	_, ok := f.active(t)
	assert.False(t, ok, "the claim taken for the attempt is released on failure")
	assert.False(t, f.home(t).Dirty)
	commits, _, _ := f.store.counts()
	assert.Zero(t, commits, "no bootstrap after reservation/claim (the spy also t.Errorf's if one is attempted)")
}

// Exact operation correlation (amendment): every move carries the exact
// target job/invocation/node/operation and generations; a loop revisit is a
// distinct operation.
func TestWorkspaceSync_MoveCarriesExactOperationCause(t *testing.T) {
	f := newWSFixture(t)
	base := f.seedBaseline(t, map[string]string{"a.txt": "v1"})
	f.addRemote(t, "mac-1", 2)
	ctx := context.Background()

	a1 := f.reserve(t, "mac-1")
	att1 := f.begin(t, a1, moveTo("mac-1", runner.TierShouldMove))
	assert.True(t, att1.Moved)
	assert.Equal(t, base, att1.Base)
	require.NoError(t, f.pool.Ledger.Fail(ctx, a1.Fence, runner.OutcomeOther))
	require.NoError(t, f.leases.Release(ctx, att1.Resource))

	a2 := f.reserve(t, builtin) // the loop revisits the node: a new invocation
	att2 := f.begin(t, a2, moveTo(builtin, runner.TierMustMove))
	assert.True(t, att2.Moved)
	assert.Equal(t, "v1", f.read(t, "a.txt"), "moving back restores the committed base")

	hist, err := f.homes.History(ctx, f.run)
	require.NoError(t, err)
	require.Len(t, hist, 2)
	for i, tc := range []struct {
		a         runner.Assignment
		to        runner.RunnerID
		from, gen int64
		tier      string
	}{{a1, "mac-1", 0, 1, runner.TierShouldMove}, {a2, builtin, 1, 2, runner.TierMustMove}} {
		m := hist[i]
		require.NotNil(t, m.TargetJobID)
		require.NotNil(t, m.InvocationID)
		assert.Equal(t, tc.a.Job.ID, *m.TargetJobID)
		assert.Equal(t, tc.a.Job.Key.InvocationID, *m.InvocationID)
		assert.Equal(t, "edit", m.NodeID)
		assert.Equal(t, tc.a.Job.Key.OperationID, m.OperationID)
		assert.Equal(t, tc.from, m.FromGeneration)
		assert.Equal(t, tc.gen, m.ToGeneration)
		assert.Equal(t, tc.to, m.To)
		assert.Contains(t, m.Reason, tc.tier)
	}
	assert.NotEqual(t, *hist[0].InvocationID, *hist[1].InvocationID)

	moved := f.pool.EventsOf(t, runner.EventWorkspaceMoved)
	require.Len(t, moved, 2)
	assert.Equal(t, a1.Job.ID.String(), moved[0].Detail["jobId"])
	assert.Equal(t, "mac-1", moved[0].Detail["to"])
	var moving []instance.ExecutionStatus
	for _, st := range statuses(f.bus, f.run) {
		if st.State == instance.ExecMovingWorkspace {
			moving = append(moving, st)
		}
	}
	require.Len(t, moving, 2)
	assert.Equal(t, "mac-1", moving[0].HomeRunnerID)
	assert.Equal(t, a1.Job.ID, moving[0].JobID)
}

// Remote homes are sized by the committed manifest, never by the server's
// (stale) local copy; server-local homes use the sizer.
func TestWorkspaceSync_RemoteHomeUsesCommittedManifestSize(t *testing.T) {
	f := newWSFixture(t)
	base := f.seedBaseline(t, map[string]string{"a.txt": "0123456789", "b.txt": "abc"})
	f.sizes.bytes[f.run] = 999
	assert.Equal(t, int64(999), f.sync.WorkspaceBytes(context.Background(), f.run), "server-local home: instance dir size")

	f.addRemote(t, "mac-1", 2)
	f.begin(t, f.reserve(t, "mac-1"), moveTo("mac-1", runner.TierShouldMove))
	m, err := f.store.Manifest(context.Background(), base)
	require.NoError(t, err)
	assert.Equal(t, m.TotalBytes(), f.sync.WorkspaceBytes(context.Background(), f.run))
	assert.NotEqual(t, int64(999), m.TotalBytes())
}

// Sizing failure: optional load moves are disabled (-1) and a diagnostic is
// recorded; must-moves stay allowed.
func TestWorkspaceSync_SizingErrorDisablesOnlyLoadMove(t *testing.T) {
	f := newWSFixture(t)
	f.sizes.err = errors.New("walk: permission denied")
	bytes := f.sync.WorkspaceBytes(context.Background(), f.run)
	assert.Equal(t, runner.WorkspaceBytesUnknown, bytes)
	ev := f.pool.EventsOf(t, runner.EventWorkspaceSizeUnknown)
	require.Len(t, ev, 1)
	require.NotNil(t, ev[0].RunID)
	assert.Equal(t, f.run, *ev[0].RunID)

	r := func(id runner.RunnerID) runner.Runner {
		return runner.Runner{ID: id, Name: string(id), StepLimit: 2, Liveness: runner.LivenessOnline,
			Compat: runner.Compatibility{State: runner.CompatCompatible}}
	}
	snap := runner.PoolSnapshot{Runners: []runner.Runner{r("mac-1"), r("mac-2")}, SlotsInUse: map[runner.RunnerID]int{"mac-1": 1},
		SourceInFlight: map[runner.SourceID]int{}, Telemetry: map[runner.RunnerID]runner.Telemetry{},
		Homes: map[uuid.UUID]runner.Home{f.run: {RunID: f.run, RunnerID: "mac-1", Generation: 1}}, Now: time.Now()}
	policy := runner.PlacementPolicy{MoveLoadThresholdPct: 10, MoveSizeCapBytes: 1 << 30, TelemetryMaxAge: time.Minute}
	req := runner.ExecRequirements{Group: runner.ModelGroup{Kind: runner.CredNone}, Op: runner.OpAgent, Affinity: runner.AffinityWorkspace, RunID: f.run}

	req.WorkspaceBytes = 1024
	assert.Equal(t, runner.TierMayMoveLoad, runner.Place(req, snap, policy).Tier, "the load gap alone would move a known-size workspace")
	req.WorkspaceBytes = bytes
	d := runner.Place(req, snap, policy)
	assert.Equal(t, runner.TierStay, d.Tier)
	assert.Equal(t, runner.RunnerID("mac-1"), d.Runner)
	snap.Runners[0].Draining = true
	d = runner.Place(req, snap, policy)
	assert.Equal(t, runner.TierMustMove, d.Tier, "unknown size never blocks a must-move")
	assert.Equal(t, runner.RunnerID("mac-2"), d.Runner)
}

// RR-08: a non-portable home's attempt never restores; its working copy (edited
// after the last local commit) survives; it still claims and marks dirty.
func TestWorkspaceSync_NonPortableAttemptNeverRestores(t *testing.T) {
	f := newWSFixture(t)
	f.write(t, "a.txt", "initial")
	f.store.set(func(s *storeSpy) { s.promisor = true })
	require.NoError(t, f.baseline.EnsureBaseline(context.Background(), f.run, f.dir()))
	f.write(t, "a.txt", "edited after the last local commit")

	a := f.reserve(t, builtin)
	att := f.begin(t, a, stay(builtin))
	assert.Equal(t, "edited after the last local commit", f.read(t, "a.txt"))
	assert.True(t, att.Base.IsZero(), "no catalog baseline is required")
	rec, ok := f.active(t)
	require.True(t, ok)
	assert.Equal(t, a.Fence, *rec.Job)
	assert.True(t, f.home(t).Dirty)
}

// RR-08: a non-portable home never goes to a remote runner.
func TestWorkspaceSync_NonPortableRejectsRemoteRunner(t *testing.T) {
	f := newWSFixture(t)
	f.seedBaseline(t, map[string]string{"a.txt": "v1"})
	require.NoError(t, f.homes.SetNonPortable(context.Background(), f.run, runner.NonPortablePromisorReason))
	f.addRemote(t, "mac-1", 2)
	a := f.reserve(t, "mac-1")

	_, err := f.sync.BeginAttempt(context.Background(), a, f.req(), moveTo("mac-1", runner.TierShouldMove), f.job())
	require.ErrorIs(t, err, runner.ErrHomeOwned)
	_, ok := f.active(t)
	assert.False(t, ok, "no claim is taken")
	assert.Equal(t, runner.ServerHome, f.home(t).RunnerID)
	hist, err := f.homes.History(context.Background(), f.run)
	require.NoError(t, err)
	assert.Empty(t, hist)
}

// Error: a stale (requeued) assignment cannot begin.
func TestWorkspaceSync_StaleAssignmentCannotBegin(t *testing.T) {
	f := newWSFixture(t)
	f.seedBaseline(t, map[string]string{"a.txt": "v1"})
	a := f.reserve(t, builtin)
	_, err := f.pool.Ledger.Requeue(context.Background(), a.Fence, runner.OutcomeRunnerLost)
	require.NoError(t, err)
	_, err = f.sync.BeginAttempt(context.Background(), a, f.req(), stay(builtin), f.job())
	require.ErrorIs(t, err, runner.ErrFenced)
	_, ok := f.active(t)
	assert.False(t, ok)
}
