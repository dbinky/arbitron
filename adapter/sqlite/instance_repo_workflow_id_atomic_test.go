package sqlite

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/adapter/sqlite/generated"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInstanceRepo_Create_StampsWorkflowID_Atomically verifies that Create
// persists workflow_id within the SAME INSERT that creates the row, not via a
// separate follow-up UPDATE. The earlier two-statement form left a crash window
// in which the row existed with workflow_id=NULL, making a live run invisible to
// the delete-while-running guard (which keys on workflow_id).
//
// We assert atomicity by reading the row back through the GENERATED GetInstance
// query (which must now return workflow_id) immediately after Create: a folded
// INSERT yields the id; a separate-UPDATE form that had crashed would yield NULL.
func TestInstanceRepo_Create_StampsWorkflowID_Atomically(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()
	wfID := uuid.New()

	inst := &instance.Instance{
		ModelPins: map[string]string{}, ModelPinsVersion: instance.ModelPinsVersionSnapshot,
		ID:               uuid.New(),
		OwnerID:          uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		WorkflowID:       wfID,
		WorkflowName:     "wf",
		WorkflowSnapshot: map[string]any{"name": "wf"},
		State:            instance.StateQueued,
		Priority:         instance.PriorityNormal,
		Variables:        map[string]any{},
		Visibility:       instance.VisibilityPrivate,
	}
	require.NoError(t, repo.Create(ctx, inst))

	// The generated GetInstance query must carry workflow_id (folded in), so the
	// id is present without any hand-written follow-up read.
	q := generated.New(db.DB)
	row, err := q.GetInstance(ctx, uuidToString(inst.ID))
	require.NoError(t, err)
	require.True(t, row.WorkflowID.Valid, "workflow_id must be set by the same INSERT (atomic)")
	assert.Equal(t, wfID.String(), row.WorkflowID.String,
		"workflow_id must be persisted in the CreateInstance INSERT, not a separate UPDATE")
}

// TestInstanceRepo_Create_NilWorkflowID_LeavesColumnNull verifies a legacy /
// local-mode run (Nil WorkflowID) leaves the column NULL through the folded
// INSERT — the backfill's "dangling label" state.
func TestInstanceRepo_Create_NilWorkflowID_LeavesColumnNull(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	ctx := context.Background()

	inst := &instance.Instance{
		ModelPins: map[string]string{}, ModelPinsVersion: instance.ModelPinsVersionSnapshot,
		ID:               uuid.New(),
		OwnerID:          uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		WorkflowName:     "legacy",
		WorkflowSnapshot: map[string]any{"name": "legacy"},
		State:            instance.StateQueued,
		Priority:         instance.PriorityNormal,
		Variables:        map[string]any{},
		Visibility:       instance.VisibilityPrivate,
	}
	require.NoError(t, repo.Create(ctx, inst))

	q := generated.New(db.DB)
	row, err := q.GetInstance(ctx, uuidToString(inst.ID))
	require.NoError(t, err)
	assert.False(t, row.WorkflowID.Valid, "a Nil WorkflowID must leave workflow_id NULL")
}
