package sqlite

import (
	"context"
	"reflect"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// newUserForDepts creates a user row and returns its ID, reusing the existing
// sqlite test-DB helper setupTestDB (returns testDB{DB *sql.DB}; opens a migrated
// :memory: DB with PRAGMA foreign_keys=ON, verified in dismissal_repo_test.go).
func newUserForDepts(t *testing.T, repo *UserRepo) uuid.UUID {
	t.Helper()
	id := uuid.New()
	subj := "subj-" + id.String()
	if err := repo.Create(context.Background(), &instance.User{
		ID:       id,
		Subject:  &subj,
		Username: "u-" + id.String(),
		Role:     "user",
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return id
}

func TestReplaceDepartments_HappyPath(t *testing.T) {
	db := setupTestDB(t) // testDB{DB: *sql.DB}, FKs ON
	repo := NewUserRepo(db.DB)
	uid := newUserForDepts(t, repo)

	if err := repo.ReplaceDepartments(context.Background(), uid, []string{"engineering", "platform"}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, err := repo.ListDepartments(context.Background(), uid)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := []string{"engineering", "platform"} // ORDER BY department
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestReplaceDepartments_IsIdempotentReplace(t *testing.T) {
	db := setupTestDB(t)
	repo := NewUserRepo(db.DB)
	uid := newUserForDepts(t, repo)

	ctx := context.Background()
	if err := repo.ReplaceDepartments(ctx, uid, []string{"sales", "eng"}); err != nil {
		t.Fatalf("first replace: %v", err)
	}
	// Second replace with a DIFFERENT set fully supersedes the first.
	if err := repo.ReplaceDepartments(ctx, uid, []string{"eng"}); err != nil {
		t.Fatalf("second replace: %v", err)
	}
	got, err := repo.ListDepartments(ctx, uid)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"eng"}) {
		t.Errorf("got %v, want [eng]", got)
	}
}

func TestReplaceDepartments_EmptySetClears(t *testing.T) {
	db := setupTestDB(t)
	repo := NewUserRepo(db.DB)
	uid := newUserForDepts(t, repo)

	ctx := context.Background()
	if err := repo.ReplaceDepartments(ctx, uid, []string{"eng"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := repo.ReplaceDepartments(ctx, uid, nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, err := repo.ListDepartments(ctx, uid)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

func TestReplaceDepartments_DeduplicatesWithinCall(t *testing.T) {
	db := setupTestDB(t)
	repo := NewUserRepo(db.DB)
	uid := newUserForDepts(t, repo)

	ctx := context.Background()
	// Duplicate values in one call must not error (ON CONFLICT DO NOTHING).
	if err := repo.ReplaceDepartments(ctx, uid, []string{"eng", "eng"}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, _ := repo.ListDepartments(ctx, uid)
	if !reflect.DeepEqual(got, []string{"eng"}) {
		t.Errorf("got %v, want [eng]", got)
	}
}

func TestListDepartments_CascadeOnUserDelete(t *testing.T) {
	db := setupTestDB(t)
	repo := NewUserRepo(db.DB)
	uid := newUserForDepts(t, repo)

	ctx := context.Background()
	if err := repo.ReplaceDepartments(ctx, uid, []string{"eng"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Delete the user directly; ON DELETE CASCADE must remove the dept rows.
	if _, err := db.DB.ExecContext(ctx, "DELETE FROM users WHERE id = ?", uid.String()); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	got, err := repo.ListDepartments(ctx, uid)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty after cascade", got)
	}
}

// A new user with no department rows returns an empty slice, not an error.
func TestListDepartments_FreshUser_ReturnsEmpty(t *testing.T) {
	db := setupTestDB(t)
	repo := NewUserRepo(db.DB)
	uid := newUserForDepts(t, repo)

	got, err := repo.ListDepartments(context.Background(), uid)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty for fresh user", got)
	}
}

// LIST ORDER BY department is enforced regardless of insert order.
func TestListDepartments_AlphabeticalOrder(t *testing.T) {
	db := setupTestDB(t)
	repo := NewUserRepo(db.DB)
	uid := newUserForDepts(t, repo)

	// Insert in non-alphabetical order; expect sorted result.
	ctx := context.Background()
	if err := repo.ReplaceDepartments(ctx, uid, []string{"sales", "engineering", "platform"}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, err := repo.ListDepartments(ctx, uid)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := []string{"engineering", "platform", "sales"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v (ORDER BY must be alphabetical)", got, want)
	}
}

// ReplaceDepartments with a non-existent userID fails with a FK constraint error.
// The delete succeeds (0 rows affected) but the subsequent insert is rejected.
func TestReplaceDepartments_NonExistentUser_ReturnsError(t *testing.T) {
	db := setupTestDB(t)
	repo := NewUserRepo(db.DB)

	err := repo.ReplaceDepartments(context.Background(), uuid.New(), []string{"engineering"})
	if err == nil {
		t.Error("expected error for non-existent user, got nil")
	}
}

// ReplaceDepartments for user A must not affect user B's department set.
// This pins the WHERE clause isolation in DeleteUserDepartments: a bug that
// deleted ALL rows (missing WHERE) would silently clear every user's departments.
func TestReplaceDepartments_UserIsolation(t *testing.T) {
	db := setupTestDB(t)
	repo := NewUserRepo(db.DB)

	uidA := newUserForDepts(t, repo)
	uidB := newUserForDepts(t, repo)
	ctx := context.Background()

	if err := repo.ReplaceDepartments(ctx, uidA, []string{"engineering"}); err != nil {
		t.Fatalf("seed A: %v", err)
	}
	if err := repo.ReplaceDepartments(ctx, uidB, []string{"sales"}); err != nil {
		t.Fatalf("seed B: %v", err)
	}

	// Replace A's departments.
	if err := repo.ReplaceDepartments(ctx, uidA, []string{"platform"}); err != nil {
		t.Fatalf("replace A: %v", err)
	}

	// B's departments must be unchanged.
	gotB, err := repo.ListDepartments(ctx, uidB)
	if err != nil {
		t.Fatalf("list B: %v", err)
	}
	if !reflect.DeepEqual(gotB, []string{"sales"}) {
		t.Errorf("user B departments = %v, want [sales] (must not be affected by user A replace)", gotB)
	}

	// A's departments reflect the replacement.
	gotA, err := repo.ListDepartments(ctx, uidA)
	if err != nil {
		t.Fatalf("list A: %v", err)
	}
	if !reflect.DeepEqual(gotA, []string{"platform"}) {
		t.Errorf("user A departments = %v, want [platform]", gotA)
	}
}
