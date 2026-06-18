package sqlite

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- instances: workflow_id round-trips through Create -> Get ---

func TestInstanceRepo_WorkflowID_RoundTrip(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)
	wfID := uuid.New()

	inst := &instance.Instance{
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
	require.NoError(t, repo.Create(context.Background(), inst))

	got, err := repo.Get(context.Background(), inst.ID)
	require.NoError(t, err)
	assert.Equal(t, wfID, got.WorkflowID, "instance workflow_id must round-trip")
	assert.Equal(t, "wf", got.WorkflowName, "the label is retained")
}

// A legacy run created with a Nil WorkflowID leaves the column NULL and reads
// back as uuid.Nil (the backfill's "dangling label" state).
func TestInstanceRepo_NilWorkflowID_RoundTripsAsNil(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)

	inst := &instance.Instance{
		ID:               uuid.New(),
		OwnerID:          uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		WorkflowName:     "legacy",
		WorkflowSnapshot: map[string]any{"name": "legacy"},
		State:            instance.StateQueued,
		Priority:         instance.PriorityNormal,
		Variables:        map[string]any{},
		Visibility:       instance.VisibilityPrivate,
	}
	require.NoError(t, repo.Create(context.Background(), inst))

	got, err := repo.Get(context.Background(), inst.ID)
	require.NoError(t, err)
	assert.Equal(t, uuid.Nil, got.WorkflowID, "a Nil WorkflowID round-trips as Nil (NULL column)")
}

// --- schedules: workflow_id round-trips through Create -> Get and Update ---

func TestScheduleRepo_WorkflowID_RoundTrip(t *testing.T) {
	repo, _, creator := newScheduleRepo(t)
	ctx := context.Background()
	wfID := uuid.New()

	s := newSchedule(creator)
	s.WorkflowID = wfID
	require.NoError(t, repo.Create(ctx, s))

	got, err := repo.Get(ctx, s.ID)
	require.NoError(t, err)
	assert.Equal(t, wfID, got.WorkflowID, "schedule workflow_id must round-trip")

	// Update preserves the id (the copy carries it forward).
	got.WorkflowName = "renamed-label"
	require.NoError(t, repo.Update(ctx, got))
	reread, err := repo.Get(ctx, s.ID)
	require.NoError(t, err)
	assert.Equal(t, wfID, reread.WorkflowID, "Update preserves workflow_id")
	assert.Equal(t, "renamed-label", reread.WorkflowName)
}

func TestScheduleRepo_NilWorkflowID_RoundTripsAsNil(t *testing.T) {
	repo, _, creator := newScheduleRepo(t)
	ctx := context.Background()

	s := newSchedule(creator) // WorkflowID left Nil
	require.NoError(t, repo.Create(ctx, s))

	got, err := repo.Get(ctx, s.ID)
	require.NoError(t, err)
	assert.Equal(t, uuid.Nil, got.WorkflowID, "a Nil schedule WorkflowID round-trips as Nil")
}

// --- published_workflows: SetWorkflowID links the publication to the id ---

func TestPublishedWorkflowRepo_SetWorkflowID_Links(t *testing.T) {
	db := setupTestDB(t)
	repo := NewPublishedWorkflowRepo(db.DB)
	ctx := context.Background()
	publisher := seedUser(t, db, "pub-"+uuid.NewString()[:8])

	require.NoError(t, repo.Upsert(ctx, "shared-wf", publisher, "src", workflow.VisibilityPublic, ""))

	wfID := uuid.New()
	require.NoError(t, repo.SetWorkflowID(ctx, "shared-wf", wfID))

	var stored string
	require.NoError(t, db.DB.QueryRowContext(ctx,
		`SELECT workflow_id FROM published_workflows WHERE name = ?`, "shared-wf").Scan(&stored))
	assert.Equal(t, wfID.String(), stored, "SetWorkflowID must link the publication to the stable id")
}

// SetWorkflowID with a Nil id is a no-op (leaves the column NULL).
func TestPublishedWorkflowRepo_SetWorkflowID_NilIsNoOp(t *testing.T) {
	db := setupTestDB(t)
	repo := NewPublishedWorkflowRepo(db.DB)
	ctx := context.Background()
	publisher := seedUser(t, db, "pub-"+uuid.NewString()[:8])

	require.NoError(t, repo.Upsert(ctx, "shared-wf", publisher, "src", workflow.VisibilityPublic, ""))
	require.NoError(t, repo.SetWorkflowID(ctx, "shared-wf", uuid.Nil))

	var stored interface{}
	require.NoError(t, db.DB.QueryRowContext(ctx,
		`SELECT workflow_id FROM published_workflows WHERE name = ?`, "shared-wf").Scan(&stored))
	assert.Nil(t, stored, "a Nil id must leave workflow_id NULL")
}
