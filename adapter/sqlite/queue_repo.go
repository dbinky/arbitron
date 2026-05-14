package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/adapter/sqlite/generated"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

type QueueRepo struct {
	db      *sql.DB
	queries *generated.Queries
}

func NewQueueRepo(db *sql.DB) *QueueRepo {
	return &QueueRepo{
		db:      db,
		queries: generated.New(db),
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
	count, err := r.queries.CountRunningByOwner(ctx, uuidToString(ownerID))
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
		GlobalCap:  int64(globalCap),
		PerUserCap: int64(perUserCap),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting next eligible instance: %w", err)
	}

	repo := &InstanceRepo{db: r.db, queries: r.queries}
	return repo.rowToInstance(row)
}

func (r *QueueRepo) SetQueued(ctx context.Context, id uuid.UUID, queuedAt time.Time) error {
	err := r.queries.SetQueued(ctx, generated.SetQueuedParams{
		ID:       uuidToString(id),
		QueuedAt: stringToNullString(timeToString(queuedAt)),
	})
	if err != nil {
		return fmt.Errorf("setting instance queued: %w", err)
	}
	return nil
}

func (r *QueueRepo) ListWaitingWithTimer(ctx context.Context) ([]instance.Instance, error) {
	rows, err := r.queries.ListWaitingWithTimer(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing waiting instances with timer: %w", err)
	}
	repo := &InstanceRepo{db: r.db, queries: r.queries}
	instances := make([]instance.Instance, 0, len(rows))
	for _, row := range rows {
		inst, err := repo.rowToInstance(row)
		if err != nil {
			return nil, err
		}
		instances = append(instances, *inst)
	}
	return instances, nil
}

func (r *QueueRepo) ListWaitingWithWebhook(ctx context.Context) ([]instance.Instance, error) {
	rows, err := r.queries.ListWaitingWithWebhook(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing waiting instances with webhook: %w", err)
	}
	repo := &InstanceRepo{db: r.db, queries: r.queries}
	instances := make([]instance.Instance, 0, len(rows))
	for _, row := range rows {
		inst, err := repo.rowToInstance(row)
		if err != nil {
			return nil, err
		}
		instances = append(instances, *inst)
	}
	return instances, nil
}
