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

func TestUserRepo_UpdateUsername_ChangesDisplayName(t *testing.T) {
	db := setupTestDB(t)
	repo := NewUserRepo(db.DB)
	ctx := context.Background()

	u := &instance.User{ID: uuid.New(), Subject: ptr("sub-upd"), Username: "before", Role: identity.RoleUser}
	require.NoError(t, repo.Create(ctx, u))

	require.NoError(t, repo.UpdateUsername(ctx, u.ID, "after"))

	got, err := repo.GetByID(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, "after", got.Username)
}

// Edge: UpdateUsername on a non-existent ID is a silent no-op (0 rows affected,
// no error). Callers must ensure existence before calling (Provisioner.Resolve
// always does GetBySubject first).
func TestUserRepo_UpdateUsername_NonExistentUserNoError(t *testing.T) {
	db := setupTestDB(t)
	repo := NewUserRepo(db.DB)
	err := repo.UpdateUsername(context.Background(), uuid.New(), "ghost")
	assert.NoError(t, err, "UpdateUsername of a missing row must not return an error")
}

func TestUserRepo_List_ReturnsAllUsers(t *testing.T) {
	db := setupTestDB(t)
	repo := NewUserRepo(db.DB)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, &instance.User{ID: uuid.New(), Subject: ptr("s1"), Username: "u1", Role: identity.RoleUser}))
	require.NoError(t, repo.Create(ctx, &instance.User{ID: uuid.New(), Subject: ptr("s2"), Username: "u2", Role: identity.RoleAdmin}))

	users, err := repo.List(ctx)
	require.NoError(t, err)
	// at least 3: the seeded 'local' user plus the two we added
	assert.GreaterOrEqual(t, len(users), 3)
	names := make(map[string]bool)
	for _, u := range users {
		names[u.Username] = true
	}
	assert.True(t, names["u1"])
	assert.True(t, names["u2"])
	assert.True(t, names["local"], "default 'local' user must appear in List")
}

func TestUserRepo_UpdateRole_ChangesRole(t *testing.T) {
	db := setupTestDB(t)
	repo := NewUserRepo(db.DB)
	ctx := context.Background()

	u := &instance.User{ID: uuid.New(), Subject: ptr("sub-role"), Username: "roleuser", Role: identity.RoleUser}
	require.NoError(t, repo.Create(ctx, u))

	require.NoError(t, repo.UpdateRole(ctx, u.ID, string(identity.RoleAdmin)))

	got, err := repo.GetByID(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, identity.RoleAdmin, got.Role)
}

// Edge: UpdateRole on a non-existent ID is a silent no-op. Callers (UserService.SetRole)
// check existence first via GetByID.
func TestUserRepo_UpdateRole_NonExistentUserNoError(t *testing.T) {
	db := setupTestDB(t)
	repo := NewUserRepo(db.DB)
	err := repo.UpdateRole(context.Background(), uuid.New(), string(identity.RoleUser))
	assert.NoError(t, err, "UpdateRole of a missing row must not return an error")
}

func TestUserRepo_CountAdmins_ReflectsCurrentAdmins(t *testing.T) {
	db := setupTestDB(t)
	repo := NewUserRepo(db.DB)
	ctx := context.Background()

	// The seeded 'local' user is an admin.
	n, err := repo.CountAdmins(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "only the default admin should be present initially")

	// Add a second admin.
	u := &instance.User{ID: uuid.New(), Subject: ptr("sub-adm2"), Username: "adm2", Role: identity.RoleAdmin}
	require.NoError(t, repo.Create(ctx, u))
	n2, err := repo.CountAdmins(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, n2)

	// Demote to user role; count drops back to 1.
	require.NoError(t, repo.UpdateRole(ctx, u.ID, string(identity.RoleUser)))
	n3, err := repo.CountAdmins(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n3)
}
