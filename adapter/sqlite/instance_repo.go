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

type InstanceRepo struct {
	db      *sql.DB
	queries *generated.Queries
}

func NewInstanceRepo(db *sql.DB) *InstanceRepo {
	return &InstanceRepo{
		db:      db,
		queries: generated.New(db),
	}
}

func (r *InstanceRepo) Create(ctx context.Context, inst *instance.Instance) error {
	snapshotJSON, err := toJSONString(inst.WorkflowSnapshot)
	if err != nil {
		return fmt.Errorf("marshalling workflow snapshot: %w", err)
	}
	varsJSON, err := toJSONString(inst.Variables)
	if err != nil {
		return fmt.Errorf("marshalling variables: %w", err)
	}
	origVarsJSON, err := toJSONString(inst.OriginalVariables)
	if err != nil {
		return fmt.Errorf("marshalling original variables: %w", err)
	}

	row, err := r.queries.CreateInstance(ctx, generated.CreateInstanceParams{
		ID:                uuidToString(inst.ID),
		OwnerID:           uuidToString(inst.OwnerID),
		WorkflowName:      inst.WorkflowName,
		WorkflowSnapshot:  snapshotJSON,
		State:             string(inst.State),
		Priority:          string(inst.Priority),
		Variables:         varsJSON,
		OriginalVariables: origVarsJSON,
		QueuedAt:          timeToNullString(inst.QueuedAt),
	})
	if err != nil {
		return fmt.Errorf("creating instance: %w", err)
	}

	inst.CreatedAt = parseTime(row.CreatedAt)
	inst.UpdatedAt = parseTime(row.UpdatedAt)
	return nil
}

func (r *InstanceRepo) Get(ctx context.Context, id uuid.UUID) (*instance.Instance, error) {
	row, err := r.queries.GetInstance(ctx, uuidToString(id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
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
		Limit:  int64(filters.Limit),
		Offset: int64(filters.Offset),
	}
	if filters.State != nil {
		params.State = string(*filters.State)
	}
	if filters.WorkflowName != nil {
		params.WorkflowName = *filters.WorkflowName
	}
	if filters.Since != nil {
		params.Since = timeToString(*filters.Since)
	}

	rows, err := r.queries.ListInstances(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("listing instances: %w", err)
	}

	summaries := make([]instance.InstanceSummary, len(rows))
	for i, row := range rows {
		summaries[i] = instance.InstanceSummary{
			ID:            parseUUID(row.ID),
			WorkflowName:  row.WorkflowName,
			State:         instance.State(row.State),
			Priority:      instance.Priority(row.Priority),
			CurrentNodeID: nullStringToPtr(row.CurrentNodeID),
			CreatedAt:     parseTime(row.CreatedAt),
			UpdatedAt:     parseTime(row.UpdatedAt),
			CompletedAt:   nullStringToTimePtr(row.CompletedAt),
			QueuedAt:      nullStringToTimePtr(row.QueuedAt),
		}
	}
	return summaries, nil
}

func (r *InstanceRepo) UpdateState(ctx context.Context, id uuid.UUID, state instance.State) error {
	err := r.queries.UpdateInstanceState(ctx, generated.UpdateInstanceStateParams{
		ID:    uuidToString(id),
		State: string(state),
	})
	if err != nil {
		return fmt.Errorf("updating instance state: %w", err)
	}
	return nil
}

func (r *InstanceRepo) UpdateVariables(ctx context.Context, id uuid.UUID, variables map[string]any) error {
	varsJSON, err := toJSONString(variables)
	if err != nil {
		return fmt.Errorf("marshalling variables: %w", err)
	}
	err = r.queries.UpdateInstanceVariables(ctx, generated.UpdateInstanceVariablesParams{
		ID:        uuidToString(id),
		Variables: varsJSON,
	})
	if err != nil {
		return fmt.Errorf("updating instance variables: %w", err)
	}
	return nil
}

func (r *InstanceRepo) UpdateCurrentNode(ctx context.Context, id uuid.UUID, nodeID string) error {
	err := r.queries.UpdateInstanceCurrentNode(ctx, generated.UpdateInstanceCurrentNodeParams{
		ID:            uuidToString(id),
		CurrentNodeID: stringToNullString(nodeID),
	})
	if err != nil {
		return fmt.Errorf("updating current node: %w", err)
	}
	return nil
}

func (r *InstanceRepo) SetFailed(ctx context.Context, id uuid.UUID, failedNodeID string) error {
	err := r.queries.SetInstanceFailed(ctx, generated.SetInstanceFailedParams{
		ID:           uuidToString(id),
		FailedNodeID: stringToNullString(failedNodeID),
	})
	if err != nil {
		return fmt.Errorf("setting instance failed: %w", err)
	}
	return nil
}

func (r *InstanceRepo) SetCompleted(ctx context.Context, id uuid.UUID) error {
	err := r.queries.SetInstanceCompleted(ctx, uuidToString(id))
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

func (r *InstanceRepo) UpdatePriority(ctx context.Context, id uuid.UUID, priority instance.Priority) error {
	err := r.queries.UpdateInstancePriority(ctx, generated.UpdateInstancePriorityParams{
		ID:       uuidToString(id),
		Priority: string(priority),
	})
	if err != nil {
		return fmt.Errorf("updating instance priority: %w", err)
	}
	return nil
}

func (r *InstanceRepo) SetWaiting(ctx context.Context, id uuid.UUID, waitingUntil *time.Time) error {
	idStr := uuidToString(id)
	if waitingUntil != nil {
		_, err := r.db.ExecContext(ctx,
			`UPDATE instances SET state = 'waiting', waiting_until = ?, updated_at = datetime('now') WHERE id = ?`,
			timeToString(*waitingUntil), idStr)
		if err != nil {
			return fmt.Errorf("setting instance waiting: %w", err)
		}
	} else {
		_, err := r.db.ExecContext(ctx,
			`UPDATE instances SET waiting_until = NULL, updated_at = datetime('now') WHERE id = ?`,
			idStr)
		if err != nil {
			return fmt.Errorf("clearing waiting_until: %w", err)
		}
	}
	return nil
}

func (r *InstanceRepo) SetWebhookFields(ctx context.Context, id uuid.UUID, token *string, secretHash *string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE instances SET webhook_token = ?, webhook_secret_hash = ?, updated_at = datetime('now') WHERE id = ?`,
		token, secretHash, uuidToString(id))
	if err != nil {
		return fmt.Errorf("setting webhook fields: %w", err)
	}
	return nil
}

func (r *InstanceRepo) SetWebhookReceived(ctx context.Context, id uuid.UUID, receivedAt time.Time) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE instances SET webhook_received_at = ?, updated_at = datetime('now') WHERE id = ?`,
		timeToString(receivedAt), uuidToString(id))
	if err != nil {
		return fmt.Errorf("setting webhook received: %w", err)
	}
	return nil
}

func (r *InstanceRepo) Stats(ctx context.Context, workflowName string, from time.Time, to time.Time) (*instance.WorkflowStats, error) {
	query := `
SELECT
  COUNT(*) AS total_runs,
  SUM(CASE WHEN state = 'completed' THEN 1 ELSE 0 END) AS success_count,
  SUM(CASE WHEN state = 'failed' THEN 1 ELSE 0 END) AS failure_count,
  COALESCE(AVG(CASE WHEN state = 'completed' AND completed_at IS NOT NULL
    THEN (julianday(completed_at) - julianday(created_at)) * 86400.0
    ELSE NULL END), 0) AS avg_duration_secs
FROM instances
WHERE workflow_name = ?
  AND created_at >= ?
  AND created_at <= ?`

	var stats instance.WorkflowStats
	err := r.db.QueryRowContext(ctx, query, workflowName, timeToString(from), timeToString(to)).Scan(
		&stats.TotalRuns,
		&stats.SuccessCount,
		&stats.FailureCount,
		&stats.AvgDurationSecs,
	)
	if err != nil {
		return nil, fmt.Errorf("querying workflow stats: %w", err)
	}
	return &stats, nil
}

func (r *InstanceRepo) rowToInstance(row generated.Instance) (*instance.Instance, error) {
	snapshot, err := fromJSONString(row.WorkflowSnapshot)
	if err != nil {
		return nil, fmt.Errorf("unmarshalling workflow snapshot: %w", err)
	}
	vars, err := fromJSONString(row.Variables)
	if err != nil {
		return nil, fmt.Errorf("unmarshalling variables: %w", err)
	}
	origVars, err := fromJSONString(row.OriginalVariables)
	if err != nil {
		return nil, fmt.Errorf("unmarshalling original variables: %w", err)
	}

	return &instance.Instance{
		ID:                parseUUID(row.ID),
		OwnerID:           parseUUID(row.OwnerID),
		WorkflowName:      row.WorkflowName,
		WorkflowSnapshot:  snapshot,
		State:             instance.State(row.State),
		Priority:          instance.Priority(row.Priority),
		Variables:         vars,
		OriginalVariables: origVars,
		CurrentNodeID:     nullStringToPtr(row.CurrentNodeID),
		FailedNodeID:      nullStringToPtr(row.FailedNodeID),
		CreatedAt:         parseTime(row.CreatedAt),
		UpdatedAt:         parseTime(row.UpdatedAt),
		CompletedAt:       nullStringToTimePtr(row.CompletedAt),
		QueuedAt:          nullStringToTimePtr(row.QueuedAt),
		WaitingUntil:      nullStringToTimePtr(row.WaitingUntil),
		WebhookToken:      nullStringToPtr(row.WebhookToken),
		WebhookSecretHash: nullStringToPtr(row.WebhookSecretHash),
		WebhookReceivedAt: nullStringToTimePtr(row.WebhookReceivedAt),
	}, nil
}
