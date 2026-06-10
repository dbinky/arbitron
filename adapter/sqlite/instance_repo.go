package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/adapter/sqlite/generated"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// InstanceRepo implements instance.InstanceRepository backed by SQLite.
type InstanceRepo struct {
	db      *sql.DB
	queries *generated.Queries
}

// NewInstanceRepo creates an InstanceRepo using the given database connection.
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

	// Default an unset visibility to private (owner-only), matching the column
	// default and the pre-Phase-3 behavior of instances created without an
	// explicit audience.
	visibility := inst.Visibility
	if visibility == "" {
		visibility = instance.VisibilityPrivate
	}

	// Default an unset trigger source to manual, matching the column default and
	// the engine's OriginManual default. ScheduleID stays nil (NULL) for manual.
	triggerSource := inst.TriggerSource
	if triggerSource == "" {
		triggerSource = instance.OriginManual
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
		Department:        deptToNullString(inst.Department),
		Visibility:        string(visibility),
		TriggerSource:     string(triggerSource),
		ScheduleID:        scheduleIDToNullString(inst.ScheduleID),
	})
	if err != nil {
		return fmt.Errorf("creating instance: %w", err)
	}

	createdAt, err := parseTime(row.CreatedAt)
	if err != nil {
		return fmt.Errorf("parsing created_at: %w", err)
	}
	updatedAt, err := parseTime(row.UpdatedAt)
	if err != nil {
		return fmt.Errorf("parsing updated_at: %w", err)
	}
	inst.CreatedAt = createdAt
	inst.UpdatedAt = updatedAt
	return nil
}

func (r *InstanceRepo) Get(ctx context.Context, id uuid.UUID) (*instance.Instance, error) {
	row, err := r.queries.GetInstance(ctx, uuidToString(id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
		}
		return nil, fmt.Errorf("getting instance: %w", err)
	}
	return rowToInstance(row)
}

func (r *InstanceRepo) List(ctx context.Context, filters instance.ListFilters) ([]instance.InstanceSummary, error) {
	if filters.Limit <= 0 {
		filters.Limit = 50
	}

	// Admin lists arrive with no owner/department/public narrowing; mark the
	// query unconstrained so the disjunctive WHERE admits every row.
	unconstrained := int64(0)
	if filters.OwnerID == nil && len(filters.Departments) == 0 && !filters.IncludePublic {
		unconstrained = 1
	}
	deptJSON := "[]"
	if len(filters.Departments) > 0 {
		b, err := json.Marshal(filters.Departments)
		if err != nil {
			return nil, fmt.Errorf("marshalling departments filter: %w", err)
		}
		deptJSON = string(b)
	}
	includePublic := int64(0)
	if filters.IncludePublic {
		includePublic = 1
	}

	params := generated.ListInstancesParams{
		Limit:         int64(filters.Limit),
		Offset:        int64(filters.Offset),
		Unconstrained: unconstrained,
		IncludePublic: includePublic,
		Departments:   deptJSON,
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
	if filters.OwnerID != nil {
		params.OwnerID = uuidToString(*filters.OwnerID)
	}

	rows, err := r.queries.ListInstances(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("listing instances: %w", err)
	}

	summaries := make([]instance.InstanceSummary, len(rows))
	for i, row := range rows {
		id, err := parseUUID(row.ID)
		if err != nil {
			return nil, fmt.Errorf("listing instances: %w", err)
		}
		createdAt, err := parseTime(row.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("listing instances: %w", err)
		}
		updatedAt, err := parseTime(row.UpdatedAt)
		if err != nil {
			return nil, fmt.Errorf("listing instances: %w", err)
		}
		completedAt, err := nullStringToTimePtr(row.CompletedAt)
		if err != nil {
			return nil, fmt.Errorf("listing instances: %w", err)
		}
		queuedAt, err := nullStringToTimePtr(row.QueuedAt)
		if err != nil {
			return nil, fmt.Errorf("listing instances: %w", err)
		}
		scheduleID, err := nullStringToScheduleID(row.ScheduleID)
		if err != nil {
			return nil, fmt.Errorf("listing instances: %w", err)
		}
		summaries[i] = instance.InstanceSummary{
			ID:            id,
			WorkflowName:  row.WorkflowName,
			State:         instance.State(row.State),
			Priority:      instance.Priority(row.Priority),
			Department:    nullStringToString(row.Department),
			Visibility:    instance.Visibility(row.Visibility),
			CurrentNodeID: nullStringToPtr(row.CurrentNodeID),
			CreatedAt:     createdAt,
			UpdatedAt:     updatedAt,
			CompletedAt:   completedAt,
			QueuedAt:      queuedAt,
			TriggerSource: instance.TriggerSource(row.TriggerSource),
			ScheduleID:    scheduleID,
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
		inst, err := rowToInstance(row)
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
	now := timeToString(time.Now().UTC())
	idStr := uuidToString(id)
	if waitingUntil != nil {
		err := r.queries.SetWaitingWithTimer(ctx, generated.SetWaitingWithTimerParams{
			WaitingUntil: timeToNullString(waitingUntil),
			UpdatedAt:    now,
			ID:           idStr,
		})
		if err != nil {
			return fmt.Errorf("setting instance waiting: %w", err)
		}
	} else {
		// When clearing waiting_until, we intentionally do NOT change the state.
		// The caller is responsible for state transitions (e.g. back to 'running').
		// This matches the Postgres adapter behavior.
		err := r.queries.ClearWaitingUntil(ctx, generated.ClearWaitingUntilParams{
			UpdatedAt: now,
			ID:        idStr,
		})
		if err != nil {
			return fmt.Errorf("clearing waiting_until: %w", err)
		}
	}
	return nil
}

func (r *InstanceRepo) SetWebhookFields(ctx context.Context, id uuid.UUID, token *string, secretHash *string) error {
	now := timeToString(time.Now().UTC())
	err := r.queries.SetWebhookFields(ctx, generated.SetWebhookFieldsParams{
		WebhookToken:      ptrToNullString(token),
		WebhookSecretHash: ptrToNullString(secretHash),
		UpdatedAt:         now,
		ID:                uuidToString(id),
	})
	if err != nil {
		return fmt.Errorf("setting webhook fields: %w", err)
	}
	return nil
}

func (r *InstanceRepo) SetWebhookReceived(ctx context.Context, id uuid.UUID, receivedAt time.Time) error {
	now := timeToString(time.Now().UTC())
	err := r.queries.SetWebhookReceived(ctx, generated.SetWebhookReceivedParams{
		WebhookReceivedAt: stringToNullString(timeToString(receivedAt)),
		UpdatedAt:         now,
		ID:                uuidToString(id),
	})
	if err != nil {
		return fmt.Errorf("setting webhook received: %w", err)
	}
	return nil
}

// Stats aggregates execution metrics for a single workflow over [from, to].
//
// AUTHORIZATION: this method performs NO caller/owner/visibility scoping — it is
// a dumb data accessor. The access decision (CanSeePublication / ownership) lives
// in workflow.WorkflowService.Stats, which gates the name before delegating here.
// Do NOT add a per-caller predicate to this query; keep the boundary in the
// domain service (hexagonal architecture).
//
// RAW (not sqlc): this query is hand-written rather than generated because every
// aggregate must be COALESCE-wrapped to survive the empty-result-set case. Over
// zero matching rows SQLite's SUM/AVG return NULL (not 0), which would Scan into
// the int/float fields as a NULL error; COALESCE(..., 0) collapses that NULL to a
// clean zero. COUNT(*) is the lone exception (it is 0, never NULL, on an empty
// set). Regenerating this via sqlc would relocate — not remove — the NULL footgun
// into the generated layer, so it is intentionally kept raw here.
//
// TIMESTAMP COMPARISON: created_at is stored as a fixed-width UTC RFC3339-style
// string (see timeToString); because the width and layout are fixed and UTC,
// lexical (string) >=/<= comparison is equivalent to chronological comparison, so
// the range predicate is correct without a date() conversion. An inverted range
// is rejected upstream in the service (ErrInvalidStatsRange), so it never reaches
// this query.
func (r *InstanceRepo) Stats(ctx context.Context, workflowName string, from time.Time, to time.Time) (*instance.WorkflowStats, error) {
	query := `
SELECT
  COUNT(*) AS total_runs,
  COALESCE(SUM(CASE WHEN state = 'completed' THEN 1 ELSE 0 END), 0) AS success_count,
  COALESCE(SUM(CASE WHEN state = 'failed' THEN 1 ELSE 0 END), 0) AS failure_count,
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

// HasActiveRun reports whether ownerID has a live instance of workflowName.
// "Active" is any non-terminal state: queued, running, waiting, or paused. It is
// the schedule evaluator's collision primitive (org-readiness Phase 3): a due
// schedule is skipped when its creator already has an active run of the same
// workflow. It is intentionally a method on the concrete repo (not the
// instance.InstanceRepository port) so the evaluator's narrow ActiveRunChecker
// port is satisfied at the composition root without widening the shared port.
func (r *InstanceRepo) HasActiveRun(ctx context.Context, workflowName string, ownerID uuid.UUID) (bool, error) {
	const q = `SELECT EXISTS(
  SELECT 1 FROM instances
  WHERE workflow_name = ? AND owner_id = ?
    AND state IN ('queued','running','waiting','paused')
)`
	var exists int
	if err := r.db.QueryRowContext(ctx, q, workflowName, uuidToString(ownerID)).Scan(&exists); err != nil {
		return false, fmt.Errorf("checking active run for workflow %q owner %q: %w", workflowName, ownerID, err)
	}
	return exists != 0, nil
}

// rowToInstance converts a sqlc-generated Instance row into a domain Instance.
// This is a package-level function so both InstanceRepo and QueueRepo can use
// it without hidden coupling between the two repos.
func rowToInstance(row generated.Instance) (*instance.Instance, error) {
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

	id, err := parseUUID(row.ID)
	if err != nil {
		return nil, fmt.Errorf("converting instance row: %w", err)
	}
	ownerID, err := parseUUID(row.OwnerID)
	if err != nil {
		return nil, fmt.Errorf("converting instance row: %w", err)
	}
	createdAt, err := parseTime(row.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("converting instance row: %w", err)
	}
	updatedAt, err := parseTime(row.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("converting instance row: %w", err)
	}
	completedAt, err := nullStringToTimePtr(row.CompletedAt)
	if err != nil {
		return nil, fmt.Errorf("converting instance row: %w", err)
	}
	queuedAt, err := nullStringToTimePtr(row.QueuedAt)
	if err != nil {
		return nil, fmt.Errorf("converting instance row: %w", err)
	}
	waitingUntil, err := nullStringToTimePtr(row.WaitingUntil)
	if err != nil {
		return nil, fmt.Errorf("converting instance row: %w", err)
	}
	webhookReceivedAt, err := nullStringToTimePtr(row.WebhookReceivedAt)
	if err != nil {
		return nil, fmt.Errorf("converting instance row: %w", err)
	}
	scheduleID, err := nullStringToScheduleID(row.ScheduleID)
	if err != nil {
		return nil, fmt.Errorf("converting instance row: %w", err)
	}

	return &instance.Instance{
		ID:                id,
		OwnerID:           ownerID,
		WorkflowName:      row.WorkflowName,
		WorkflowSnapshot:  snapshot,
		Department:        nullStringToString(row.Department),
		Visibility:        instance.Visibility(row.Visibility),
		State:             instance.State(row.State),
		Priority:          instance.Priority(row.Priority),
		Variables:         vars,
		OriginalVariables: origVars,
		CurrentNodeID:     nullStringToPtr(row.CurrentNodeID),
		FailedNodeID:      nullStringToPtr(row.FailedNodeID),
		CreatedAt:         createdAt,
		UpdatedAt:         updatedAt,
		CompletedAt:       completedAt,
		QueuedAt:          queuedAt,
		WaitingUntil:      waitingUntil,
		WebhookToken:      nullStringToPtr(row.WebhookToken),
		WebhookSecretHash: nullStringToPtr(row.WebhookSecretHash),
		WebhookReceivedAt: webhookReceivedAt,
		TriggerSource:     instance.TriggerSource(row.TriggerSource),
		ScheduleID:        scheduleID,
	}, nil
}
