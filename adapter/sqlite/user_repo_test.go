package sqlite

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ptr(s string) *string { return &s }

// Happy path: create then read back by id and by subject.
func TestUserRepo_Create_GetByID_RoundTrip(t *testing.T) {
	db := setupTestDB(t)
	repo := NewUserRepo(db.DB)
	ctx := context.Background()

	u := &instance.User{
		ID:       uuid.New(),
		Subject:  ptr("sub-123"),
		Email:    ptr("alice@example.com"),
		Username: "alice",
		Role:     identity.RoleUser,
	}
	require.NoError(t, repo.Create(ctx, u))

	got, err := repo.GetByID(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, u.ID, got.ID)
	require.NotNil(t, got.Subject)
	assert.Equal(t, "sub-123", *got.Subject)
	require.NotNil(t, got.Email)
	assert.Equal(t, "alice@example.com", *got.Email)
	assert.Equal(t, "alice", got.Username)
	assert.Equal(t, identity.RoleUser, got.Role)

	bySubj, err := repo.GetBySubject(ctx, "sub-123")
	require.NoError(t, err)
	assert.Equal(t, u.ID, bySubj.ID)
}

// Input validation: duplicate non-null subject violates the partial unique
// index; two NULL-subject users are allowed.
func TestUserRepo_Create_DuplicateSubject(t *testing.T) {
	db := setupTestDB(t)
	repo := NewUserRepo(db.DB)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, &instance.User{
		ID: uuid.New(), Subject: ptr("dup"), Username: "u1", Role: identity.RoleUser,
	}))
	err := repo.Create(ctx, &instance.User{
		ID: uuid.New(), Subject: ptr("dup"), Username: "u2", Role: identity.RoleUser,
	})
	require.Error(t, err, "duplicate non-null subject must violate the unique index")

	// Two NULL-subject users coexist (default user already has NULL subject).
	require.NoError(t, repo.Create(ctx, &instance.User{
		ID: uuid.New(), Subject: nil, Username: "nullsub1", Role: identity.RoleUser,
	}))
	require.NoError(t, repo.Create(ctx, &instance.User{
		ID: uuid.New(), Subject: nil, Username: "nullsub2", Role: identity.RoleUser,
	}))
}

func TestUserRepo_GetByID_NotFound(t *testing.T) {
	db := setupTestDB(t)
	repo := NewUserRepo(db.DB)
	_, err := repo.GetByID(context.Background(), uuid.New())
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrNotFound)
}

func TestUserRepo_GetBySubject_NotFound(t *testing.T) {
	db := setupTestDB(t)
	repo := NewUserRepo(db.DB)
	_, err := repo.GetBySubject(context.Background(), "never-seen")
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrNotFound)
}

// Boundary: GetByID of the seeded default user returns admin, no subject/email.
func TestUserRepo_GetByID_DefaultUser(t *testing.T) {
	db := setupTestDB(t)
	repo := NewUserRepo(db.DB)
	ctx := context.Background()

	defID, err := repo.GetDefaultUser(ctx)
	require.NoError(t, err)

	got, err := repo.GetByID(ctx, defID)
	require.NoError(t, err)
	assert.Equal(t, "local", got.Username)
	assert.Equal(t, identity.RoleAdmin, got.Role)
	assert.Nil(t, got.Subject)
	assert.Nil(t, got.Email)
}

func TestRunMigrations_003_SessionsTableExists(t *testing.T) {
	db := setupTestDB(t)

	var name string
	err := db.DB.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name='sessions'").Scan(&name)
	require.NoError(t, err)
	assert.Equal(t, "sessions", name)

	// users.subject and users.email columns exist.
	cols := map[string]bool{}
	rows, err := db.DB.Query("PRAGMA table_info(users)")
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var cid int
		var cname, ctype string
		var notnull, pk int
		var dflt any
		require.NoError(t, rows.Scan(&cid, &cname, &ctype, &notnull, &dflt, &pk))
		cols[cname] = true
	}
	assert.True(t, cols["subject"], "users.subject column must exist after migration 003")
	assert.True(t, cols["email"], "users.email column must exist after migration 003")
}

// Isolation/security: an empty-string subject lookup must NOT match the
// NULL-subject default user (guards against an empty OIDC subject claim
// hijacking the admin account).
func TestUserRepo_GetBySubject_NullSubjectNotMatched(t *testing.T) {
	db := setupTestDB(t)
	repo := NewUserRepo(db.DB)
	_, err := repo.GetBySubject(context.Background(), "")
	require.Error(t, err)
	assert.ErrorIs(t, err, instance.ErrNotFound)
}
