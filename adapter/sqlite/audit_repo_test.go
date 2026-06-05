package sqlite

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/audit"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuditRepo_AppendThenList(t *testing.T) {
	db := setupTestDB(t)
	repo := NewAuditRepo(db.DB)
	ctx := context.Background()

	ev := audit.Event{
		ID:         uuid.New(),
		OccurredAt: time.Now().UTC(),
		ActorID:    "00000000-0000-0000-0000-000000000001",
		Action:     audit.ActionWorkflowPublish,
		Target:     "triage",
		Detail:     `{"to":"department","department":"engineering"}`,
	}
	require.NoError(t, repo.Append(ctx, ev))

	got, err := repo.ListByActor(ctx, "00000000-0000-0000-0000-000000000001", 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, audit.ActionWorkflowPublish, got[0].Action)
	assert.Equal(t, "triage", got[0].Target)
	assert.Equal(t, `{"to":"department","department":"engineering"}`, got[0].Detail)
}

func TestAuditRepo_DefaultDetailIsEmptyObject(t *testing.T) {
	db := setupTestDB(t)
	repo := NewAuditRepo(db.DB)
	ctx := context.Background()

	ev := audit.Event{
		ID: uuid.New(), OccurredAt: time.Now().UTC(),
		ActorID: "actor-x", Action: audit.ActionAdminOverride, Target: "eng-wf",
		// Detail intentionally empty → adapter stores "{}".
	}
	require.NoError(t, repo.Append(ctx, ev))
	got, err := repo.ListByActor(ctx, "actor-x", 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "{}", got[0].Detail)
}

// TestAuditRepo_ListByActor_NewestFirst verifies the ORDER BY occurred_at DESC
// guarantee: the most recent event must be first. A bug that used ASC order
// would make audit views show stale history at the top.
func TestAuditRepo_ListByActor_NewestFirst(t *testing.T) {
	db := setupTestDB(t)
	repo := NewAuditRepo(db.DB)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Second)
	actor := "00000000-0000-0000-0000-000000000001"
	require.NoError(t, repo.Append(ctx, audit.Event{
		ID: uuid.New(), OccurredAt: base.Add(-2 * time.Second), ActorID: actor,
		Action: audit.ActionWorkflowPublish, Target: "first",
	}))
	require.NoError(t, repo.Append(ctx, audit.Event{
		ID: uuid.New(), OccurredAt: base.Add(-1 * time.Second), ActorID: actor,
		Action: audit.ActionWorkflowVisibilityChange, Target: "second",
	}))
	require.NoError(t, repo.Append(ctx, audit.Event{
		ID: uuid.New(), OccurredAt: base, ActorID: actor,
		Action: audit.ActionAdminOverride, Target: "third",
	}))

	got, err := repo.ListByActor(ctx, actor, 10)
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, "third", got[0].Target, "newest event must be first (ORDER BY occurred_at DESC)")
	assert.Equal(t, "second", got[1].Target)
	assert.Equal(t, "first", got[2].Target)
}

// TestAuditRepo_ListByActor_LimitApplied verifies that limit caps the result.
// Storing 3 events then listing with limit=2 must return exactly the 2 newest.
func TestAuditRepo_ListByActor_LimitApplied(t *testing.T) {
	db := setupTestDB(t)
	repo := NewAuditRepo(db.DB)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Second)
	actor := "00000000-0000-0000-0000-000000000001"
	for i := 0; i < 3; i++ {
		require.NoError(t, repo.Append(ctx, audit.Event{
			ID: uuid.New(), OccurredAt: base.Add(time.Duration(i) * time.Second),
			ActorID: actor, Action: audit.ActionWorkflowPublish, Target: fmt.Sprintf("wf-%d", i),
		}))
	}

	got, err := repo.ListByActor(ctx, actor, 2)
	require.NoError(t, err)
	assert.Len(t, got, 2, "LIMIT must cap the result to 2 even though 3 events exist")
}

// TestAuditRepo_ListByActor_ActorIsolation verifies that the WHERE actor_id=?
// clause is present: events belonging to actor A must not appear in actor B's
// list. A missing WHERE clause would return all events for every actor.
func TestAuditRepo_ListByActor_ActorIsolation(t *testing.T) {
	db := setupTestDB(t)
	repo := NewAuditRepo(db.DB)
	ctx := context.Background()

	actorA := "00000000-0000-0000-0000-000000000001"
	actorB := "00000000-0000-0000-0000-000000000002"
	require.NoError(t, repo.Append(ctx, audit.Event{
		ID: uuid.New(), OccurredAt: time.Now().UTC(), ActorID: actorA,
		Action: audit.ActionWorkflowPublish, Target: "a-wf",
	}))
	require.NoError(t, repo.Append(ctx, audit.Event{
		ID: uuid.New(), OccurredAt: time.Now().UTC(), ActorID: actorB,
		Action: audit.ActionAdminOverride, Target: "b-wf",
	}))

	gotA, err := repo.ListByActor(ctx, actorA, 10)
	require.NoError(t, err)
	require.Len(t, gotA, 1)
	assert.Equal(t, "a-wf", gotA[0].Target)

	gotB, err := repo.ListByActor(ctx, actorB, 10)
	require.NoError(t, err)
	require.Len(t, gotB, 1)
	assert.Equal(t, "b-wf", gotB[0].Target)
}

// TestAuditRepo_ListByActor_Empty verifies that querying an actor with no
// events returns an empty (non-nil) slice and no error.
func TestAuditRepo_ListByActor_Empty(t *testing.T) {
	db := setupTestDB(t)
	repo := NewAuditRepo(db.DB)
	ctx := context.Background()

	got, err := repo.ListByActor(ctx, "nobody", 10)
	require.NoError(t, err)
	assert.NotNil(t, got)
	assert.Empty(t, got)
}

// TestAuditRepo_ListByActor_ZeroLimit verifies that limit=0 returns nothing
// without panicking. The SQL LIMIT 0 clause is valid and must produce an empty
// result, not an error or a full table scan.
func TestAuditRepo_ListByActor_ZeroLimit(t *testing.T) {
	db := setupTestDB(t)
	repo := NewAuditRepo(db.DB)
	ctx := context.Background()

	actor := "00000000-0000-0000-0000-000000000001"
	require.NoError(t, repo.Append(ctx, audit.Event{
		ID: uuid.New(), OccurredAt: time.Now().UTC(), ActorID: actor,
		Action: audit.ActionWorkflowPublish, Target: "wf",
	}))

	got, err := repo.ListByActor(ctx, actor, 0)
	require.NoError(t, err)
	assert.Empty(t, got, "LIMIT 0 must return no rows")
}
