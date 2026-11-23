package sqlite

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLedger_StaleDecisionAfterCordonOrLimitChange(t *testing.T) {
	for _, change := range []string{"cordon", "limit"} {
		t.Run(change, func(t *testing.T) {
			f := newLedgerFixture(t)
			ctx := context.Background()
			k := newKey()
			j, err := f.ledger.EnsureIntent(ctx, k, "n", anthropicRequirements(k.RunID))
			require.NoError(t, err)
			decision := placementDecision(t, f, anthropicRequirements(k.RunID))
			if change == "cordon" {
				require.NoError(t, f.reg.SetCordoned(ctx, runner.BuiltInRunnerID, true))
			} else {
				occupiedKey := newKey()
				occupied, e := f.ledger.EnsureIntent(ctx, occupiedKey, "occupied", anthropicRequirements(occupiedKey.RunID))
				require.NoError(t, e)
				_, e = f.ledger.Reserve(ctx, occupied.ID, f.decision(f.src), 8, ledgerNow)
				require.NoError(t, e)
				_, err = f.db.DB.Exec(`UPDATE runners SET step_limit=1 WHERE id=?`, string(runner.BuiltInRunnerID))
				require.NoError(t, err)
			}
			_, err = f.ledger.Reserve(ctx, j.ID, decision, 8, ledgerNow)
			assert.ErrorIs(t, err, runner.ErrSlotTaken)
			if change == "limit" {
				got, e := f.ledger.Get(ctx, j.ID)
				require.NoError(t, e)
				assert.Equal(t, runner.JobQueued, got.State)
				assert.Zero(t, got.Generation)
				attempts, e := f.ledger.Attempts(ctx, j.ID)
				require.NoError(t, e)
				assert.Empty(t, attempts)
				assert.Equal(t, 1, f.slots(t))
			} else {
				assertQueuedWithoutAttempt(t, f, j)
			}
		})
	}
}

func assertQueuedWithoutAttempt(t *testing.T, f ledgerFixture, j runner.Job) {
	t.Helper()
	got, err := f.ledger.Get(context.Background(), j.ID)
	require.NoError(t, err)
	assert.Equal(t, runner.JobQueued, got.State)
	assert.Zero(t, got.Generation)
	a, err := f.ledger.Attempts(context.Background(), j.ID)
	require.NoError(t, err)
	assert.Empty(t, a)
	assert.Zero(t, f.slots(t))
}

func TestLedger_StaleDecisionAfterSourceLogoutOrRemoval(t *testing.T) {
	testStaleSource(t, []string{"logout", "removal"})
}

func testStaleSource(t *testing.T, changes []string) {
	t.Helper()
	for _, change := range changes {
		t.Run(change, func(t *testing.T) {
			f := newLedgerFixture(t)
			ctx := context.Background()
			k := newKey()
			j, err := f.ledger.EnsureIntent(ctx, k, "n", anthropicRequirements(k.RunID))
			require.NoError(t, err)
			decision := placementDecision(t, f, anthropicRequirements(k.RunID))
			query := `UPDATE runner_sources SET auth='logged_out' WHERE id=?`
			if change == "removal" {
				query = `DELETE FROM runner_sources WHERE id=?`
			}
			if change == "unavailable" {
				query = `UPDATE runner_sources SET unavailable_reason='missing harness' WHERE id=?`
			}
			_, err = f.db.DB.Exec(query, string(f.src))
			require.NoError(t, err)
			_, err = f.ledger.Reserve(ctx, j.ID, decision, 8, ledgerNow)
			assert.ErrorIs(t, err, runner.ErrSlotTaken)
			assertQueuedWithoutAttempt(t, f, j)
		})
	}
}

func TestLedger_UnavailableHarnessRefused(t *testing.T) {
	testStaleSource(t, []string{"unavailable"})
}

func placementDecision(t *testing.T, f ledgerFixture, req runner.ExecRequirements) runner.Decision {
	t.Helper()
	snap, err := f.reg.Snapshot(context.Background())
	require.NoError(t, err)
	decision := runner.Place(req, snap, runner.PlacementPolicy{})
	require.True(t, decision.Placed)
	require.Equal(t, f.src, decision.Source)
	return decision
}

func TestLedger_RequirementsRoundTripAndCorruption(t *testing.T) {
	f := newLedgerFixture(t)
	ctx := context.Background()
	k := newKey()
	req := anthropicRequirements(k.RunID)
	req.Provider = "anthropic"
	req.EndpointFingerprint = "route"
	req.ContextWindow = provider.ContextWindow("large")
	req.Effort = provider.Effort("high")
	req.Tools = []string{"read"}
	req.RequireExecutedModel = true
	j, err := f.ledger.EnsureIntent(ctx, k, "n", req)
	require.NoError(t, err)
	var raw string
	require.NoError(t, f.db.DB.QueryRow(`SELECT requirements FROM runner_jobs WHERE id=?`, j.ID.String()).Scan(&raw))
	var got runner.ExecRequirements
	require.NoError(t, json.Unmarshal([]byte(raw), &got))
	assert.Equal(t, req, got)
	_, err = f.db.DB.Exec(`UPDATE runner_jobs SET requirements='{' WHERE id=?`, j.ID.String())
	require.NoError(t, err)
	_, err = f.ledger.Reserve(ctx, j.ID, f.decision(f.src), 8, ledgerNow)
	require.Error(t, err)
	assertQueuedWithoutAttempt(t, f, j)
}

func TestLedger_ReservationTransactionFailureRollsBack(t *testing.T) {
	f := newLedgerFixture(t)
	ctx := context.Background()
	k := newKey()
	j, err := f.ledger.EnsureIntent(ctx, k, "n", anthropicRequirements(k.RunID))
	require.NoError(t, err)
	_, err = f.db.DB.Exec(`CREATE TRIGGER refuse_attempt BEFORE INSERT ON runner_job_attempts BEGIN SELECT RAISE(ABORT,'injected'); END`)
	require.NoError(t, err)
	_, err = f.ledger.Reserve(ctx, j.ID, f.decision(f.src), 8, time.Now())
	require.ErrorContains(t, err, "injected")
	assertQueuedWithoutAttempt(t, f, j)
}

func TestReserve_RequiredSourceCannotBeChangedByDecision(t *testing.T) {
	f := newLedgerFixture(t)
	ctx := context.Background()
	k := newKey()
	req := anthropicRequirements(k.RunID)
	req.RequiredSource = f.src
	a, err := f.reg.GetSource(ctx, f.src)
	require.NoError(t, err)
	b := a
	b.ID = runner.MakeSourceID(runner.BuiltInRunnerID, runner.CredAnthropicSubscription, "other")
	b.CredentialRef = "other"
	require.NoError(t, f.reg.ReplaceSources(ctx, runner.BuiltInRunnerID, []runner.Source{a, b}))
	j, err := f.ledger.EnsureIntent(ctx, k, "n", req)
	require.NoError(t, err)
	_, err = f.ledger.Reserve(ctx, j.ID, f.decision(b.ID), 8, ledgerNow)
	assert.ErrorIs(t, err, runner.ErrSlotTaken)
	assertQueuedWithoutAttempt(t, f, j)
}
