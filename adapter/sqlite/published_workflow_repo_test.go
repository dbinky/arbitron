package sqlite

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// defaultUserID is the seeded local admin from migration 001.
var defaultUserID = uuid.MustParse("00000000-0000-0000-0000-000000000001")

// --- Happy path ---

func TestPublishedWorkflowRepo_UpsertThenPublisherOf(t *testing.T) {
	db := setupTestDB(t)
	repo := NewPublishedWorkflowRepo(db.DB)
	ctx := context.Background()

	require.NoError(t, repo.Upsert(ctx, "shared-wf", defaultUserID, "my-private", workflow.VisibilityPublic, ""))

	pub, ok, err := repo.PublisherOf(ctx, "shared-wf")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, defaultUserID, pub)
}

func TestPublishedWorkflowRepo_GetRoundTrip(t *testing.T) {
	db := setupTestDB(t)
	repo := NewPublishedWorkflowRepo(db.DB)
	ctx := context.Background()

	require.NoError(t, repo.Upsert(ctx, "shared-wf", defaultUserID, "src-name", workflow.VisibilityPublic, ""))

	row, ok, err := repo.Get(ctx, "shared-wf")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "shared-wf", row.Name)
	assert.Equal(t, defaultUserID, row.PublisherID)
	assert.Equal(t, "src-name", row.SourceName)
	assert.False(t, row.PublishedAt.IsZero())
}

func TestPublishedWorkflowRepo_ListSortedByName(t *testing.T) {
	db := setupTestDB(t)
	repo := NewPublishedWorkflowRepo(db.DB)
	ctx := context.Background()

	require.NoError(t, repo.Upsert(ctx, "zeta", defaultUserID, "z", workflow.VisibilityPublic, ""))
	require.NoError(t, repo.Upsert(ctx, "alpha", defaultUserID, "a", workflow.VisibilityPublic, ""))

	list, err := repo.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, "alpha", list[0].Name)
	assert.Equal(t, "zeta", list[1].Name)
}

// --- Validation ---

func TestPublishedWorkflowRepo_Upsert_UnknownPublisherFailsFK(t *testing.T) {
	db := setupTestDB(t)
	repo := NewPublishedWorkflowRepo(db.DB)
	ctx := context.Background()

	// Ensure FK enforcement is on for this connection.
	_, _ = db.DB.Exec("PRAGMA foreign_keys = ON")

	err := repo.Upsert(ctx, "wf", uuid.New(), "src", workflow.VisibilityPublic, "")
	require.Error(t, err, "publisher_id with no users row must violate the FK")
}

// --- Error/failure ---

func TestPublishedWorkflowRepo_PublisherOf_UnknownIsNoError(t *testing.T) {
	db := setupTestDB(t)
	repo := NewPublishedWorkflowRepo(db.DB)
	ctx := context.Background()

	pub, ok, err := repo.PublisherOf(ctx, "ghost")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, uuid.Nil, pub)
}

func TestPublishedWorkflowRepo_Get_UnknownIsNoError(t *testing.T) {
	db := setupTestDB(t)
	repo := NewPublishedWorkflowRepo(db.DB)
	ctx := context.Background()

	row, ok, err := repo.Get(ctx, "ghost")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Nil(t, row)
}

func TestPublishedWorkflowRepo_Delete_UnknownIsNoop(t *testing.T) {
	db := setupTestDB(t)
	repo := NewPublishedWorkflowRepo(db.DB)
	ctx := context.Background()

	require.NoError(t, repo.Delete(ctx, "ghost"))
}

func TestPublishedWorkflowRepo_Delete_RemovesRow(t *testing.T) {
	db := setupTestDB(t)
	repo := NewPublishedWorkflowRepo(db.DB)
	ctx := context.Background()

	require.NoError(t, repo.Upsert(ctx, "wf", defaultUserID, "src", workflow.VisibilityPublic, ""))
	require.NoError(t, repo.Delete(ctx, "wf"))

	_, ok, err := repo.PublisherOf(ctx, "wf")
	require.NoError(t, err)
	assert.False(t, ok)
}

// --- Boundary ---

func TestPublishedWorkflowRepo_ReUpsert_SameNameUpdatesAndStaysSingleRow(t *testing.T) {
	db := setupTestDB(t)
	repo := NewPublishedWorkflowRepo(db.DB)
	ctx := context.Background()

	other := seedUser(t, db, "other")

	require.NoError(t, repo.Upsert(ctx, "wf", defaultUserID, "src1", workflow.VisibilityPublic, ""))
	require.NoError(t, repo.Upsert(ctx, "wf", other, "src2", workflow.VisibilityPublic, ""))

	pub, ok, err := repo.PublisherOf(ctx, "wf")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, other, pub, "publisher updated on re-upsert")

	row, _, err := repo.Get(ctx, "wf")
	require.NoError(t, err)
	assert.Equal(t, "src2", row.SourceName)

	// Single row on the PK.
	var count int
	require.NoError(t, db.DB.QueryRow(`SELECT COUNT(*) FROM published_workflows WHERE name = 'wf'`).Scan(&count))
	assert.Equal(t, 1, count)
}

// --- Isolation/security ---

func TestPublishedWorkflowRepo_MigrationIdempotent(t *testing.T) {
	db := setupTestDB(t)

	// RunMigrations twice must not error and the table must remain usable.
	require.NoError(t, RunMigrations(db.DB))

	repo := NewPublishedWorkflowRepo(db.DB)
	ctx := context.Background()
	require.NoError(t, repo.Upsert(ctx, "wf", defaultUserID, "src", workflow.VisibilityPublic, ""))

	var count int
	require.NoError(t, db.DB.QueryRow(`SELECT COUNT(*) FROM published_workflows`).Scan(&count))
	assert.Equal(t, 1, count)
}

// --- Phase 2: visibility column migration ---

func TestPublishedWorkflowRepo_PreExistingRowDefaultsToPublic(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// Simulate a row that existed before migration 010 by inserting only the
	// pre-010 columns; the column DEFAULT must fill visibility='public'.
	_, err := db.DB.ExecContext(ctx,
		`INSERT INTO published_workflows (name, publisher_id, source_name) VALUES (?, ?, ?)`,
		"legacy-shared", defaultUserID.String(), "src")
	require.NoError(t, err)

	repo := NewPublishedWorkflowRepo(db.DB)
	pub, ok, err := repo.GetPublication(ctx, "legacy-shared")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "public", pub.Visibility)
	assert.Equal(t, "", pub.Department) // NULL department reads back as empty string
}

func TestPublishedWorkflowRepo_UpsertDepartment_RoundTrips(t *testing.T) {
	db := setupTestDB(t)
	repo := NewPublishedWorkflowRepo(db.DB)
	ctx := context.Background()

	require.NoError(t, repo.Upsert(ctx, "triage", defaultUserID, "src", workflow.VisibilityDepartment, "engineering"))

	pub, ok, err := repo.GetPublication(ctx, "triage")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "department", pub.Visibility)
	assert.Equal(t, "engineering", pub.Department)
}

func TestPublishedWorkflowRepo_UpsertPublic_StoresNullDepartment(t *testing.T) {
	db := setupTestDB(t)
	repo := NewPublishedWorkflowRepo(db.DB)
	ctx := context.Background()

	require.NoError(t, repo.Upsert(ctx, "pub-wf", defaultUserID, "src", workflow.VisibilityPublic, ""))

	// department column is SQL NULL, surfaced as "".
	pub, ok, err := repo.GetPublication(ctx, "pub-wf")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "public", pub.Visibility)
	assert.Equal(t, "", pub.Department)

	var deptIsNull bool
	require.NoError(t, db.DB.QueryRowContext(ctx,
		`SELECT department IS NULL FROM published_workflows WHERE name = ?`, "pub-wf").Scan(&deptIsNull))
	assert.True(t, deptIsNull)
}

func TestPublishedWorkflowRepo_UpsertRejectsBadVisibility(t *testing.T) {
	db := setupTestDB(t)
	repo := NewPublishedWorkflowRepo(db.DB)
	ctx := context.Background()

	// CHECK constraint rejects an out-of-vocab visibility.
	err := repo.Upsert(ctx, "bad", defaultUserID, "src", workflow.Visibility("private"), "")
	require.Error(t, err)
}

// TestPublishedWorkflowRepo_PublicationOf_DeptVisibility tests PublicationOf
// directly for a department-visibility publication. This verifies the domain
// type mapping: string "department" → workflow.VisibilityDepartment, Publisher
// UUID round-trips correctly, and Department is preserved.
func TestPublishedWorkflowRepo_PublicationOf_DeptVisibility(t *testing.T) {
	db := setupTestDB(t)
	repo := NewPublishedWorkflowRepo(db.DB)
	ctx := context.Background()

	require.NoError(t, repo.Upsert(ctx, "sre-runbook", defaultUserID, "private-src", workflow.VisibilityDepartment, "engineering"))

	pub, ok, err := repo.PublicationOf(ctx, "sre-runbook")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, defaultUserID, pub.Publisher)
	assert.Equal(t, "private-src", pub.SourceName)
	assert.Equal(t, workflow.VisibilityDepartment, pub.Visibility)
	assert.Equal(t, "engineering", pub.Department)
}

// TestPublishedWorkflowRepo_PublicationOf_PublicVisibility verifies that a
// public publication maps to workflow.VisibilityPublic and has an empty
// Department string (SQL NULL read back as "").
func TestPublishedWorkflowRepo_PublicationOf_PublicVisibility(t *testing.T) {
	db := setupTestDB(t)
	repo := NewPublishedWorkflowRepo(db.DB)
	ctx := context.Background()

	require.NoError(t, repo.Upsert(ctx, "public-wf", defaultUserID, "src", workflow.VisibilityPublic, ""))

	pub, ok, err := repo.PublicationOf(ctx, "public-wf")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, workflow.VisibilityPublic, pub.Visibility)
	assert.Equal(t, "", pub.Department)
}

// TestPublishedWorkflowRepo_PublicationOf_Unknown verifies that PublicationOf
// returns (zero, false, nil) for an unknown name — not an error.
func TestPublishedWorkflowRepo_PublicationOf_Unknown(t *testing.T) {
	db := setupTestDB(t)
	repo := NewPublishedWorkflowRepo(db.DB)
	ctx := context.Background()

	pub, ok, err := repo.PublicationOf(ctx, "does-not-exist")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, workflow.Publication{}, pub)
}
