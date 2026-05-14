package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/arbitron/arbitron/adapter/sqlite/generated"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

type VariableSnapshotRepo struct {
	db      *sql.DB
	queries *generated.Queries
}

func NewVariableSnapshotRepo(db *sql.DB) *VariableSnapshotRepo {
	return &VariableSnapshotRepo{
		db:      db,
		queries: generated.New(db),
	}
}

func (r *VariableSnapshotRepo) Create(ctx context.Context, snapshot *instance.VariableSnapshot) error {
	varsJSON, err := toJSONString(snapshot.Variables)
	if err != nil {
		return fmt.Errorf("marshalling variables: %w", err)
	}

	row, err := r.queries.CreateVariableSnapshot(ctx, generated.CreateVariableSnapshotParams{
		ID:         uuidToString(snapshot.ID),
		InstanceID: uuidToString(snapshot.InstanceID),
		NodeID:     snapshot.NodeID,
		Variables:  varsJSON,
	})
	if err != nil {
		return fmt.Errorf("creating variable snapshot: %w", err)
	}

	snapshot.CapturedAt = parseTime(row.CapturedAt)
	return nil
}

func (r *VariableSnapshotRepo) ListByInstance(ctx context.Context, instanceID uuid.UUID) ([]instance.VariableSnapshot, error) {
	rows, err := r.queries.ListVariableSnapshotsByInstance(ctx, uuidToString(instanceID))
	if err != nil {
		return nil, fmt.Errorf("listing variable snapshots: %w", err)
	}

	snapshots := make([]instance.VariableSnapshot, 0, len(rows))
	for _, row := range rows {
		vars, err := fromJSONString(row.Variables)
		if err != nil {
			return nil, fmt.Errorf("unmarshalling variables: %w", err)
		}
		snapshots = append(snapshots, instance.VariableSnapshot{
			ID:         parseUUID(row.ID),
			InstanceID: parseUUID(row.InstanceID),
			NodeID:     row.NodeID,
			Variables:  vars,
			CapturedAt: parseTime(row.CapturedAt),
		})
	}
	return snapshots, nil
}
