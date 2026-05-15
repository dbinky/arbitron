package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/arbitron/arbitron/adapter/sqlite/generated"
	"github.com/google/uuid"
)

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
