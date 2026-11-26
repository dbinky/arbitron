// domain/runner/local_checkpoint_sync_test.go
package runner_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lcSyncHomes adds the writes the sync paths make to lcHomes.
type lcSyncHomes struct{ *lcHomes }

func (h lcSyncHomes) SetNonPortable(_ context.Context, run uuid.UUID, reason string) error {
	x := h.homes[run]
	x.RunID, x.NonPortableReason = run, reason
	h.homes[run] = x
	return nil
}
func (h lcSyncHomes) MarkDirty(_ context.Context, run uuid.UUID, dirty bool) error {
	x := h.homes[run]
	x.RunID, x.Dirty = run, dirty
	h.homes[run] = x
	return nil
}

// lcPromisorStore: StageBuiltIn / Commit always report missing promisor objects.
type lcPromisorStore struct{ *lcCatalog }

func (s lcPromisorStore) StageBuiltIn(context.Context, uuid.UUID, string, runner.Fence, engine.ResourceFenceRef) (runner.CheckpointRef, runner.Manifest, error) {
	return runner.CheckpointRef{}, runner.Manifest{}, errors.Join(runner.ErrUnsupportedGit, runner.ErrPromisorObjectsMissing)
}
func (s lcPromisorStore) Commit(context.Context, uuid.UUID, string) (runner.CheckpointRef, runner.Manifest, error) {
	return runner.CheckpointRef{}, runner.Manifest{}, errors.Join(runner.ErrUnsupportedGit, runner.ErrPromisorObjectsMissing)
}
func (s lcPromisorStore) Discard(context.Context, runner.StagedCheckpoint) error { return nil }

type lcLedger struct{ runner.Ledger }

func (lcLedger) FenceCheck(context.Context, runner.Fence) error             { return nil }
func (lcLedger) ListByRun(context.Context, uuid.UUID) ([]runner.Job, error) { return nil, nil }

type lcSync struct {
	*lcFixture
	sync     *runner.WorkspaceSync
	baseline *runner.BaselineInitializer
	att      runner.WorkspaceAttempt
	asg      runner.Assignment
}

func newLCSync(t *testing.T) *lcSync {
	t.Helper()
	f := newLCFixture(t)
	homes := lcSyncHomes{f.homes}
	store := lcPromisorStore{f.catalog}
	ledger := lcLedger{}
	status := runner.NewExecutionStatusProjector(ledger)
	s := &lcSync{lcFixture: f}
	s.sync = runner.NewWorkspaceSync(runner.SyncDeps{
		Checkpoints: store, Homes: homes, Leases: f.leases, Ledger: ledger, Local: f.local,
		Events: runner.NewEventRecorder(&memoryEventLog{}, &recordingBus{}, 100, time.Now), Status: status,
		Clock: time.Now,
	})
	s.baseline = runner.NewWorkspaceBaselineInitializer(runner.BaselineDeps{
		Checkpoints: store, Ledger: ledger, Leases: f.leases, Homes: homes, Local: f.local,
	})
	job := runner.Job{ID: uuid.New(), Key: runner.JobKey{RunID: f.run, InvocationID: uuid.New(), OperationID: "edit", Attempt: 1},
		NodeID: "edit", Op: runner.OpAgent, RunnerID: runner.BuiltInRunnerID, Generation: 1, State: runner.JobRunning}
	s.asg = runner.Assignment{Job: job}
	s.att = runner.WorkspaceAttempt{RunID: f.run, WorkDir: "/srv/ws/instances/" + f.run.String(), Runner: runner.BuiltInRunnerID, Resource: f.claim}
	return s
}

// Happy path through the hook: the receipt carries the prepared local ref.
func TestWorkspaceSync_NonPortableSuccessCarriesALocalCheckpointReceipt(t *testing.T) {
	s := newLCSync(t)
	s.nonPortable(2)
	c, err := s.sync.FinishSuccess(context.Background(), s.asg, s.att)
	require.NoError(t, err)
	assert.True(t, c.NonPortable)
	assert.Empty(t, c.Ref)
	assert.Equal(t, s.claim, c.Resource)
	ref, err := runner.ParseLocalCheckpointRef(c.LocalRef)
	require.NoError(t, err)
	assert.Equal(t, int64(3), ref.Version)
	assert.Equal(t, []string{s.att.WorkDir}, s.local.stagedFrom, "SINK: the actual instance workspace was copied")
}

// The first promisor miss mid-run marks the home and still takes the copy.
func TestWorkspaceSync_PromisorMissOnAPortableHomeTakesTheFirstLocalCheckpoint(t *testing.T) {
	s := newLCSync(t)
	c, err := s.sync.FinishSuccess(context.Background(), s.asg, s.att)
	require.NoError(t, err)
	assert.Equal(t, runner.NonPortablePromisorReason, s.homes.homes[s.run].NonPortableReason)
	ref, err := runner.ParseLocalCheckpointRef(c.LocalRef)
	require.NoError(t, err)
	assert.Equal(t, int64(1), ref.Version)
}

// Failure: copy fails -> FinishSuccess errors with NO receipt; the step is not done.
func TestWorkspaceSync_NonPortableLocalCopyFailureHasNoReceipt(t *testing.T) {
	s := newLCSync(t)
	s.nonPortable(1)
	s.local.stageErr = errors.New("no space left on device")
	c, err := s.sync.FinishSuccess(context.Background(), s.asg, s.att)
	require.Error(t, err)
	assert.Equal(t, engine.WorkspaceCompletion{}, c)
}

// A failed step on a non-portable home rolls back to its committed local copy.
func TestWorkspaceSync_NonPortableFailureRestoresTheLocalCheckpoint(t *testing.T) {
	s := newLCSync(t)
	s.nonPortable(4)
	require.NoError(t, s.sync.FinishFailure(context.Background(), s.asg, s.att))
	assert.Equal(t, []string{s.homes.homes[s.run].LocalCheckpoint + "|" + s.att.WorkDir}, s.local.restored)
}

// The recovery restorer PoolRecovery uses (P3 T10) restores the local copy.
func TestWorkspaceSync_RestoreBuiltInNonPortableUsesTheLocalCheckpoint(t *testing.T) {
	s := newLCSync(t)
	s.nonPortable(6)
	require.NoError(t, s.sync.RestoreBuiltIn(context.Background(), s.run, s.att.WorkDir))
	assert.Equal(t, []string{s.homes.homes[s.run].LocalCheckpoint + "|" + s.att.WorkDir}, s.local.restored)

	t2 := newLCSync(t)
	t2.nonPortable(0)
	err := t2.sync.RestoreBuiltIn(context.Background(), t2.run, t2.att.WorkDir)
	require.ErrorIs(t, err, runner.ErrNoRecoveryBase, "no base at all is the recovery blocker, never a dirty re-run")
}

// Edge: a run non-portable from the start bootstraps its initial local copy.
func TestBaseline_PromisorMissingCommitsALocalInitialState(t *testing.T) {
	s := newLCSync(t)
	require.NoError(t, s.baseline.EnsureBaseline(context.Background(), s.run, s.att.WorkDir))
	assert.Equal(t, runner.NonPortablePromisorReason, s.homes.homes[s.run].NonPortableReason)
	require.Len(t, s.homes.boots, 1)
	ref, err := runner.ParseLocalCheckpointRef(s.homes.boots[0])
	require.NoError(t, err)
	assert.Equal(t, int64(1), ref.Version)

	require.NoError(t, s.baseline.EnsureBaseline(context.Background(), s.run, s.att.WorkDir))
	assert.Len(t, s.local.staged, 1, "an existing local base is reused, never re-snapshotted")
}

func TestBaseline_LocalInitialStateFailurePreventsDispatch(t *testing.T) {
	s := newLCSync(t)
	s.local.stageErr = errors.New("permission denied")
	require.Error(t, s.baseline.EnsureBaseline(context.Background(), s.run, s.att.WorkDir))
}
