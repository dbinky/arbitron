package sqlite

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Create persists department+visibility; Get reads them back unchanged.
func TestInstanceRepo_DepartmentVisibility_RoundTrip(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)

	inst := &instance.Instance{
		ID:               uuid.New(),
		OwnerID:          uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		WorkflowName:     "wf",
		WorkflowSnapshot: map[string]any{"name": "wf"},
		State:            instance.StateQueued,
		Priority:         instance.PriorityNormal,
		Variables:        map[string]any{},
		Department:       "engineering",
		Visibility:       instance.VisibilityDepartment,
	}
	require.NoError(t, repo.Create(context.Background(), inst))

	got, err := repo.Get(context.Background(), inst.ID)
	require.NoError(t, err)
	assert.Equal(t, "engineering", got.Department)
	assert.Equal(t, instance.VisibilityDepartment, got.Visibility)
}

// A private run persists empty department + private visibility.
func TestInstanceRepo_PrivateRun_NullDepartment(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)

	inst := &instance.Instance{
		ID:               uuid.New(),
		OwnerID:          uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		WorkflowName:     "wf",
		WorkflowSnapshot: map[string]any{},
		State:            instance.StateQueued,
		Priority:         instance.PriorityNormal,
		Variables:        map[string]any{},
		Visibility:       instance.VisibilityPrivate,
	}
	require.NoError(t, repo.Create(context.Background(), inst))

	got, err := repo.Get(context.Background(), inst.ID)
	require.NoError(t, err)
	assert.Equal(t, "", got.Department, "empty department stored as SQL NULL, read back as empty string")
	assert.Equal(t, instance.VisibilityPrivate, got.Visibility)
}
