package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ runner.IntentAbandoner = (*RunnerLedgerRepo)(nil)

func TestLedger_AbandonReleasesQueuedIntent(t *testing.T) {
	f := newLedgerFixture(t)
	ctx := context.Background()
	k := newKey()
	j, err := f.ledger.EnsureIntent(ctx, k, "draft", anthropicRequirements(k.RunID))
	require.NoError(t, err)
	group := runner.ModelGroup{Kind: runner.CredAnthropicSubscription, Family: "anthropic"}
	require.NoError(t, f.ledger.SetWait(ctx, j.ID, &runner.WaitReason{Kind: runner.WaitCapacity, Group: group, Detail: "waiting · capacity"}))

	require.NoError(t, f.ledger.Abandon(ctx, j.ID))
	got, err := f.ledger.Get(ctx, j.ID)
	require.NoError(t, err)
	assert.Equal(t, runner.JobFailed, got.State)
	assert.Nil(t, got.Wait, "an abandoned intent never shows a stale wait")
	active, err := f.ledger.ListActive(ctx)
	require.NoError(t, err)
	assert.Empty(t, active)
	attempts, err := f.ledger.Attempts(ctx, j.ID)
	require.NoError(t, err)
	assert.Empty(t, attempts, "nothing was ever offered")

	again, err := f.ledger.EnsureIntent(ctx, k, "draft", anthropicRequirements(k.RunID))
	require.NoError(t, err)
	assert.Equal(t, j.ID, again.ID)
	assert.Equal(t, runner.JobQueued, again.State, "the same key re-opens on re-execution")
}

func TestLedger_AbandonLeavesNonQueuedJobsAlone(t *testing.T) {
	f := newLedgerFixture(t)
	ctx := context.Background()
	k := newKey()
	j, err := f.ledger.EnsureIntent(ctx, k, "draft", anthropicRequirements(k.RunID))
	require.NoError(t, err)
	offered, err := f.ledger.Reserve(ctx, j.ID, f.decision(f.src), 8, ledgerNow.Add(time.Minute))
	require.NoError(t, err)

	require.NoError(t, f.ledger.Abandon(ctx, j.ID))
	got, err := f.ledger.Get(ctx, j.ID)
	require.NoError(t, err)
	assert.Equal(t, runner.JobOffered, got.State, "an offered job is fenced, never abandoned")
	assert.NoError(t, f.ledger.FenceCheck(ctx, offered.Fence()))
	assert.Equal(t, 1, f.slots(t))

	assert.ErrorIs(t, f.ledger.Abandon(ctx, uuid.New()), runner.ErrJobNotFound)
}
