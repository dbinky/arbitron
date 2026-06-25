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

// QueueRepo implements instance.QueueRepository backed by SQLite.
type QueueRepo struct {
	db      *sql.DB
	queries *generated.Queries
}

// NewQueueRepo creates a QueueRepo using the given database connection.
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

	return rowToInstance(row)
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
	instances := make([]instance.Instance, 0, len(rows))
	for _, row := range rows {
		inst, err := rowToInstance(row)
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
	instances := make([]instance.Instance, 0, len(rows))
	for _, row := range rows {
		inst, err := rowToInstance(row)
		if err != nil {
			return nil, err
		}
		instances = append(instances, *inst)
	}
	return instances, nil
}

// ListQueued returns all currently-queued instances ordered by priority
// (high → normal → low) then QueuedAt FIFO within a priority — the same
// ordering NextEligible uses. An empty queue returns an empty slice.
func (r *QueueRepo) ListQueued(ctx context.Context) ([]instance.Instance, error) {
	query := `SELECT id, owner_id, workflow_name, workflow_snapshot, state, priority,
		variables, original_variables, current_node_id, failed_node_id,
		created_at, updated_at, completed_at, queued_at, waiting_until,
		webhook_token, webhook_secret_hash, webhook_received_at,
		department, visibility, trigger_source, schedule_id, workflow_id
		FROM instances WHERE state = 'queued' ORDER BY
		CASE priority
			WHEN 'high' THEN 1
			WHEN 'normal' THEN 2
			WHEN 'low' THEN 3
		END,
		queued_at ASC`
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("listing queued instances: %w", err)
	}
	defer rows.Close()

	instances := make([]instance.Instance, 0)
	for rows.Next() {
		inst, err := scanInstanceRow(rows)
		if err != nil {
			return nil, err
		}
		instances = append(instances, *inst)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating queued instances: %w", err)
	}
	return instances, nil
}

// scanInstanceRow scans a full instance row from *sql.Rows into a domain
// Instance, mirroring the generated Instance struct column order.
func scanInstanceRow(rows *sql.Rows) (*instance.Instance, error) {
	var i generated.Instance
	if err := rows.Scan(
		&i.ID, &i.OwnerID, &i.WorkflowName, &i.WorkflowSnapshot, &i.State, &i.Priority,
		&i.Variables, &i.OriginalVariables, &i.CurrentNodeID, &i.FailedNodeID,
		&i.CreatedAt, &i.UpdatedAt, &i.CompletedAt, &i.QueuedAt, &i.WaitingUntil,
		&i.WebhookToken, &i.WebhookSecretHash, &i.WebhookReceivedAt,
		&i.Department, &i.Visibility, &i.TriggerSource, &i.ScheduleID, &i.WorkflowID,
	); err != nil {
		return nil, fmt.Errorf("scanning instance row: %w", err)
	}
	return rowToInstance(i)
}
