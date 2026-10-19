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

// These three queue projections use the shared full-row column list so depth
// and group-task links cannot be silently dropped by stale generated queries.
// Keep predicates and ordering in lockstep with queries/queue.sql.
const nextEligibleWithLinkSQL = `SELECT ` + instanceColumnsWithLink + ` FROM instances i
WHERE i.state = 'queued'
  AND (?1 = 0 OR (SELECT COUNT(*) FROM instances WHERE state = 'running') < ?1)
  AND (?2 = 0 OR (SELECT COUNT(*) FROM instances WHERE state = 'running' AND owner_id = i.owner_id) < ?2)
ORDER BY
  CASE i.priority
    WHEN 'high' THEN 1
    WHEN 'normal' THEN 2
    WHEN 'low' THEN 3
  END,
  i.queued_at ASC
LIMIT 1`

const listWaitingWithTimerWithLinkSQL = `SELECT ` + instanceColumnsWithLink +
	` FROM instances WHERE state = 'waiting' AND waiting_until IS NOT NULL AND webhook_token IS NULL`

const listWaitingWithWebhookWithLinkSQL = `SELECT ` + instanceColumnsWithLink +
	` FROM instances WHERE state = 'waiting' AND webhook_token IS NOT NULL`

func (r *QueueRepo) NextEligible(ctx context.Context, globalCap int, perUserCap int) (*instance.Instance, error) {
	row := r.db.QueryRowContext(ctx, nextEligibleWithLinkSQL, int64(globalCap), int64(perUserCap))
	inst, err := scanInstanceWithLink(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting next eligible instance: %w", err)
	}
	return inst, nil
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

// SetQueuedIfWaiting implements instance.QueueRepository.
//
// The `state = 'waiting'` predicate is the whole point: it makes the re-queue
// atomic against the spawn executor's competing claim on the same row, so a
// parent cannot end up queued while a run goroutine is already executing it.
// Hand-written (not sqlc-generated) because the generated SetQueued is :exec and
// discards the row count this needs.
func (r *QueueRepo) SetQueuedIfWaiting(ctx context.Context, id uuid.UUID, queuedAt time.Time) (bool, error) {
	const q = `UPDATE instances
  SET state = 'queued', queued_at = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ')
  WHERE id = ? AND state = 'waiting'`
	res, err := r.db.ExecContext(ctx, q, stringToNullString(timeToString(queuedAt)), uuidToString(id))
	if err != nil {
		return false, fmt.Errorf("re-queueing waiting instance: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("re-queueing waiting instance: %w", err)
	}
	return n > 0, nil
}

// SetQueuedIfWaitingAtNode implements instance.NodeAddressedWaker.
//
// It is SetQueuedIfWaiting with the node conjunct ClaimWaitingParent already
// uses: the parent is re-queued ONLY IF it is still `waiting` AND its
// current node is nodeID. A group coordinator waking "the parent waiting at
// group node G" must not re-queue a parent that has since moved on and is
// waiting somewhere else (a later spawn or group node, a timer, a webhook) —
// that parent would resume at the wrong node. Because the predicate also
// requires `waiting`, a cancelled, failed or completed parent is never
// resurrected, and the same ONE conditional UPDATE makes this wake mutually
// exclusive with the executor's ClaimWaitingParent on the same row.
//
// A false return is the normal "somebody else moved it / it is not waiting
// here" outcome, not an error. An empty nodeID is refused: no waiting parent
// can legitimately be addressed by it, and silently matching nothing would
// hide a caller bug.
func (r *QueueRepo) SetQueuedIfWaitingAtNode(ctx context.Context, id uuid.UUID, nodeID string, queuedAt time.Time) (bool, error) {
	if nodeID == "" {
		return false, fmt.Errorf("re-queueing waiting instance %s: node id is required", id)
	}
	const q = `UPDATE instances
  SET state = 'queued', queued_at = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ')
  WHERE id = ? AND state = 'waiting' AND current_node_id = ?`
	res, err := r.db.ExecContext(ctx, q, stringToNullString(timeToString(queuedAt)), uuidToString(id), nodeID)
	if err != nil {
		return false, fmt.Errorf("re-queueing instance %s waiting at node %q: %w", id, nodeID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("re-queueing instance %s waiting at node %q: %w", id, nodeID, err)
	}
	return n > 0, nil
}

func (r *QueueRepo) ListWaitingWithTimer(ctx context.Context) ([]instance.Instance, error) {
	instances, err := queryInstancesWithLink(ctx, r.db, listWaitingWithTimerWithLinkSQL)
	if err != nil {
		return nil, fmt.Errorf("listing waiting instances with timer: %w", err)
	}
	return instances, nil
}

func (r *QueueRepo) ListWaitingWithWebhook(ctx context.Context) ([]instance.Instance, error) {
	instances, err := queryInstancesWithLink(ctx, r.db, listWaitingWithWebhookWithLinkSQL)
	if err != nil {
		return nil, fmt.Errorf("listing waiting instances with webhook: %w", err)
	}
	return instances, nil
}

// ListQueued returns all currently-queued instances ordered by priority
// (high → normal → low) then QueuedAt FIFO within a priority — the same
// ordering NextEligible uses. An empty queue returns an empty slice.
func (r *QueueRepo) ListQueued(ctx context.Context) ([]instance.Instance, error) {
	rows, err := r.queries.ListQueued(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing queued instances: %w", err)
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
