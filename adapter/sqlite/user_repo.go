package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/arbitron/arbitron/adapter/sqlite/generated"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

var _ instance.UserRepository = (*UserRepo)(nil)

// UserRepo implements instance.UserRepository backed by SQLite.
type UserRepo struct {
	db      *sql.DB
	queries *generated.Queries
}

// NewUserRepo creates a UserRepo using the given database connection.
func NewUserRepo(db *sql.DB) *UserRepo {
	return &UserRepo{
		db:      db,
		queries: generated.New(db),
	}
}

// toDomainUser maps the (identically-shaped) generated user row columns to the
// domain User. The sqlc queries return distinct per-query row structs, so the
// caller passes the individual columns.
func toDomainUser(id string, subject, email sql.NullString, username, role string) (*instance.User, error) {
	uid, err := parseUUID(id)
	if err != nil {
		return nil, err
	}
	return &instance.User{
		ID:       uid,
		Subject:  nullStringToPtr(subject),
		Email:    nullStringToPtr(email),
		Username: username,
		Role:     identity.Role(role),
	}, nil
}

func (r *UserRepo) GetDefaultUser(ctx context.Context) (uuid.UUID, error) {
	user, err := r.queries.GetDefaultUser(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("getting default user: %w", err)
	}
	id, err := parseUUID(user.ID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("getting default user: %w", err)
	}
	return id, nil
}

func (r *UserRepo) GetByID(ctx context.Context, id uuid.UUID) (*instance.User, error) {
	row, err := r.queries.GetUserByID(ctx, uuidToString(id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("getting user %s: %w", id, instance.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("getting user %s: %w", id, err)
	}
	return toDomainUser(row.ID, row.Subject, row.Email, row.Username, row.Role)
}

// UsersByID batch-resolves users by ID in a single query using a parameterized
// IN clause. An empty ids slice short-circuits to an empty map (no query). IDs
// with no matching row are absent from the result. rows.Err is checked so a
// mid-iteration failure surfaces (the enricher degrades the submitter field
// rather than swallowing it silently).
func (r *UserRepo) UsersByID(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]instance.User, error) {
	out := make(map[uuid.UUID]instance.User, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = uuidToString(id)
	}
	query := `SELECT id, subject, email, username, role FROM users WHERE id IN (` +
		strings.Join(placeholders, ",") + `)`
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("batch-getting users by id: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, username, role string
		var subject, email sql.NullString
		if err := rows.Scan(&id, &subject, &email, &username, &role); err != nil {
			return nil, fmt.Errorf("scanning user row: %w", err)
		}
		u, err := toDomainUser(id, subject, email, username, role)
		if err != nil {
			return nil, fmt.Errorf("converting user row: %w", err)
		}
		out[u.ID] = *u
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating users by id: %w", err)
	}
	return out, nil
}

func (r *UserRepo) GetBySubject(ctx context.Context, subject string) (*instance.User, error) {
	// SECURITY: an empty subject must never match the NULL-subject default user.
	// In SQL `subject = ''` never matches a NULL row, but we short-circuit
	// defensively so an empty OIDC subject claim can never resolve any user.
	if subject == "" {
		return nil, fmt.Errorf("getting user by subject: %w", instance.ErrNotFound)
	}
	row, err := r.queries.GetUserBySubject(ctx, sql.NullString{String: subject, Valid: true})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("getting user by subject: %w", instance.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("getting user by subject: %w", err)
	}
	return toDomainUser(row.ID, row.Subject, row.Email, row.Username, row.Role)
}

func (r *UserRepo) GetByEmail(ctx context.Context, email string) (*instance.User, error) {
	// SECURITY: an empty email must never match a NULL-email row. In SQL
	// `email = ''` never matches a NULL row, but we short-circuit defensively so
	// an empty/absent edge email claim can never resolve any user (mirrors the
	// GetBySubject empty-input guard).
	if email == "" {
		return nil, fmt.Errorf("getting user by email: %w", instance.ErrNotFound)
	}
	// The query lower()s the column, so pass an already-lowercased value to make
	// the comparison case-insensitive (lower(email) = ?).
	row, err := r.queries.GetUserByEmail(ctx, sql.NullString{String: strings.ToLower(email), Valid: true})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("getting user by email: %w", instance.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("getting user by email: %w", err)
	}
	return toDomainUser(row.ID, row.Subject, row.Email, row.Username, row.Role)
}

func (r *UserRepo) Create(ctx context.Context, u *instance.User) error {
	err := r.queries.CreateUser(ctx, generated.CreateUserParams{
		ID:       uuidToString(u.ID),
		Subject:  ptrToNullString(u.Subject),
		Email:    ptrToNullString(u.Email),
		Username: u.Username,
		Role:     string(u.Role),
	})
	if err != nil {
		return fmt.Errorf("creating user: %w", err)
	}
	return nil
}

func (r *UserRepo) UpdateUsername(ctx context.Context, id uuid.UUID, username string) error {
	err := r.queries.UpdateUsername(ctx, generated.UpdateUsernameParams{
		Username: username,
		ID:       uuidToString(id),
	})
	if err != nil {
		return fmt.Errorf("updating username for user %s: %w", id, err)
	}
	return nil
}

func (r *UserRepo) List(ctx context.Context) ([]instance.User, error) {
	rows, err := r.queries.ListUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}
	out := make([]instance.User, 0, len(rows))
	for _, row := range rows {
		u, err := toDomainUser(row.ID, row.Subject, row.Email, row.Username, row.Role)
		if err != nil {
			return nil, fmt.Errorf("listing users: %w", err)
		}
		out = append(out, *u)
	}
	return out, nil
}

func (r *UserRepo) UpdateRole(ctx context.Context, id uuid.UUID, role string) error {
	err := r.queries.UpdateUserRole(ctx, generated.UpdateUserRoleParams{
		Role: role,
		ID:   uuidToString(id),
	})
	if err != nil {
		return fmt.Errorf("updating role for user %s: %w", id, err)
	}
	return nil
}

func (r *UserRepo) CountAdmins(ctx context.Context) (int, error) {
	n, err := r.queries.CountAdmins(ctx)
	if err != nil {
		return 0, fmt.Errorf("counting admins: %w", err)
	}
	return int(n), nil
}

// ListDepartments returns the user's persisted department set, sorted by the
// ORDER BY department in the query.
func (r *UserRepo) ListDepartments(ctx context.Context, userID uuid.UUID) ([]string, error) {
	rows, err := r.queries.ListUserDepartments(ctx, uuidToString(userID))
	if err != nil {
		return nil, fmt.Errorf("listing departments for user %s: %w", userID, err)
	}
	return rows, nil
}

// ReplaceDepartments atomically supersedes the user's department set: delete-all
// then insert each value, in a single transaction. The caller passes already
// NORMALIZED values; ON CONFLICT DO NOTHING makes duplicate values within one
// call harmless. An empty/nil slice clears the set.
func (r *UserRepo) ReplaceDepartments(ctx context.Context, userID uuid.UUID, departments []string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning replace-departments transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit

	qtx := r.queries.WithTx(tx)
	uid := uuidToString(userID)
	if err := qtx.DeleteUserDepartments(ctx, uid); err != nil {
		return fmt.Errorf("clearing departments for user %s: %w", userID, err)
	}
	for _, d := range departments {
		if err := qtx.InsertUserDepartment(ctx, generated.InsertUserDepartmentParams{
			UserID:     uid,
			Department: d,
		}); err != nil {
			return fmt.Errorf("inserting department %q for user %s: %w", d, userID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing replace-departments transaction: %w", err)
	}
	return nil
}
