package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/arbitron/arbitron/adapter/sqlite/generated"
	"github.com/google/uuid"
)

type UserRepo struct {
	db      *sql.DB
	queries *generated.Queries
}

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
	return parseUUID(user.ID), nil
}
