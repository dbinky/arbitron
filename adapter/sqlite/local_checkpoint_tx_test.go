package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPromoteLocalCheckpointTx_SuccessorOnly(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	homes := NewWorkspaceHomeRepo(db.DB)
	run := uuid.New()
	require.NoError(t, homes.SetNonPortable(ctx, run, runner.NonPortablePromisorReason))
	require.NoError(t, homes.MarkDirty(ctx, run, true))
	ref := func(v int64) string {
		return runner.LocalCheckpointRef{RunID: run, Version: v, ManifestHash: strings.Repeat("a", 64)}.String()
	}
	promote := func(local string) error {
		tx, err := db.DB.BeginTx(ctx, nil)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()
		if err := promoteLocalCheckpointTx(ctx, tx, run, local); err != nil {
			return err
		}
		return tx.Commit()
	}
	require.NoError(t, promote(ref(1)))
	h, err := homes.GetHome(ctx, run)
	require.NoError(t, err)
	assert.Equal(t, ref(1), h.LocalCheckpoint)
	assert.False(t, h.Dirty)
	require.NoError(t, promote(ref(2)))
	for _, bad := range []string{ref(2), ref(4), "", runner.LocalCheckpointRef{RunID: uuid.New(), Version: 3, ManifestHash: strings.Repeat("a", 64)}.String()} {
		require.ErrorIs(t, promote(bad), engine.ErrInvalidStepCompletion, bad)
	}
	h, err = homes.GetHome(ctx, run)
	require.NoError(t, err)
	assert.Equal(t, ref(2), h.LocalCheckpoint)
}

func localTurn(t *testing.T, f turnCommitFixture, version int64) engine.TurnCommit {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, NewWorkspaceHomeRepo(f.db.DB).SetNonPortable(ctx, f.inst.ID, runner.NonPortablePromisorReason))
	c := f.turn()
	local := runner.LocalCheckpointRef{RunID: f.inst.ID, Version: version, ManifestHash: strings.Repeat("c", 64)}.String()
	c.Completions[0].Workspace = &engine.WorkspaceCompletion{NonPortable: true, LocalRef: local, Resource: f.resource}
	c.Execution.LLMConversation[engine.ConversationKeyWorkspaceCheckpointRef] = local
	return c
}
func TestTurnCommitRepo_NonPortableTurnPromotesLocalCheckpoint(t *testing.T) {
	f := newTurnCommitFixture(t)
	c := localTurn(t, f, 1)
	ctx := context.Background()
	require.NoError(t, f.repo.CommitTurn(ctx, c))
	var local string
	var dirty, version int
	require.NoError(t, f.db.DB.QueryRow(`SELECT local_checkpoint, dirty, checkpoint_version FROM workspace_homes WHERE run_id = ?`, f.inst.ID.String()).Scan(&local, &dirty, &version))
	assert.Equal(t, c.Completions[0].Workspace.LocalRef, local)
	assert.Zero(t, dirty)
	assert.Equal(t, 1, version)
	assert.Equal(t, local, f.stored(t).LLMConversation[engine.ConversationKeyWorkspaceCheckpointRef])
	assert.Zero(t, f.pending(t))
	assert.Equal(t, "completed", f.jobState(t))
	assert.Zero(t, f.lf.slots(t))
	_, active, err := NewResourceLeaseRepo(f.db.DB, "boot").Active(ctx, f.resource.Key)
	require.NoError(t, err)
	assert.False(t, active)
	require.NoError(t, f.repo.CommitTurn(ctx, c), "exact retry does not promote twice")
}
func TestTurnCommitRepo_NonPortableRejectsMixedReceipt(t *testing.T) {
	for _, nonportable := range []bool{true, false} {
		t.Run(map[bool]string{true: "catalog ref on non-portable", false: "local ref on portable"}[nonportable], func(t *testing.T) {
			f := newTurnCommitFixture(t)
			c := localTurn(t, f, 1)
			c.Completions[0].Workspace.Ref = f.wsRef(2)
			c.Completions[0].Workspace.NonPortable = nonportable
			require.ErrorIs(t, f.repo.CommitTurn(context.Background(), c), engine.ErrInvalidStepCompletion)
			f.assertPriorPair(t)
		})
	}
}

func TestTurnCommitRepo_NonPortableLateWriteFailureRollsBackTheLocalPair(t *testing.T) {
	f := newTurnCommitFixture(t)
	c := localTurn(t, f, 1)
	_, err := f.db.DB.Exec(`CREATE TRIGGER reject_local_job BEFORE UPDATE OF state ON runner_jobs BEGIN SELECT RAISE(ABORT, 'local final write failed'); END`)
	require.NoError(t, err)
	err = f.repo.CommitTurn(context.Background(), c)
	require.ErrorContains(t, err, "local final write failed")
	f.assertPriorPair(t)
	var local string
	var dirty int
	require.NoError(t, f.db.DB.QueryRow(`SELECT local_checkpoint, dirty FROM workspace_homes WHERE run_id = ?`, f.inst.ID.String()).Scan(&local, &dirty))
	assert.Empty(t, local)
	assert.Equal(t, 1, dirty)
	_, active, err := NewResourceLeaseRepo(f.db.DB, "boot").Active(context.Background(), f.resource.Key)
	require.NoError(t, err)
	assert.True(t, active)
}

func TestTurnCommitRepo_NonPortableStaleLocalRefRollsBackTheTurn(t *testing.T) {
	f := newTurnCommitFixture(t)
	c := localTurn(t, f, 3)
	require.ErrorIs(t, f.repo.CommitTurn(context.Background(), c), engine.ErrInvalidStepCompletion)
	f.assertPriorPair(t)
	var local string
	require.NoError(t, f.db.DB.QueryRow(`SELECT local_checkpoint FROM workspace_homes WHERE run_id = ?`, f.inst.ID.String()).Scan(&local))
	assert.Empty(t, local)
	_, active, err := NewResourceLeaseRepo(f.db.DB, "boot").Active(context.Background(), f.resource.Key)
	require.NoError(t, err)
	assert.True(t, active)
	var token string
	require.NoError(t, f.db.DB.QueryRow(`SELECT token FROM turn_leases WHERE key = ?`, f.lease.Key).Scan(&token))
	assert.Equal(t, f.lease.Token.String(), token)
}
