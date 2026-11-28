package sqlite

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionMoveRepo_ExactPriorPairAndAssignmentAreRequired(t *testing.T) {
	for _, tc := range []struct {
		name, sql string
		arg       any
	}{
		{"new checkpoint", `UPDATE node_executions SET session_checkpoint_ref = ? WHERE id = ?`, "ckpt-newer"},
		{"foreign job invocation", `UPDATE runner_jobs SET invocation_id = ? WHERE id = ?`, uuid.New().String()},
		{"foreign job node", `UPDATE runner_jobs SET node_id = ? WHERE id = ?`, "other-node"},
		{"wrong operation", `UPDATE runner_jobs SET op = ? WHERE id = ?`, "chat"},
		{"foreign source", `UPDATE runner_jobs SET source_id = ? WHERE id = ?`, "foreign-source"},
		{"foreign restore holder", `UPDATE workspace_leases SET holder = ? WHERE resource_key = ?`, "job:other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMoveRepoFixture(t, 3, 4)
			var id any = f.job.ID.String()
			if tc.name == "new checkpoint" {
				id = f.exec.ID.String()
			} else if tc.name == "foreign restore holder" {
				id = f.resource.Key
			}
			_, err := f.db.DB.Exec(tc.sql, tc.arg, id)
			require.NoError(t, err)
			err = f.repo.CommitMove(context.Background(), f.commit())
			require.ErrorIs(t, err, engine.ErrTurnFenced)
			f.assertUncommitted(t, 3, "running", true)
			if tc.name == "new checkpoint" {
				assert.Equal(t, "ckpt-newer", *f.stored(t).SessionCheckpointRef)
			}
		})
	}
}

func TestSessionMoveRepo_RestoreMoveRequiresItsRecordedClaim(t *testing.T) {
	f := newMoveRepoFixture(t, 3, 4)
	st := f.state
	st.NewResource = nil
	require.NoError(t, f.repo.AdvanceMove(context.Background(), st))
	c := f.commit()
	c.Resource = engine.ResourceFenceRef{}
	require.ErrorIs(t, f.repo.CommitMove(context.Background(), c), engine.ErrTurnFenced)
	f.assertUncommitted(t, 3, "running", true)
}

func TestSessionMoveRepo_IdempotentRetryRequiresTheExactCommittedOutcome(t *testing.T) {
	f := newMoveRepoFixture(t, 3, 4)
	ctx := context.Background()
	c := f.commit()
	require.NoError(t, f.repo.CommitMove(ctx, c))
	for _, tc := range []struct {
		name   string
		mutate func(*string, *bool)
	}{
		{"different session", func(s *string, _ *bool) { *s = "other-session" }},
		{"different provisional state", func(_ *string, p *bool) { *p = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			other := c
			tc.mutate(&other.SessionID, &other.Provisional)
			require.ErrorIs(t, f.repo.CommitMove(ctx, other), engine.ErrTurnFenced)
			assert.Equal(t, "sid-b", f.stored(t).LLMConversation[engine.ConversationKeySessionID])
			assert.Len(t, f.stored(t).SessionMoves, 1)
			assert.Zero(t, f.lf.slots(t))
		})
	}
}
