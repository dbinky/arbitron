package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLedger_ConcurrentReservationsNeverExceedLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.db")
	dbA, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { dbA.Close() })
	require.NoError(t, RunMigrations(dbA))
	dbB, err := Open(path) // a second, independent connection pool on the same file
	require.NoError(t, err)
	t.Cleanup(func() { dbB.Close() })

	ctx := context.Background()
	reg := NewRunnerRegistryRepo(dbA)
	require.NoError(t, reg.UpsertRunner(ctx, runner.Runner{ID: runner.BuiltInRunnerID, Name: "builtin", BuiltIn: true, StepLimit: 8, Liveness: runner.LivenessOnline}))
	ledgers := []*RunnerLedgerRepo{NewRunnerLedgerRepo(dbA), NewRunnerLedgerRepo(dbB)}

	req := runner.ExecRequirements{Group: runner.ModelGroup{Kind: runner.CredNone}, Op: runner.OpProcess, Affinity: runner.AffinityBuiltIn}
	var jobs []runner.Job
	for i := 0; i < 20; i++ {
		k := runner.JobKey{RunID: uuid.New(), InvocationID: uuid.New(), OperationID: "node", Attempt: 1}
		j, err := ledgers[0].EnsureIntent(ctx, k, "script", req)
		require.NoError(t, err)
		jobs = append(jobs, j)
	}

	var won, lost int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i, j := range jobs {
		wg.Add(1)
		go func(l *RunnerLedgerRepo, id uuid.UUID) {
			defer wg.Done()
			<-start
			_, err := l.Reserve(ctx, id, runner.Decision{Placed: true, Runner: runner.BuiltInRunnerID, Reason: "rank"}, 8, time.Now().Add(time.Minute))
			switch {
			case err == nil:
				atomic.AddInt32(&won, 1)
			case errors.Is(err, runner.ErrSlotTaken):
				atomic.AddInt32(&lost, 1)
			default:
				t.Errorf("unexpected reservation error: %v", err)
			}
		}(ledgers[i%2], j.ID)
	}
	close(start)
	wg.Wait()

	assert.Equal(t, int32(8), won, "exactly the limit wins")
	assert.Equal(t, int32(12), lost)
	var offered int
	require.NoError(t, dbA.QueryRow(`SELECT COUNT(*) FROM runner_jobs WHERE state IN ('offered','running')`).Scan(&offered))
	assert.Equal(t, 8, offered, "the sink agrees: never more than eight held slots")
	snap, err := reg.Snapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, 8, snap.SlotsInUse[runner.BuiltInRunnerID])
}

func TestLedger_ConcurrentProbationSingleFlight(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.db")
	dbA, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { dbA.Close() })
	require.NoError(t, RunMigrations(dbA))
	dbB, err := Open(path) // a second, independent connection pool on the same file
	require.NoError(t, err)
	t.Cleanup(func() { dbB.Close() })

	ctx := context.Background()
	reg := NewRunnerRegistryRepo(dbA)
	require.NoError(t, reg.UpsertRunner(ctx, runner.Runner{ID: runner.BuiltInRunnerID, Name: "builtin", BuiltIn: true, StepLimit: 8, Liveness: runner.LivenessOnline}))
	ledgers := []*RunnerLedgerRepo{NewRunnerLedgerRepo(dbA), NewRunnerLedgerRepo(dbB)}

	src := runner.MakeSourceID(runner.BuiltInRunnerID, runner.CredAnthropicSubscription, "")
	require.NoError(t, reg.ReplaceSources(ctx, runner.BuiltInRunnerID, []runner.Source{{ID: src, RunnerID: runner.BuiltInRunnerID, Kind: runner.CredAnthropicSubscription, Families: []modelidentity.Family{"anthropic"}, Backends: []string{"claude-code"}, Health: runner.SourceHealth{Auth: runner.AuthOK, Capacity: runner.CapacityProbation}}}))
	req := anthropicRequirements(uuid.New())
	var jobs []runner.Job
	for i := 0; i < 8; i++ {
		k := runner.JobKey{RunID: uuid.New(), InvocationID: uuid.New(), OperationID: "node", Attempt: 1}
		j, err := ledgers[0].EnsureIntent(ctx, k, "script", req)
		require.NoError(t, err)
		jobs = append(jobs, j)
	}

	var won, lost int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i, j := range jobs {
		wg.Add(1)
		go func(l *RunnerLedgerRepo, id uuid.UUID) {
			defer wg.Done()
			<-start
			_, err := l.Reserve(ctx, id, runner.Decision{Placed: true, Runner: runner.BuiltInRunnerID, Source: src, Reason: "rank"}, 8, time.Now().Add(time.Minute))
			switch {
			case err == nil:
				atomic.AddInt32(&won, 1)
			case errors.Is(err, runner.ErrSlotTaken):
				atomic.AddInt32(&lost, 1)
			default:
				t.Errorf("unexpected reservation error: %v", err)
			}
		}(ledgers[i%2], j.ID)
	}
	close(start)
	wg.Wait()

	assert.Equal(t, int32(1), won, "exactly the limit wins")
	assert.Equal(t, int32(7), lost)
	var offered int
	require.NoError(t, dbA.QueryRow(`SELECT COUNT(*) FROM runner_jobs WHERE state IN ('offered','running')`).Scan(&offered))
	assert.Equal(t, 1, offered, "the sink agrees: never more than eight held slots")
	snap, err := reg.Snapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, snap.SlotsInUse[runner.BuiltInRunnerID])
}
