package sqlite

import (
	"context"
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
