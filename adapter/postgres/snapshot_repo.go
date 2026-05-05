package postgres

import (
	"context"
	"fmt"

	"github.com/arbitron/arbitron/adapter/postgres/generated"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// VariableSnapshotRepo implements instance.VariableSnapshotRepository using Postgres via sqlc.
type VariableSnapshotRepo struct {
	pool    *pgxpool.Pool
	queries *generated.Queries
}

// NewVariableSnapshotRepo creates a new VariableSnapshotRepo backed by the given connection pool.
func NewVariableSnapshotRepo(pool *pgxpool.Pool) *VariableSnapshotRepo {
	return &VariableSnapshotRepo{
		pool:    pool,
		queries: generated.New(pool),
	}
}

func (r *VariableSnapshotRepo) Create(ctx context.Context, snapshot *instance.VariableSnapshot) error {
	varsJSON, err := toJSON(snapshot.Variables)
	if err != nil {
		return fmt.Errorf("marshalling variables: %w", err)
	}

	row, err := r.queries.CreateVariableSnapshot(ctx, generated.CreateVariableSnapshotParams{
		ID:         toPgUUID(snapshot.ID),
		InstanceID: toPgUUID(snapshot.InstanceID),
		NodeID:     snapshot.NodeID,
		Variables:  varsJSON,
	})
	if err != nil {
		return fmt.Errorf("creating variable snapshot: %w", err)
	}

	snapshot.CapturedAt = fromPgTimestamptz(row.CapturedAt)
	return nil
}

func (r *VariableSnapshotRepo) ListByInstance(ctx context.Context, instanceID uuid.UUID) ([]instance.VariableSnapshot, error) {
	rows, err := r.queries.ListVariableSnapshotsByInstance(ctx, toPgUUID(instanceID))
	if err != nil {
		return nil, fmt.Errorf("listing variable snapshots: %w", err)
	}

	snapshots := make([]instance.VariableSnapshot, 0, len(rows))
	for _, row := range rows {
		vars, err := fromJSON(row.Variables)
		if err != nil {
			return nil, fmt.Errorf("unmarshalling variables: %w", err)
		}
		snapshots = append(snapshots, instance.VariableSnapshot{
			ID:         fromPgUUID(row.ID),
			InstanceID: fromPgUUID(row.InstanceID),
			NodeID:     row.NodeID,
			Variables:  vars,
			CapturedAt: fromPgTimestamptz(row.CapturedAt),
		})
	}
	return snapshots, nil
}
