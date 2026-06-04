package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

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
