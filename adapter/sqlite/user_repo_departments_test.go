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
