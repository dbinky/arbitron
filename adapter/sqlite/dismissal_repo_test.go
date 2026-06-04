package sqlite

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const seededLocalUserID = "00000000-0000-0000-0000-000000000001"

// ---------------------------------------------------------------------------
// Migration: the instance_dismissals table exists after RunMigrations.
// ---------------------------------------------------------------------------

func TestInstanceDismissalsMigration_TableExists(t *testing.T) {
	db := setupTestDB(t)
	var count int
	require.NoError(t, db.DB.QueryRow("SELECT COUNT(*) FROM instance_dismissals").Scan(&count))
	assert.Equal(t, 0, count)
}

// dismissalRepoSatisfiesPort fails to compile if *DismissalRepo no longer
// implements the domain port.
var _ instance.DismissalRepository = (*DismissalRepo)(nil)

func newDismissalRepo(t *testing.T) (*DismissalRepo, testDB) {
	t.Helper()
	db := setupTestDB(t)
	return NewDismissalRepo(db.DB), db
}

// seedUser inserts a user row and returns its UUID.
func seedUser(t *testing.T, db testDB, username string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.DB.Exec(
		`INSERT INTO users (id, username, role) VALUES (?, ?, 'user')`,
		uuidToString(id), username)
	require.NoError(t, err)
	return id
}

// seedInstance inserts a minimal instance owned by ownerID and returns its
// UUID. workflow_snapshot/variables default to '{}' via the schema but are set
// explicitly here for clarity.
func seedInstance(t *testing.T, db testDB, ownerID uuid.UUID, state string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.DB.Exec(
		`INSERT INTO instances (id, owner_id, workflow_name, workflow_snapshot, state)
		 VALUES (?, ?, 'wf', '{}', ?)`,
		uuidToString(id), uuidToString(ownerID), state)
	require.NoError(t, err)
	return id
}

func localUserID(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(seededLocalUserID)
	require.NoError(t, err)
	return id
}

// ---------------------------------------------------------------------------
// Happy path
// ---------------------------------------------------------------------------

// Happy: Dismiss then DismissedIDs returns that instance for the user.
func TestDismissalRepo_DismissThenListed(t *testing.T) {
	repo, db := newDismissalRepo(t)
	ctx := context.Background()
	user := localUserID(t)
	inst := seedInstance(t, db, user, "completed")

	require.NoError(t, repo.Dismiss(ctx, user, inst))

	ids, err := repo.DismissedIDs(ctx, user)
	require.NoError(t, err)
	assert.True(t, ids[inst], "dismissed instance must appear in DismissedIDs")
	assert.Len(t, ids, 1)
}

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------

// Edge: Dismiss is idempotent — dismissing twice succeeds and leaves one row.
func TestDismissalRepo_DismissIdempotent(t *testing.T) {
	repo, db := newDismissalRepo(t)
	ctx := context.Background()
	user := localUserID(t)
	inst := seedInstance(t, db, user, "failed")

	require.NoError(t, repo.Dismiss(ctx, user, inst))
	require.NoError(t, repo.Dismiss(ctx, user, inst), "second Dismiss must be a no-op success")

	var count int
	require.NoError(t, db.DB.QueryRow(
		"SELECT COUNT(*) FROM instance_dismissals WHERE user_id = ? AND instance_id = ?",
		uuidToString(user), uuidToString(inst)).Scan(&count))
	assert.Equal(t, 1, count, "idempotent dismiss must not create duplicate rows")
}

// Edge: Restore of a non-dismissed instance is a no-op success.
func TestDismissalRepo_RestoreNonDismissedNoOp(t *testing.T) {
	repo, db := newDismissalRepo(t)
	ctx := context.Background()
	user := localUserID(t)
	inst := seedInstance(t, db, user, "completed")

	require.NoError(t, repo.Restore(ctx, user, inst), "restore of a non-dismissed item is a no-op success")

	ids, err := repo.DismissedIDs(ctx, user)
	require.NoError(t, err)
	assert.Empty(t, ids)
}

// Edge: DismissedIDs on an empty store returns a non-nil empty map.
func TestDismissalRepo_DismissedIDsEmpty(t *testing.T) {
	repo, _ := newDismissalRepo(t)
	ids, err := repo.DismissedIDs(context.Background(), localUserID(t))
	require.NoError(t, err)
	assert.NotNil(t, ids)
	assert.Empty(t, ids)
}

// ---------------------------------------------------------------------------
// Success
// ---------------------------------------------------------------------------

// Success: Dismiss then Restore removes the dismissal.
func TestDismissalRepo_DismissThenRestore(t *testing.T) {
	repo, db := newDismissalRepo(t)
	ctx := context.Background()
	user := localUserID(t)
	inst := seedInstance(t, db, user, "completed")

	require.NoError(t, repo.Dismiss(ctx, user, inst))
	require.NoError(t, repo.Restore(ctx, user, inst))

	ids, err := repo.DismissedIDs(ctx, user)
	require.NoError(t, err)
	assert.False(t, ids[inst])
	assert.Empty(t, ids)
}

// ---------------------------------------------------------------------------
// Boundary / scoping
// ---------------------------------------------------------------------------

// Boundary: DismissedIDs is scoped per user — user A's dismissal is invisible
// to user B even for the same instance.
func TestDismissalRepo_DismissedIDsScopedPerUser(t *testing.T) {
	repo, db := newDismissalRepo(t)
	ctx := context.Background()
	userA := localUserID(t)
	userB := seedUser(t, db, "bob")
	inst := seedInstance(t, db, userA, "failed")

	require.NoError(t, repo.Dismiss(ctx, userA, inst))

	idsA, err := repo.DismissedIDs(ctx, userA)
	require.NoError(t, err)
	assert.True(t, idsA[inst])

	idsB, err := repo.DismissedIDs(ctx, userB)
	require.NoError(t, err)
	assert.Empty(t, idsB, "user B must not see user A's dismissal")
}

// Boundary: ClearForInstance removes the dismissal across ALL users.
func TestDismissalRepo_ClearForInstanceAcrossUsers(t *testing.T) {
	repo, db := newDismissalRepo(t)
	ctx := context.Background()
	userA := localUserID(t)
	userB := seedUser(t, db, "bob")
	inst := seedInstance(t, db, userA, "failed")

	require.NoError(t, repo.Dismiss(ctx, userA, inst))
	require.NoError(t, repo.Dismiss(ctx, userB, inst))

	require.NoError(t, repo.ClearForInstance(ctx, inst))

	idsA, err := repo.DismissedIDs(ctx, userA)
	require.NoError(t, err)
	idsB, err := repo.DismissedIDs(ctx, userB)
	require.NoError(t, err)
	assert.Empty(t, idsA, "clear must remove user A's dismissal")
	assert.Empty(t, idsB, "clear must remove user B's dismissal")
}

// ---------------------------------------------------------------------------
// Integration: cascade on instance delete
// ---------------------------------------------------------------------------

// Integration: deleting the instance removes the dismissal row via ON DELETE
// CASCADE. This repo's Open() enables PRAGMA foreign_keys=ON with a single
// pooled connection, so setupTestDB has FK enforcement active and the cascade
// MUST fire.
func TestDismissalRepo_CascadeOnInstanceDelete(t *testing.T) {
	repo, db := newDismissalRepo(t)
	ctx := context.Background()
	user := localUserID(t)
	inst := seedInstance(t, db, user, "completed")
	require.NoError(t, repo.Dismiss(ctx, user, inst))

	// Confirm whether FK enforcement is active on this connection.
	var fk int
	require.NoError(t, db.DB.QueryRow("PRAGMA foreign_keys").Scan(&fk))

	_, err := db.DB.Exec("DELETE FROM instances WHERE id = ?", uuidToString(inst))
	require.NoError(t, err)

	ids, err := repo.DismissedIDs(ctx, user)
	require.NoError(t, err)
	require.Equal(t, 1, fk, "this repo's Open() enables PRAGMA foreign_keys=ON; setupTestDB must have FKs active")
	assert.Empty(t, ids, "ON DELETE CASCADE must remove the dismissal when the instance is deleted")
}
