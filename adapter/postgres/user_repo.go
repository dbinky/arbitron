package postgres

import (
	"context"
	"fmt"

	"github.com/arbitron/arbitron/adapter/postgres/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// UserRepo implements instance.UserRepository using Postgres via sqlc.
type UserRepo struct {
	pool    *pgxpool.Pool
	queries *generated.Queries
}

// NewUserRepo creates a new UserRepo backed by the given connection pool.
func NewUserRepo(pool *pgxpool.Pool) *UserRepo {
	return &UserRepo{
		pool:    pool,
		queries: generated.New(pool),
	}
}

func (r *UserRepo) GetDefaultUser() (uuid.UUID, error) {
	user, err := r.queries.GetDefaultUser(context.Background())
	if err != nil {
		return uuid.Nil, fmt.Errorf("getting default user: %w", err)
	}
	return fromPgUUID(user.ID), nil
}
