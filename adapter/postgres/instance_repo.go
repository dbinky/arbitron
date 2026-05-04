package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/adapter/postgres/generated"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// InstanceRepo implements instance.InstanceRepository using Postgres via sqlc.
type InstanceRepo struct {
	pool    *pgxpool.Pool
	queries *generated.Queries
}

// NewInstanceRepo creates a new InstanceRepo backed by the given connection pool.
func NewInstanceRepo(pool *pgxpool.Pool) *InstanceRepo {
	return &InstanceRepo{
		pool:    pool,
		queries: generated.New(pool),
	}
}

func (r *InstanceRepo) Create(ctx context.Context, inst *instance.Instance) error {
	snapshotJSON, err := toJSON(inst.WorkflowSnapshot)
	if err != nil {
		return fmt.Errorf("marshalling workflow snapshot: %w", err)
	}
	varsJSON, err := toJSON(inst.Variables)
	if err != nil {
		return fmt.Errorf("marshalling variables: %w", err)
	}
	origVarsJSON, err := toJSON(inst.OriginalVariables)
	if err != nil {
		return fmt.Errorf("marshalling original variables: %w", err)
	}

	row, err := r.queries.CreateInstance(ctx, generated.CreateInstanceParams{
		ID:                toPgUUID(inst.ID),
		OwnerID:           toPgUUID(inst.OwnerID),
		WorkflowName:      inst.WorkflowName,
		WorkflowSnapshot:  snapshotJSON,
		State:             generated.InstanceState(inst.State),
		Priority:          generated.InstancePriority(inst.Priority),
		Variables:         varsJSON,
		OriginalVariables: origVarsJSON,
	})
	if err != nil {
		return fmt.Errorf("creating instance: %w", err)
	}

	inst.CreatedAt = fromPgTimestamptz(row.CreatedAt)
	inst.UpdatedAt = fromPgTimestamptz(row.UpdatedAt)
	return nil
}

func (r *InstanceRepo) Get(ctx context.Context, id uuid.UUID) (*instance.Instance, error) {
	row, err := r.queries.GetInstance(ctx, toPgUUID(id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("instance not found: %s", id)
		}
		return nil, fmt.Errorf("getting instance: %w", err)
	}
	return r.rowToInstance(row)
}

func (r *InstanceRepo) List(ctx context.Context, filters instance.ListFilters) ([]instance.InstanceSummary, error) {
	if filters.Limit <= 0 {
		filters.Limit = 50
	}
	params := generated.ListInstancesParams{
		Limit:  int32(filters.Limit),
		Offset: int32(filters.Offset),
	}
	if filters.State != nil {
		params.State = generated.NullInstanceState{
			InstanceState: generated.InstanceState(*filters.State),
			Valid:         true,
		}
	}
	if filters.WorkflowName != nil {
		params.WorkflowName = toPgText(*filters.WorkflowName)
	}

	rows, err := r.queries.ListInstances(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("listing instances: %w", err)
	}

	summaries := make([]instance.InstanceSummary, len(rows))
	for i, row := range rows {
		summaries[i] = instance.InstanceSummary{
			ID:            fromPgUUID(row.ID),
			WorkflowName:  row.WorkflowName,
			State:         instance.State(row.State),
			Priority:      instance.Priority(row.Priority),
			CurrentNodeID: fromPgTextPtr(row.CurrentNodeID),
			CreatedAt:     fromPgTimestamptz(row.CreatedAt),
			UpdatedAt:     fromPgTimestamptz(row.UpdatedAt),
			CompletedAt:   fromPgTimestamptzPtr(row.CompletedAt),
		}
	}
	return summaries, nil
}

func (r *InstanceRepo) UpdateState(ctx context.Context, id uuid.UUID, state instance.State) error {
	err := r.queries.UpdateInstanceState(ctx, generated.UpdateInstanceStateParams{
		ID:    toPgUUID(id),
		State: generated.InstanceState(state),
	})
	if err != nil {
		return fmt.Errorf("updating instance state: %w", err)
	}
	return nil
}

func (r *InstanceRepo) UpdateVariables(ctx context.Context, id uuid.UUID, variables map[string]any) error {
	varsJSON, err := toJSON(variables)
	if err != nil {
		return fmt.Errorf("marshalling variables: %w", err)
	}
	err = r.queries.UpdateInstanceVariables(ctx, generated.UpdateInstanceVariablesParams{
		ID:        toPgUUID(id),
		Variables: varsJSON,
	})
	if err != nil {
		return fmt.Errorf("updating instance variables: %w", err)
	}
	return nil
}

func (r *InstanceRepo) UpdateCurrentNode(ctx context.Context, id uuid.UUID, nodeID string) error {
	err := r.queries.UpdateInstanceCurrentNode(ctx, generated.UpdateInstanceCurrentNodeParams{
		ID:            toPgUUID(id),
		CurrentNodeID: toPgText(nodeID),
	})
	if err != nil {
		return fmt.Errorf("updating current node: %w", err)
	}
	return nil
}

func (r *InstanceRepo) SetFailed(ctx context.Context, id uuid.UUID, failedNodeID string) error {
	err := r.queries.SetInstanceFailed(ctx, generated.SetInstanceFailedParams{
		ID:           toPgUUID(id),
		FailedNodeID: toPgText(failedNodeID),
	})
	if err != nil {
		return fmt.Errorf("setting instance failed: %w", err)
	}
	return nil
}

func (r *InstanceRepo) SetCompleted(ctx context.Context, id uuid.UUID) error {
	err := r.queries.SetInstanceCompleted(ctx, toPgUUID(id))
	if err != nil {
		return fmt.Errorf("setting instance completed: %w", err)
	}
	return nil
}

func (r *InstanceRepo) ListRunning(ctx context.Context) ([]instance.Instance, error) {
	rows, err := r.queries.ListRunningInstances(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing running instances: %w", err)
	}

	instances := make([]instance.Instance, 0, len(rows))
	for _, row := range rows {
		inst, err := r.rowToInstance(row)
		if err != nil {
			return nil, err
		}
		instances = append(instances, *inst)
	}
	return instances, nil
}

func (r *InstanceRepo) rowToInstance(row generated.Instance) (*instance.Instance, error) {
	snapshot, err := fromJSON(row.WorkflowSnapshot)
	if err != nil {
		return nil, fmt.Errorf("unmarshalling workflow snapshot: %w", err)
	}
	vars, err := fromJSON(row.Variables)
	if err != nil {
		return nil, fmt.Errorf("unmarshalling variables: %w", err)
	}
	origVars, err := fromJSON(row.OriginalVariables)
	if err != nil {
		return nil, fmt.Errorf("unmarshalling original variables: %w", err)
	}

	return &instance.Instance{
		ID:                fromPgUUID(row.ID),
		OwnerID:           fromPgUUID(row.OwnerID),
		WorkflowName:      row.WorkflowName,
		WorkflowSnapshot:  snapshot,
		State:             instance.State(row.State),
		Priority:          instance.Priority(row.Priority),
		Variables:         vars,
		OriginalVariables: origVars,
		CurrentNodeID:     fromPgTextPtr(row.CurrentNodeID),
		FailedNodeID:      fromPgTextPtr(row.FailedNodeID),
		CreatedAt:         fromPgTimestamptz(row.CreatedAt),
		UpdatedAt:         fromPgTimestamptz(row.UpdatedAt),
		CompletedAt:       fromPgTimestamptzPtr(row.CompletedAt),
	}, nil
}
