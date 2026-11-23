package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var ledgerNow = time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC)

type ledgerFixture struct {
	ledger *RunnerLedgerRepo
	reg    *RunnerRegistryRepo
	db     testDB
	src    runner.SourceID
}

func newLedgerFixture(t *testing.T) ledgerFixture {
	t.Helper()
	db := setupTestDB(t)
	l := NewRunnerLedgerRepo(db.DB)
	l.now = func() time.Time { return ledgerNow }
	reg := NewRunnerRegistryRepo(db.DB)
	reg.now = func() time.Time { return ledgerNow }
	ctx := context.Background()
	require.NoError(t, reg.UpsertRunner(ctx, runner.Runner{ID: runner.BuiltInRunnerID, Name: "builtin", BuiltIn: true, StepLimit: 8, Liveness: runner.LivenessOnline}))
	src := runner.MakeSourceID(runner.BuiltInRunnerID, runner.CredAnthropicSubscription, "")
	require.NoError(t, reg.ReplaceSources(ctx, runner.BuiltInRunnerID, []runner.Source{{ID: src, RunnerID: runner.BuiltInRunnerID, Kind: runner.CredAnthropicSubscription, Families: []modelidentity.Family{"anthropic"}, Backends: []string{"claude-code"}, Health: runner.SourceHealth{Auth: runner.AuthOK, Capacity: runner.CapacityAvailable}}}))
	return ledgerFixture{ledger: l, reg: reg, db: db, src: src}
}

func anthropicRequirements(run uuid.UUID) runner.ExecRequirements {
	return runner.ExecRequirements{Group: runner.ModelGroup{Kind: runner.CredAnthropicSubscription, Family: "anthropic"},
		Model: "claude-sonnet-4-5", Backend: "claude-code", Op: runner.OpAgent, Affinity: runner.AffinityBuiltIn, RunID: run}
}

func newKey() runner.JobKey {
	return runner.JobKey{RunID: uuid.New(), InvocationID: uuid.New(), OperationID: "node", Attempt: 1}
}

func (f ledgerFixture) decision(src runner.SourceID) runner.Decision {
	return runner.Decision{Placed: true, Runner: runner.BuiltInRunnerID, Source: src, Reason: "rank: runner=builtin"}
}

func (f ledgerFixture) slots(t *testing.T) int {
	t.Helper()
	snap, err := f.reg.Snapshot(context.Background())
	require.NoError(t, err)
	return snap.SlotsInUse[runner.BuiltInRunnerID]
}

func TestLedger_EnsureIntentIsIdempotentOnKey(t *testing.T) {
	f := newLedgerFixture(t)
	ctx := context.Background()
	key := newKey()
	a, err := f.ledger.EnsureIntent(ctx, key, "draft", anthropicRequirements(key.RunID))
	require.NoError(t, err)
	b, err := f.ledger.EnsureIntent(ctx, key, "draft", anthropicRequirements(key.RunID))
	require.NoError(t, err)
	assert.Equal(t, a.ID, b.ID)
	assert.Equal(t, runner.JobQueued, a.State)
	assert.Equal(t, int64(0), a.Generation)
	assert.Equal(t, key, a.Key)
	assert.Equal(t, "draft", a.NodeID)
	assert.Equal(t, runner.OpAgent, a.Op)
	assert.Equal(t, "anthropic_subscription:anthropic", a.Group.String())
	assert.Equal(t, "claude-sonnet-4-5", a.Model)

	var n int
	require.NoError(t, f.db.DB.QueryRow(`SELECT COUNT(*) FROM runner_jobs`).Scan(&n))
	assert.Equal(t, 1, n)

	_, err = f.ledger.EnsureIntent(ctx, runner.JobKey{RunID: key.RunID}, "draft", anthropicRequirements(key.RunID))
	assert.Error(t, err, "an incomplete key is refused")
}

func TestLedger_LoopRevisitsAndChildPrepGetDistinctKeys(t *testing.T) {
	f := newLedgerFixture(t)
	ctx := context.Background()
	run := uuid.New()
	visit1 := runner.JobKey{RunID: run, InvocationID: uuid.New(), OperationID: "node", Attempt: 1}
	visit2 := runner.JobKey{RunID: run, InvocationID: uuid.New(), OperationID: "node", Attempt: 1}
	childPrep := runner.JobKey{RunID: run, InvocationID: visit1.InvocationID, OperationID: "node/child-input", Attempt: 1}
	retry := runner.JobKey{RunID: run, InvocationID: visit1.InvocationID, OperationID: "node", Attempt: 2}
	seen := map[uuid.UUID]bool{}
	for _, k := range []runner.JobKey{visit1, visit2, childPrep, retry} {
		j, err := f.ledger.EnsureIntent(ctx, k, "spawn", anthropicRequirements(run))
		require.NoError(t, err)
		seen[j.ID] = true
	}
	assert.Len(t, seen, 4)
	jobs, err := f.ledger.ListByRun(ctx, run)
	require.NoError(t, err)
	assert.Len(t, jobs, 4)
}

func TestLedger_QueuedIntentsHoldNoSlot(t *testing.T) {
	f := newLedgerFixture(t)
	ctx := context.Background()
	var jobs []runner.Job
	for i := 0; i < 12; i++ {
		k := newKey()
		j, err := f.ledger.EnsureIntent(ctx, k, "n", anthropicRequirements(k.RunID))
		require.NoError(t, err)
		jobs = append(jobs, j)
	}
	assert.Equal(t, 0, f.slots(t), "twelve waiting intents use no slots")
	for i := 0; i < 8; i++ {
		_, err := f.ledger.Reserve(ctx, jobs[i].ID, f.decision(f.src), 8, ledgerNow.Add(time.Minute))
		require.NoError(t, err)
	}
	assert.Equal(t, 8, f.slots(t))
	_, err := f.ledger.Reserve(ctx, jobs[8].ID, f.decision(f.src), 8, ledgerNow.Add(time.Minute))
	assert.ErrorIs(t, err, runner.ErrSlotTaken, "the ninth reservation waits")
	still, err := f.ledger.Get(ctx, jobs[8].ID)
	require.NoError(t, err)
	assert.Equal(t, runner.JobQueued, still.State)
	assert.Equal(t, int64(0), still.Generation, "a losing reservation changes nothing")
}

func TestLedger_ReserveSetsAssignmentAndAttempt(t *testing.T) {
	f := newLedgerFixture(t)
	ctx := context.Background()
	k := newKey()
	j, err := f.ledger.EnsureIntent(ctx, k, "n", anthropicRequirements(k.RunID))
	require.NoError(t, err)
	reason := "waiting · capacity: no eligible source for anthropic_subscription:anthropic"
	require.NoError(t, f.ledger.SetWait(ctx, j.ID, &runner.WaitReason{Kind: runner.WaitCapacity, Group: j.Group, Model: j.Model, Detail: reason}))
	waiting, err := f.ledger.Get(ctx, j.ID)
	require.NoError(t, err)
	require.NotNil(t, waiting.Wait)
	assert.Equal(t, runner.WaitCapacity, waiting.Wait.Kind)
	assert.Equal(t, reason, waiting.Wait.Detail)

	lease := ledgerNow.Add(30 * time.Second)
	got, err := f.ledger.Reserve(ctx, j.ID, f.decision(f.src), 8, lease)
	require.NoError(t, err)
	assert.Equal(t, runner.JobOffered, got.State)
	assert.Equal(t, int64(1), got.Generation)
	assert.Equal(t, runner.BuiltInRunnerID, got.RunnerID)
	assert.Equal(t, f.src, got.SourceID)
	assert.Equal(t, "rank: runner=builtin", got.Reason)
	assert.Nil(t, got.Wait, "reservation clears the wait")
	require.NotNil(t, got.LeaseUntil)
	assert.Equal(t, lease, *got.LeaseUntil)

	attempts, err := f.ledger.Attempts(ctx, j.ID)
	require.NoError(t, err)
	require.Len(t, attempts, 1)
	assert.Equal(t, runner.Attempt{JobID: j.ID, Generation: 1, RunnerID: runner.BuiltInRunnerID, SourceID: f.src, Reason: "rank: runner=builtin", StartedAt: ledgerNow}, attempts[0])

	_, err = f.ledger.Reserve(ctx, j.ID, f.decision(f.src), 8, lease)
	assert.ErrorIs(t, err, runner.ErrFenced, "only a queued job can be reserved")
	_, err = f.ledger.Reserve(ctx, uuid.New(), f.decision(f.src), 8, lease)
	assert.ErrorIs(t, err, runner.ErrJobNotFound)
	_, err = f.ledger.Reserve(ctx, j.ID, runner.Decision{}, 8, lease)
	assert.Error(t, err, "a wait decision cannot be reserved")
	assert.ErrorIs(t, f.ledger.SetWait(ctx, j.ID, nil), runner.ErrFenced, "only queued intents carry a wait")
}

func TestLedger_ProcessReservationHasNoSource(t *testing.T) {
	f := newLedgerFixture(t)
	ctx := context.Background()
	k := newKey()
	j, err := f.ledger.EnsureIntent(ctx, k, "script", runner.ExecRequirements{Group: runner.ModelGroup{Kind: runner.CredNone}, Op: runner.OpProcess, Affinity: runner.AffinityBuiltIn, RunID: k.RunID})
	require.NoError(t, err)
	got, err := f.ledger.Reserve(ctx, j.ID, f.decision(""), 8, ledgerNow)
	require.NoError(t, err)
	assert.Equal(t, runner.SourceID(""), got.SourceID)
	assert.Equal(t, 1, f.slots(t))
}

func TestLedger_RequeueBumpsGenerationAndReleasesSlot(t *testing.T) {
	f := newLedgerFixture(t)
	ctx := context.Background()
	k := newKey()
	j, err := f.ledger.EnsureIntent(ctx, k, "n", anthropicRequirements(k.RunID))
	require.NoError(t, err)
	offered, err := f.ledger.Reserve(ctx, j.ID, f.decision(f.src), 8, ledgerNow)
	require.NoError(t, err)
	require.NoError(t, f.ledger.MarkRunning(ctx, offered.Fence()))
	assert.Equal(t, 1, f.slots(t))

	re, err := f.ledger.Requeue(ctx, offered.Fence(), runner.OutcomeExhausted)
	require.NoError(t, err)
	assert.Equal(t, runner.JobQueued, re.State)
	assert.Equal(t, int64(2), re.Generation)
	assert.Equal(t, runner.RunnerID(""), re.RunnerID)
	assert.Equal(t, runner.SourceID(""), re.SourceID)
	assert.Equal(t, 0, f.slots(t), "re-placement closes the prior assignment first")
	assert.ErrorIs(t, f.ledger.FenceCheck(ctx, offered.Fence()), runner.ErrFenced, "the old generation is stale at once")
	_, err = f.ledger.Requeue(ctx, offered.Fence(), runner.OutcomeExhausted)
	assert.ErrorIs(t, err, runner.ErrFenced, "a stale requeue is refused")
}

func TestLedger_FenceCheckIsTheSingleRule(t *testing.T) {
	f := newLedgerFixture(t)
	ctx := context.Background()
	k := newKey()
	j, err := f.ledger.EnsureIntent(ctx, k, "n", anthropicRequirements(k.RunID))
	require.NoError(t, err)
	assert.ErrorIs(t, f.ledger.FenceCheck(ctx, j.Fence()), runner.ErrFenced, "a queued job has no live assignment")
	offered, err := f.ledger.Reserve(ctx, j.ID, f.decision(f.src), 8, ledgerNow)
	require.NoError(t, err)
	good := offered.Fence()

	cases := map[string]struct {
		f    runner.Fence
		want error
	}{
		"current":           {good, nil},
		"stale generation":  {runner.Fence{JobID: good.JobID, RunnerID: good.RunnerID, Generation: good.Generation - 1}, runner.ErrFenced},
		"future generation": {runner.Fence{JobID: good.JobID, RunnerID: good.RunnerID, Generation: good.Generation + 1}, runner.ErrFenced},
		"wrong runner":      {runner.Fence{JobID: good.JobID, RunnerID: "mac-mini", Generation: good.Generation}, runner.ErrFenced},
		"unknown job":       {runner.Fence{JobID: uuid.New(), RunnerID: good.RunnerID, Generation: good.Generation}, runner.ErrJobNotFound},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := f.ledger.FenceCheck(ctx, c.f)
			if c.want == nil {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, c.want)
			// Every fenced mutation reports through the same rule.
			assert.ErrorIs(t, f.ledger.MarkRunning(ctx, c.f), c.want)
			assert.ErrorIs(t, f.ledger.ExtendLease(ctx, c.f, ledgerNow), c.want)
			assert.ErrorIs(t, f.ledger.Complete(ctx, c.f, "x"), c.want)
			assert.ErrorIs(t, f.ledger.Fail(ctx, c.f, runner.OutcomeOther), c.want)
		})
	}

	require.NoError(t, f.ledger.Complete(ctx, good, good.JobID.String()))
	assert.ErrorIs(t, f.ledger.FenceCheck(ctx, good), runner.ErrFenced, "terminal jobs are fenced")
	assert.ErrorIs(t, f.ledger.MarkRunning(ctx, good), runner.ErrFenced)
}

func TestLedger_DuplicateCompleteIsANoOp(t *testing.T) {
	f := newLedgerFixture(t)
	ctx := context.Background()
	k := newKey()
	j, err := f.ledger.EnsureIntent(ctx, k, "n", anthropicRequirements(k.RunID))
	require.NoError(t, err)
	offered, err := f.ledger.Reserve(ctx, j.ID, f.decision(f.src), 8, ledgerNow)
	require.NoError(t, err)
	require.NoError(t, f.ledger.MarkRunning(ctx, offered.Fence()))
	require.NoError(t, f.ledger.Complete(ctx, offered.Fence(), j.ID.String()))
	require.NoError(t, f.ledger.Complete(ctx, offered.Fence(), j.ID.String()), "duplicate completion is acknowledged")
	assert.Equal(t, 0, f.slots(t), "and never double-counts")

	done, err := f.ledger.Get(ctx, j.ID)
	require.NoError(t, err)
	assert.Equal(t, runner.JobCompleted, done.State)
	assert.Equal(t, j.ID.String(), done.ResultRef)
	attempts, err := f.ledger.Attempts(ctx, j.ID)
	require.NoError(t, err)
	require.Len(t, attempts, 1)
	assert.Equal(t, "completed", attempts[0].Outcome)
	require.NotNil(t, attempts[0].EndedAt)

	assert.ErrorIs(t, f.ledger.Fail(ctx, offered.Fence(), runner.OutcomeOther), runner.ErrFenced, "a completed job cannot also fail")

	// Fail is likewise idempotent for its own fence.
	k2 := newKey()
	j2, err := f.ledger.EnsureIntent(ctx, k2, "n", anthropicRequirements(k2.RunID))
	require.NoError(t, err)
	o2, err := f.ledger.Reserve(ctx, j2.ID, f.decision(f.src), 8, ledgerNow)
	require.NoError(t, err)
	require.NoError(t, f.ledger.Fail(ctx, o2.Fence(), runner.OutcomeTransient))
	require.NoError(t, f.ledger.Fail(ctx, o2.Fence(), runner.OutcomeTransient))
	failed, err := f.ledger.Attempts(ctx, j2.ID)
	require.NoError(t, err)
	assert.Equal(t, "transient", failed[0].Outcome)
}

func TestLedger_AttemptsHistoryAcrossReplacement(t *testing.T) {
	f := newLedgerFixture(t)
	ctx := context.Background()
	other := runner.MakeSourceID(runner.BuiltInRunnerID, runner.CredAnthropicSubscription, "other")
	original, err := f.reg.GetSource(ctx, f.src)
	require.NoError(t, err)
	replacement := original
	replacement.ID, replacement.CredentialRef = other, "other"
	require.NoError(t, f.reg.ReplaceSources(ctx, runner.BuiltInRunnerID, []runner.Source{original, replacement}))
	k := newKey()
	j, err := f.ledger.EnsureIntent(ctx, k, "n", anthropicRequirements(k.RunID))
	require.NoError(t, err)
	first, err := f.ledger.Reserve(ctx, j.ID, f.decision(f.src), 8, ledgerNow)
	require.NoError(t, err)
	_, err = f.ledger.Requeue(ctx, first.Fence(), runner.OutcomeExhausted)
	require.NoError(t, err)
	second, err := f.ledger.Reserve(ctx, j.ID, f.decision(other), 8, ledgerNow)
	require.NoError(t, err)
	assert.Equal(t, int64(3), second.Generation)
	require.NoError(t, f.ledger.Complete(ctx, second.Fence(), j.ID.String()))

	attempts, err := f.ledger.Attempts(ctx, j.ID)
	require.NoError(t, err)
	require.Len(t, attempts, 2)
	assert.Equal(t, int64(1), attempts[0].Generation)
	assert.Equal(t, f.src, attempts[0].SourceID)
	assert.Equal(t, "exhausted", attempts[0].Outcome)
	assert.Equal(t, int64(3), attempts[1].Generation)
	assert.Equal(t, other, attempts[1].SourceID)
	assert.Equal(t, "completed", attempts[1].Outcome)
	assert.Equal(t, k, second.Key, "re-placement never changes the logical operation")
}

func TestLedger_EnsureIntentReopensTerminalJobForReExecution(t *testing.T) {
	f := newLedgerFixture(t)
	ctx := context.Background()
	k := newKey()
	j, err := f.ledger.EnsureIntent(ctx, k, "n", anthropicRequirements(k.RunID))
	require.NoError(t, err)
	o, err := f.ledger.Reserve(ctx, j.ID, f.decision(f.src), 8, ledgerNow)
	require.NoError(t, err)
	require.NoError(t, f.ledger.Fail(ctx, o.Fence(), runner.OutcomeOther))

	again, err := f.ledger.EnsureIntent(ctx, k, "n", anthropicRequirements(k.RunID))
	require.NoError(t, err)
	assert.Equal(t, j.ID, again.ID, "same logical operation")
	assert.Equal(t, runner.JobQueued, again.State)
	assert.Equal(t, int64(2), again.Generation, "the failed generation stays fenced")
	assert.Equal(t, runner.RunnerID(""), again.RunnerID)
	assert.ErrorIs(t, f.ledger.FenceCheck(ctx, o.Fence()), runner.ErrFenced)
}

func TestLedger_ListsAndLookups(t *testing.T) {
	f := newLedgerFixture(t)
	ctx := context.Background()
	run := uuid.New()
	k1 := runner.JobKey{RunID: run, InvocationID: uuid.New(), OperationID: "node", Attempt: 1}
	k2 := runner.JobKey{RunID: run, InvocationID: uuid.New(), OperationID: "node", Attempt: 1}
	j1, err := f.ledger.EnsureIntent(ctx, k1, "a", anthropicRequirements(run))
	require.NoError(t, err)
	j2, err := f.ledger.EnsureIntent(ctx, k2, "b", anthropicRequirements(run))
	require.NoError(t, err)
	o1, err := f.ledger.Reserve(ctx, j1.ID, f.decision(f.src), 8, ledgerNow)
	require.NoError(t, err)

	got, found, err := f.ledger.FindByKey(ctx, k2)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, j2.ID, got.ID)
	_, found, err = f.ledger.FindByKey(ctx, newKey())
	require.NoError(t, err)
	assert.False(t, found)

	active, err := f.ledger.ListActive(ctx)
	require.NoError(t, err)
	assert.Len(t, active, 2)
	byRunner, err := f.ledger.ListActiveByRunner(ctx, runner.BuiltInRunnerID)
	require.NoError(t, err)
	require.Len(t, byRunner, 1, "only assigned jobs belong to a runner")
	assert.Equal(t, j1.ID, byRunner[0].ID)

	require.NoError(t, f.ledger.Complete(ctx, o1.Fence(), j1.ID.String()))
	active, err = f.ledger.ListActive(ctx)
	require.NoError(t, err)
	require.Len(t, active, 1)
	assert.Equal(t, j2.ID, active[0].ID)

	_, err = f.ledger.Get(ctx, uuid.New())
	assert.True(t, errors.Is(err, runner.ErrJobNotFound))
}
