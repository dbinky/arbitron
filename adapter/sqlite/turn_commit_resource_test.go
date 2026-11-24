package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Supply the durable base and holder evidence required by the shared P3
// transaction helpers, without changing the plan's older acceptance fixtures.
func turnCommitWithDurableEvidence(t *testing.T) turnCommitFixture {
	t.Helper()
	f := newTurnCommitFixture(t)
	baseHash := strings.Repeat("a", 64)
	insertCandidate(t, f.db, f.inst.ID, 1, baseHash, f.job, f.resource, "committed")
	base := f.inst.ID.String() + "@1:" + baseHash
	_, err := f.db.DB.Exec(`UPDATE workspace_checkpoints SET base_ref = ? WHERE run_id = ? AND version = 2`, base, f.inst.ID.String())
	require.NoError(t, err)
	_, err = f.db.DB.Exec(`INSERT INTO workspace_lease_holders(resource_key, generation, token, holder, owner, job_id, job_runner_id, job_generation, boot_id, claimed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', ?)`,
		f.resource.Key, f.resource.Generation, f.resource.Token.String(), "job:"+f.job.ID.String(), f.resource.Owner, f.job.ID.String(), string(f.job.RunnerID), f.job.Generation, timeToString(ledgerNow))
	require.NoError(t, err)
	return f
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
