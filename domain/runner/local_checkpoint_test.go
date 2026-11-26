// domain/runner/local_checkpoint_test.go
package runner_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeLocalStore records every call; refs carry a hash derived from the version.
type fakeLocalStore struct {
	staged               []runner.LocalCheckpointRef
	stagedFrom           []string
	restored             []string // ref|dir
	pruned               []string // run|keep
	deleted              []uuid.UUID
	stageErr, restoreErr error
}

func lcHash(v int64) string { return fmt.Sprintf("%064x", v) }

func (f *fakeLocalStore) StageLocal(_ context.Context, run uuid.UUID, src string, v int64) (runner.LocalCheckpointRef, error) {
	if f.stageErr != nil {
		return runner.LocalCheckpointRef{}, f.stageErr
	}
	ref := runner.LocalCheckpointRef{RunID: run, Version: v, ManifestHash: lcHash(v)}
	f.staged, f.stagedFrom = append(f.staged, ref), append(f.stagedFrom, src)
	return ref, nil
}
func (f *fakeLocalStore) RestoreLocal(_ context.Context, ref runner.LocalCheckpointRef, dir string) error {
	f.restored = append(f.restored, ref.String()+"|"+dir)
	return f.restoreErr
}
func (f *fakeLocalStore) PruneLocal(_ context.Context, run uuid.UUID, keep runner.LocalCheckpointRef) error {
	f.pruned = append(f.pruned, run.String()+"|"+keep.String())
	return nil
}
func (f *fakeLocalStore) Delete(_ context.Context, run uuid.UUID) error {
	f.deleted = append(f.deleted, run)
	return nil
}

// lcHomes implements only what LocalCheckpoints calls; anything else panics
// through the nil embedded interface, so an unexpected dependency is loud.
type lcHomes struct {
	runner.HomeStore
	homes   map[uuid.UUID]runner.Home
	bootErr error
	winner  string // with bootErr: a concurrent initializer's base that committed first
	boots   []string
}

func (h *lcHomes) GetHome(_ context.Context, run uuid.UUID) (runner.Home, error) {
	if x, ok := h.homes[run]; ok {
		return x, nil
	}
	return runner.Home{RunID: run}, nil
}
func (h *lcHomes) BootstrapLocalCheckpoint(_ context.Context, run uuid.UUID, ref string) error {
	h.boots = append(h.boots, ref)
	x := h.homes[run]
	if h.bootErr != nil {
		if h.winner != "" {
			x.LocalCheckpoint = h.winner
			h.homes[run] = x
		}
		return h.bootErr
	}
	x.LocalCheckpoint = ref
	h.homes[run] = x
	return nil
}

type lcCatalog struct {
	runner.CheckpointStore
	latest   map[uuid.UUID]runner.CheckpointRef
	restored []string
}

func (c *lcCatalog) Latest(_ context.Context, run uuid.UUID) (runner.CheckpointRef, bool, error) {
	r, ok := c.latest[run]
	return r, ok, nil
}
func (c *lcCatalog) Restore(_ context.Context, ref runner.CheckpointRef, dir string) error {
	c.restored = append(c.restored, ref.String()+"|"+dir)
	return nil
}

type lcLeases struct {
	runner.ResourceLeaseStore
	err     error
	checked []engine.ResourceFenceRef
}

func (l *lcLeases) Check(_ context.Context, f engine.ResourceFenceRef) error {
	l.checked = append(l.checked, f)
	return l.err
}

type lcFixture struct {
	local   *fakeLocalStore
	homes   *lcHomes
	catalog *lcCatalog
	leases  *lcLeases
	svc     *runner.LocalCheckpoints
	run     uuid.UUID
	claim   engine.ResourceFenceRef
}

func newLCFixture(t *testing.T) *lcFixture {
	t.Helper()
	f := &lcFixture{
		local: &fakeLocalStore{}, homes: &lcHomes{homes: map[uuid.UUID]runner.Home{}},
		catalog: &lcCatalog{latest: map[uuid.UUID]runner.CheckpointRef{}}, leases: &lcLeases{}, run: uuid.New(),
	}
	f.claim = engine.ResourceFenceRef{Key: "run:" + f.run.String(), Owner: string(runner.BuiltInRunnerID), Generation: 1, Token: uuid.New()}
	svc, err := runner.NewLocalCheckpoints(runner.LocalCheckpointDeps{Local: f.local, Homes: f.homes, Checkpoints: f.catalog, Leases: f.leases})
	require.NoError(t, err)
	f.svc = svc
	return f
}

func (f *lcFixture) nonPortable(local int64) {
	h := runner.Home{RunID: f.run, NonPortableReason: runner.NonPortablePromisorReason}
	if local > 0 {
		h.LocalCheckpoint = runner.LocalCheckpointRef{RunID: f.run, Version: local, ManifestHash: lcHash(local)}.String()
	}
	f.homes.homes[f.run] = h
}

func TestNewLocalCheckpoints_RequiresEveryDependency(t *testing.T) {
	_, err := runner.NewLocalCheckpoints(runner.LocalCheckpointDeps{})
	require.EqualError(t, err, "runner: local checkpoints: missing Local, Homes, Checkpoints, Leases")
}

// Happy path: a completed step stages the successor of the committed copy,
// under the live claim, after reclaiming leftovers of an earlier attempt.
func TestLocalCheckpoints_TakeStagesTheNextVersionUnderTheClaim(t *testing.T) {
	f := newLCFixture(t)
	f.nonPortable(3)
	ref, err := f.svc.Take(context.Background(), f.run, "/ws/run", f.claim)
	require.NoError(t, err)
	assert.Equal(t, int64(4), ref.Version)
	assert.Equal(t, []string{"/ws/run"}, f.local.stagedFrom)
	assert.Equal(t, []engine.ResourceFenceRef{f.claim}, f.leases.checked)
	assert.Equal(t, []string{f.run.String() + "|" + f.homes.homes[f.run].LocalCheckpoint}, f.local.pruned, "keep the committed v3, drop leftovers")
}

// Edge: the first local checkpoint of a home that became non-portable mid-run.
func TestLocalCheckpoints_TakeOnTheFirstNonPortableStepStagesVersionOne(t *testing.T) {
	f := newLCFixture(t)
	f.nonPortable(0)
	ref, err := f.svc.Take(context.Background(), f.run, "/ws/run", f.claim)
	require.NoError(t, err)
	assert.Equal(t, int64(1), ref.Version)
}

// Failure: the copy fails -> no ref, so FinishSuccess has no receipt and the
// step is not done.
func TestLocalCheckpoints_TakeFailsWhenTheCopyFails(t *testing.T) {
	f := newLCFixture(t)
	f.nonPortable(1)
	f.local.stageErr = errors.New("no space left on device")
	ref, err := f.svc.Take(context.Background(), f.run, "/ws/run", f.claim)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no space left on device")
	assert.True(t, ref.IsZero())
}

// Error: a stale claim (fenced writer) never copies.
func TestLocalCheckpoints_TakeRefusesAStaleClaim(t *testing.T) {
	f := newLCFixture(t)
	f.nonPortable(1)
	f.leases.err = runner.ErrFenced
	_, err := f.svc.Take(context.Background(), f.run, "/ws/run", f.claim)
	require.ErrorIs(t, err, runner.ErrFenced)
	assert.Empty(t, f.local.staged)
}

func TestLocalCheckpoints_TakeRefusesAPortableHome(t *testing.T) {
	f := newLCFixture(t)
	_, err := f.svc.Take(context.Background(), f.run, "/ws/run", f.claim)
	require.Error(t, err)
	assert.Empty(t, f.local.staged)
}

// Edge: a run non-portable from its first preparation gets an initial state.
func TestLocalCheckpoints_BootstrapCommitsTheInitialState(t *testing.T) {
	f := newLCFixture(t)
	f.nonPortable(0)
	require.NoError(t, f.svc.Bootstrap(context.Background(), f.run, "/ws/run"))
	want := runner.LocalCheckpointRef{RunID: f.run, Version: 1, ManifestHash: lcHash(1)}.String()
	assert.Equal(t, []string{want}, f.homes.boots)

	require.NoError(t, f.svc.Bootstrap(context.Background(), f.run, "/ws/run"))
	assert.Len(t, f.local.staged, 1, "an existing committed base is never re-snapshotted")
}

func TestLocalCheckpoints_BootstrapLosingARaceKeepsTheWinner(t *testing.T) {
	f := newLCFixture(t)
	f.nonPortable(0)
	f.homes.bootErr = runner.ErrCheckpointConflict
	require.ErrorIs(t, f.svc.Bootstrap(context.Background(), f.run, "/ws/run"), runner.ErrCheckpointConflict,
		"a conflict with no committed winner surfaces")

	g := newLCFixture(t)
	g.nonPortable(0)
	g.homes.bootErr = runner.ErrCheckpointConflict
	g.homes.winner = runner.LocalCheckpointRef{RunID: g.run, Version: 1, ManifestHash: lcHash(9)}.String()
	require.NoError(t, g.svc.Bootstrap(context.Background(), g.run, "/ws/run"), "a concurrently committed base stands")
	assert.Equal(t, g.homes.winner, g.homes.homes[g.run].LocalCheckpoint)
}

func TestLocalCheckpoints_BootstrapCopyFailureFailsPreparation(t *testing.T) {
	f := newLCFixture(t)
	f.nonPortable(0)
	f.local.stageErr = errors.New("permission denied")
	require.Error(t, f.svc.Bootstrap(context.Background(), f.run, "/ws/run"))
	assert.Empty(t, f.homes.boots)
}

// Restore happy path: the latest committed local checkpoint, then retention.
func TestLocalCheckpoints_RestoreUsesTheLatestLocalCheckpoint(t *testing.T) {
	f := newLCFixture(t)
	f.nonPortable(5)
	f.catalog.latest[f.run] = runner.CheckpointRef{RunID: f.run, Version: 2, ManifestHash: "old"}
	require.NoError(t, f.svc.Restore(context.Background(), f.run, "/ws/run"))
	cur := f.homes.homes[f.run].LocalCheckpoint
	assert.Equal(t, []string{cur + "|/ws/run"}, f.local.restored)
	assert.Empty(t, f.catalog.restored, "an older portable checkpoint is never used while a local one exists")
	assert.Equal(t, []string{f.run.String() + "|" + cur}, f.local.pruned, "the interrupted step's in-progress copy is reclaimed")
}

// Edge: interrupted on the first non-portable step -> the initial state (the
// last committed portable checkpoint) is restored.
func TestLocalCheckpoints_RestoreWithoutALocalCheckpointUsesTheInitialState(t *testing.T) {
	f := newLCFixture(t)
	f.nonPortable(0)
	base := runner.CheckpointRef{RunID: f.run, Version: 2, ManifestHash: "base"}
	f.catalog.latest[f.run] = base
	require.NoError(t, f.svc.Restore(context.Background(), f.run, "/ws/run"))
	assert.Equal(t, []string{base.String() + "|/ws/run"}, f.catalog.restored)
	assert.Empty(t, f.local.restored)
}

// Edge: no base at all -> the recovery blocker, naming the visible reason.
func TestLocalCheckpoints_RestoreWithNoBaseIsTheRecoveryBlocker(t *testing.T) {
	f := newLCFixture(t)
	f.nonPortable(0)
	err := f.svc.Restore(context.Background(), f.run, "/ws/run")
	require.ErrorIs(t, err, runner.ErrNoRecoveryBase)
	assert.Contains(t, err.Error(), runner.NonPortablePromisorReason)
	assert.Empty(t, f.local.restored)
	assert.Empty(t, f.catalog.restored)
}

// Error: a missing/corrupt committed local copy is the blocker too, never a
// silent rollback to an older portable checkpoint.
func TestLocalCheckpoints_UnusableLocalCheckpointIsTheBlockerNotAnOlderRollback(t *testing.T) {
	for _, cause := range []error{runner.ErrCheckpointNotFound, runner.ErrCorruptCheckpoint} {
		f := newLCFixture(t)
		f.nonPortable(2)
		f.catalog.latest[f.run] = runner.CheckpointRef{RunID: f.run, Version: 1, ManifestHash: "older"}
		f.local.restoreErr = cause
		err := f.svc.Restore(context.Background(), f.run, "/ws/run")
		require.ErrorIs(t, err, runner.ErrNoRecoveryBase)
		require.ErrorIs(t, err, cause)
		assert.Empty(t, f.catalog.restored)
	}
	f := newLCFixture(t)
	f.nonPortable(2)
	f.local.restoreErr = errors.New("input/output error")
	err := f.svc.Restore(context.Background(), f.run, "/ws/run")
	require.Error(t, err)
	assert.False(t, errors.Is(err, runner.ErrNoRecoveryBase), "a transient I/O error is an ordinary recovery error")
}

// Retention through the committer decorator.
type lcCommitter struct {
	err     error
	commits []engine.StepCommit
}

func (c *lcCommitter) CommitStep(_ context.Context, s engine.StepCommit) error {
	c.commits = append(c.commits, s)
	return c.err
}
func (c *lcCommitter) CommittedResult(context.Context, uuid.UUID) (*engine.StepCommit, bool, error) {
	return nil, false, nil
}

func TestLocalCheckpointRetention_KeepsOnlyTheCommittedCopy(t *testing.T) {
	inner, local := &lcCommitter{}, &fakeLocalStore{}
	r, err := runner.NewLocalCheckpointRetention(inner, local)
	require.NoError(t, err)
	run := uuid.New()
	ref := runner.LocalCheckpointRef{RunID: run, Version: 4, ManifestHash: lcHash(4)}
	require.NoError(t, r.CommitStep(context.Background(), engine.StepCommit{RunID: run, Completions: []engine.StepCompletion{
		{Workspace: &engine.WorkspaceCompletion{NonPortable: true, LocalRef: ref.String()}},
	}}))
	require.Len(t, inner.commits, 1)
	assert.Equal(t, []string{run.String() + "|" + ref.String()}, local.pruned)

	require.NoError(t, r.CommitStep(context.Background(), engine.StepCommit{RunID: run, Completions: []engine.StepCompletion{
		{Workspace: &engine.WorkspaceCompletion{Ref: run.String() + "@9:" + strings.Repeat("d", 64)}},
	}}))
	assert.Equal(t, []uuid.UUID{run}, local.deleted, "a portable checkpoint supersedes every local copy")
}

func TestLocalCheckpointRetention_FailedCommitKeepsEveryCopy(t *testing.T) {
	inner, local := &lcCommitter{err: engine.ErrStepPersistence}, &fakeLocalStore{}
	r, err := runner.NewLocalCheckpointRetention(inner, local)
	require.NoError(t, err)
	run := uuid.New()
	ref := runner.LocalCheckpointRef{RunID: run, Version: 2, ManifestHash: lcHash(2)}
	err = r.CommitStep(context.Background(), engine.StepCommit{RunID: run, Completions: []engine.StepCompletion{
		{Workspace: &engine.WorkspaceCompletion{NonPortable: true, LocalRef: ref.String()}},
	}})
	require.ErrorIs(t, err, engine.ErrStepPersistence)
	assert.Empty(t, local.pruned, "the previous committed copy is the recovery base until a commit succeeds")
	_, err = runner.NewLocalCheckpointRetention(nil, local)
	require.Error(t, err)
}
