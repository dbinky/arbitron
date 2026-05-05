package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/adapter/postgres/generated"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// QueueRepo implements instance.QueueRepository using Postgres via sqlc.
type QueueRepo struct {
	pool    *pgxpool.Pool
	queries *generated.Queries
}

// NewQueueRepo creates a new QueueRepo backed by the given connection pool.
func NewQueueRepo(pool *pgxpool.Pool) *QueueRepo {
	return &QueueRepo{
		pool:    pool,
		queries: generated.New(pool),
	}
}

func (r *QueueRepo) CountRunning(ctx context.Context) (int, error) {
	count, err := r.queries.CountRunning(ctx)
	if err != nil {
		return 0, fmt.Errorf("counting running instances: %w", err)
	}
	return int(count), nil
}

func (r *QueueRepo) CountRunningByOwner(ctx context.Context, ownerID uuid.UUID) (int, error) {
	count, err := r.queries.CountRunningByOwner(ctx, toPgUUID(ownerID))
	if err != nil {
		return 0, fmt.Errorf("counting running instances by owner: %w", err)
	}
	return int(count), nil
}

func (r *QueueRepo) CountQueued(ctx context.Context) (int, error) {
	count, err := r.queries.CountQueued(ctx)
	if err != nil {
		return 0, fmt.Errorf("counting queued instances: %w", err)
	}
	return int(count), nil
}

func (r *QueueRepo) CountWaiting(ctx context.Context) (int, error) {
	count, err := r.queries.CountWaiting(ctx)
	if err != nil {
		return 0, fmt.Errorf("counting waiting instances: %w", err)
	}
	return int(count), nil
}

func (r *QueueRepo) NextEligible(ctx context.Context, globalCap int, perUserCap int) (*instance.Instance, error) {
	row, err := r.queries.NextEligible(ctx, generated.NextEligibleParams{
		GlobalCap:  int32(globalCap),
		PerUserCap: int32(perUserCap),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting next eligible instance: %w", err)
	}

	// Reuse the InstanceRepo's row conversion logic.
	repo := &InstanceRepo{pool: r.pool, queries: r.queries}
	return repo.rowToInstance(row)
}

func (r *QueueRepo) SetQueued(ctx context.Context, id uuid.UUID, queuedAt time.Time) error {
	err := r.queries.SetQueued(ctx, generated.SetQueuedParams{
		ID:       toPgUUID(id),
		QueuedAt: toPgTimestamptz(queuedAt),
	})
	if err != nil {
		return fmt.Errorf("setting instance queued: %w", err)
	}
	return nil
}

func (r *QueueRepo) ListWaitingWithTimer(_ context.Context) ([]instance.Instance, error) {
	// Stub: implemented in Phase 2 Task 5
	return nil, nil
}

func (r *QueueRepo) ListWaitingWithWebhook(_ context.Context) ([]instance.Instance, error) {
	// Stub: implemented in Phase 3 Task 6
	return nil, nil
}
