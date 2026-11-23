package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/notification"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func strp(s string) *string { return &s }

func placedExec(instID uuid.UUID, node string, attempt int) *instance.NodeExecution {
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	return &instance.NodeExecution{
		ID: uuid.New(), InstanceID: instID, NodeID: node, Attempt: attempt,
		Status: instance.NodeStatusFailed, StartedAt: at,
		RunnerID: strp("builtin"), SourceID: strp("builtin/lunaroute/lr"), PlacementReason: strp("first fit"),
		PlacementHistory: []instance.PlacementRecord{
			{JobID: uuid.NewString(), RunnerID: "builtin", SourceID: "builtin/anthropic_subscription/-", Reason: "preferred", Outcome: "exhausted", Generation: 1, At: at},
			{JobID: uuid.NewString(), RunnerID: "builtin", SourceID: "builtin/lunaroute/lr", Reason: "first fit", Outcome: "other", Generation: 2, At: at.Add(time.Second)},
		},
	}
}

func TestMigration042_AddsPlacementColumns(t *testing.T) {
	db := setupTestDB(t)
	for _, col := range []string{"runner_id", "source_id", "placement_reason", "placement_history"} {
		var n int
		require.NoError(t, db.DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('node_executions') WHERE name = ?`, col).Scan(&n))
		assert.Equal(t, 1, n, col)
	}
}

func TestMigration042_AdmitsSourceLoggedOutNotification(t *testing.T) {
	db := setupTestDB(t)
	repo := NewNotificationRepo(db.DB)
	n := &notification.Notification{ID: uuid.New(), WorkflowName: "runner builtin source anthropic_subscription", Type: notification.TypeSourceLoggedOut, CreatedAt: time.Now().UTC().Truncate(time.Second)}
	require.NoError(t, repo.Create(context.Background(), n))
	got, err := repo.List(context.Background(), notification.ListFilters{})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, notification.TypeSourceLoggedOut, got[0].Type)
	assert.Equal(t, uuid.Nil, got[0].InstanceID)
}

func TestMigration042_DownRemovesColumnsAndRows(t *testing.T) {
	db := setupTestDB(t)
	require.NoError(t, NewNotificationRepo(db.DB).Create(context.Background(), &notification.Notification{ID: uuid.New(), Type: notification.TypeSourceLoggedOut, WorkflowName: "x"}))
	require.NoError(t, NewNotificationRepo(db.DB).Create(context.Background(), &notification.Notification{ID: uuid.New(), Type: notification.TypeScheduleSkipped, WorkflowName: "y"}))
	down, err := migrationsFS.ReadFile("migrations/042_node_execution_placement.down.sql")
	require.NoError(t, err)
	_, err = db.DB.Exec(string(down))
	require.NoError(t, err)
	var n int
	require.NoError(t, db.DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('node_executions') WHERE name = 'runner_id'`).Scan(&n))
	assert.Zero(t, n)
	require.NoError(t, db.DB.QueryRow(`SELECT COUNT(*) FROM notifications`).Scan(&n))
	assert.Equal(t, 1, n, "only the source_logged_out row is dropped")
}

func TestNodeExecutionRepo_PlacementRoundTrip(t *testing.T) {
	db := setupTestDB(t)
	owner := seedUser(t, db, "owner-"+uuid.NewString()[:8])
	instID := seedInstance(t, db, owner, "running")
	repo := NewNodeExecutionRepo(db.DB)
	ctx := context.Background()

	placed := placedExec(instID, "B", 1)
	require.NoError(t, repo.Create(ctx, placed))
	plain := &instance.NodeExecution{ID: uuid.New(), InstanceID: instID, NodeID: "T", Attempt: 1, Status: instance.NodeStatusSucceeded, StartedAt: time.Now().UTC()}
	require.NoError(t, repo.Create(ctx, plain))

	list, err := repo.ListByInstance(ctx, instID)
	require.NoError(t, err)
	require.Len(t, list, 2)
	byNode := map[string]instance.NodeExecution{}
	for _, e := range list {
		byNode[e.NodeID] = e
	}
	b := byNode["B"]
	require.NotNil(t, b.RunnerID)
	assert.Equal(t, "builtin", *b.RunnerID)
	assert.Equal(t, "builtin/lunaroute/lr", *b.SourceID)
	assert.Equal(t, "first fit", *b.PlacementReason)
	require.Len(t, b.PlacementHistory, 2)
	assert.Equal(t, placed.PlacementHistory, b.PlacementHistory)

	tr := byNode["T"]
	assert.Nil(t, tr.RunnerID, "transform nodes carry no placement")
	assert.Nil(t, tr.PlacementHistory)

	one, err := repo.GetByInstanceAndNode(ctx, instID, "B")
	require.NoError(t, err)
	require.NotNil(t, one.RunnerID)
	assert.Equal(t, "builtin", *one.RunnerID)
	assert.Len(t, one.PlacementHistory, 2)
}

// Edge: Update (e.g. the engine marking a failed attempt skipped) never erases placement.
func TestNodeExecutionRepo_UpdateKeepsPlacement(t *testing.T) {
	db := setupTestDB(t)
	owner := seedUser(t, db, "owner-"+uuid.NewString()[:8])
	instID := seedInstance(t, db, owner, "running")
	repo := NewNodeExecutionRepo(db.DB)
	ctx := context.Background()
	e := placedExec(instID, "B", 1)
	require.NoError(t, repo.Create(ctx, e))
	e.Status = instance.NodeStatusSkipped
	e.RunnerID, e.PlacementHistory = nil, nil
	require.NoError(t, repo.Update(ctx, e))
	got, err := repo.GetByInstanceAndNode(ctx, instID, "B")
	require.NoError(t, err)
	assert.Equal(t, instance.NodeStatusSkipped, got.Status)
	require.NotNil(t, got.RunnerID)
	assert.Len(t, got.PlacementHistory, 2)
}
