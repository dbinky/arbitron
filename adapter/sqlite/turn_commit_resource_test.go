package sqlite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Reuse the durable base and holder evidence supplied by the shared fixture.
func turnCommitWithDurableEvidence(t *testing.T) turnCommitFixture {
	t.Helper()
	return newTurnCommitFixture(t)
}

func TestTurnCommitDurableEvidence_CommitsAndReleases(t *testing.T) {
	f := turnCommitWithDurableEvidence(t)
	c := f.turn()
	require.NoError(t, f.repo.CommitTurn(context.Background(), c))
	got := f.stored(t)
	assert.Len(t, got.LLMConversation["messages"], 2)
	require.NotNil(t, got.SessionCheckpointRef)
	assert.Equal(t, c.CheckpointRef, *got.SessionCheckpointRef)
	assert.EqualValues(t, 2, f.homeVersion(t))
	assert.Equal(t, "committed", f.candidateState(t, 2))
	assert.Equal(t, "completed", f.jobState(t))
	assert.Zero(t, f.pending(t))
	assert.Zero(t, f.lf.slots(t))
	var active int
	require.NoError(t, f.db.DB.QueryRow(`SELECT active FROM workspace_leases WHERE resource_key = ?`, f.resource.Key).Scan(&active))
	assert.Zero(t, active)
	require.NoError(t, f.repo.CommitTurn(context.Background(), c), "exact replay does not require a released claim")
}

func TestTurnCommitDurableEvidence_JobFailureRollsBackPairAndClaim(t *testing.T) {
	f := turnCommitWithDurableEvidence(t)
	_, err := f.db.DB.Exec(`CREATE TRIGGER fail_turn_job BEFORE UPDATE OF state ON runner_jobs BEGIN SELECT RAISE(ABORT, 'inject turn job'); END`)
	require.NoError(t, err)
	err = f.repo.CommitTurn(context.Background(), f.turn())
	require.ErrorContains(t, err, "inject turn job", "must reach the actual final write, not an earlier validation failure")
	f.assertPriorPair(t)
	var active int
	require.NoError(t, f.db.DB.QueryRow(`SELECT active FROM workspace_leases WHERE resource_key = ?`, f.resource.Key).Scan(&active))
	assert.Equal(t, 1, active)
}
