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
		ModelPins: map[string]string{}, ModelPinsVersion: instance.ModelPinsVersionSnapshot,
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
		ModelPins: map[string]string{}, ModelPinsVersion: instance.ModelPinsVersionSnapshot,
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

// FA #14 Pass 2 — robustness

// The repo normalises a zero-value (empty string) Visibility to "private" in
// Go before the INSERT, so the column CHECK constraint is never violated and
// reads back correctly even when callers forget to set the field.
func TestInstanceRepo_ZeroVisibility_DefaultsToPrivate(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)

	inst := &instance.Instance{
		ModelPins: map[string]string{}, ModelPinsVersion: instance.ModelPinsVersionSnapshot,
		ID:               uuid.New(),
		OwnerID:          uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		WorkflowName:     "wf",
		WorkflowSnapshot: map[string]any{},
		State:            instance.StateQueued,
		Priority:         instance.PriorityNormal,
		Variables:        map[string]any{},
		// Visibility intentionally zero — repo must default to private.
	}
	require.NoError(t, repo.Create(context.Background(), inst))

	got, err := repo.Get(context.Background(), inst.ID)
	require.NoError(t, err)
	assert.Equal(t, instance.VisibilityPrivate, got.Visibility,
		"zero-value Visibility must persist as private, not violate the SQL CHECK constraint")
}

// VisibilityPublic round-trip: a public run is stored and retrieved with the
// correct visibility value (tests the third CHECK-constraint branch).
func TestInstanceRepo_PublicVisibility_RoundTrip(t *testing.T) {
	db := setupTestDB(t)
	repo := NewInstanceRepo(db.DB)

	inst := &instance.Instance{
		ModelPins: map[string]string{}, ModelPinsVersion: instance.ModelPinsVersionSnapshot,
		ID:               uuid.New(),
		OwnerID:          uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		WorkflowName:     "wf",
		WorkflowSnapshot: map[string]any{},
		State:            instance.StateQueued,
		Priority:         instance.PriorityNormal,
		Variables:        map[string]any{},
		Visibility:       instance.VisibilityPublic,
	}
	require.NoError(t, repo.Create(context.Background(), inst))

	got, err := repo.Get(context.Background(), inst.ID)
	require.NoError(t, err)
	assert.Equal(t, instance.VisibilityPublic, got.Visibility)
	assert.Equal(t, "", got.Department, "public runs need no department")
}
