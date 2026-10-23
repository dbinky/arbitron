package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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

// createInstanceSQL writes the row and the spawn link in ONE statement.
//
// The link MUST ride this INSERT rather than a follow-up UPDATE: the trigger
// path creates AND enqueues (Engine.TriggerWithDepartment), so control returns
// to the caller only after the child may already be running. A child that
// reached a terminal state before a follow-up link write landed would leave its
// waiting parent with nothing to match, and the parent would hang forever.
//
// This is hand-written rather than sqlc-generated for the same reason
// CountActiveByWorkflowID is: it lets the column set evolve without a
// regeneration step. queries/instances.sql carries the matching source-of-truth
// SQL.
//
// The group-task columns ride the SAME insert: the group launcher enqueues
// immediately after creating the child, so even a fast terminal must already
// be attributable to its roster row.
const createInstanceSQL = `INSERT INTO instances (
	id, owner_id, workflow_name, workflow_snapshot, state, priority,
	variables, original_variables, queued_at, department, visibility,
	trigger_source, schedule_id, workflow_id,
	parent_instance_id, parent_node_id, spawn_mode, spawn_depth,
	model_pins, model_pins_version, model_pin_routes,
	group_run_id, group_task_key, group_task_kind, current_node_id
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING created_at, updated_at`

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

	// A PARTIAL link is never written: a row with a parent id but no node id
	// could not be matched by the wake and would strand its parent forever. All
	// three columns are written together or all three stay NULL.
	//
	// Group tasks store spawn_mode NULL (the 018 CHECK cannot admit
	// 'group_task') and the complete group tuple. An incomplete tuple writes
	// NO link, like an incomplete legacy link. Legacy modes ignore group fields.
	var parentID, parentNode, spawnMode sql.NullString
	var groupRunID, groupTaskKey, groupTaskKind sql.NullString
	if inst.ParentInstanceID != nil && inst.ParentNodeID != nil {
		switch {
		case inst.SpawnMode == instance.SpawnModeGroupTask:
			if groupLinkComplete(inst) {
				parentID = parentIDToNullString(inst.ParentInstanceID)
				parentNode = ptrToNullString(inst.ParentNodeID)
				groupRunID = sql.NullString{String: inst.GroupRunID.String(), Valid: true}
				groupTaskKey = ptrToNullString(inst.GroupTaskKey)
				groupTaskKind = sql.NullString{String: string(inst.GroupTaskKind), Valid: true}
			}
		case inst.SpawnMode.Valid():
			parentID = parentIDToNullString(inst.ParentInstanceID)
			parentNode = ptrToNullString(inst.ParentNodeID)
			spawnMode = sql.NullString{String: string(inst.SpawnMode), Valid: true}
		}
	}

	// The pin pair rides the SAME insert as the row so a queued run can never
	// exist without the models it was triggered on. A pair the read side would
	// call corruption is refused before any write.
	modelPins, modelPinsVersion, err := encodeModelPins(inst)
	if err != nil {
		return fmt.Errorf("creating instance: %w", err)
	}

	modelPinRoutes, err := encodeModelPinRoutes(inst)
	if err != nil {
		return fmt.Errorf("creating instance: %w", err)
	}

	var createdAtRaw, updatedAtRaw string
	err = r.db.QueryRowContext(ctx, createInstanceSQL,
		uuidToString(inst.ID),
		uuidToString(inst.OwnerID),
		inst.WorkflowName,
		snapshotJSON,
		string(inst.State),
		string(inst.Priority),
		varsJSON,
		origVarsJSON,
		timeToNullString(inst.QueuedAt),
		deptToNullString(inst.Department),
		string(visibility),
		string(triggerSource),
		scheduleIDToNullString(inst.ScheduleID),
		// Stamp the stable workflow_id (migration 017) in the SAME INSERT: there
		// is no crash window in which the row exists with workflow_id=NULL, which
		// would make a live run invisible to the delete-while-running guard
		// (CountActiveByWorkflowID keys on workflow_id). A Nil id (legacy/local-mode
		// run) maps to NULL — the backfill's "dangling label" state.
		workflowIDToNullString(inst.WorkflowID),
		parentID,
		parentNode,
		spawnMode,
		// Rides the SAME insert as the link: the depth counter is the runtime
		// bound on spawn recursion, so there must be no window in which a child
		// row exists with a depth of 0 that a grandchild could read.
		inst.SpawnDepth,
		modelPins,
		modelPinsVersion,
		modelPinRoutes,
		groupRunID,
		groupTaskKey,
		groupTaskKind,
		// Branch tasks must start at their own entry, not the workflow entrypoint.
		ptrToNullString(inst.CurrentNodeID),
	).Scan(&createdAtRaw, &updatedAtRaw)
	if err != nil {
		return fmt.Errorf("creating instance: %w", err)
	}

	createdAt, err := parseTime(createdAtRaw)
	if err != nil {
		return fmt.Errorf("parsing created_at: %w", err)
	}
	updatedAt, err := parseTime(updatedAtRaw)
	if err != nil {
		return fmt.Errorf("parsing updated_at: %w", err)
	}
	inst.CreatedAt = createdAt
	inst.UpdatedAt = updatedAt
	return nil
}

// groupLinkComplete reports whether the group-task half of a link is complete.
// Parent pointers are checked by Create before calling this helper.
func groupLinkComplete(inst *instance.Instance) bool {
	return inst.ParentNodeID != nil && *inst.ParentNodeID != "" &&
		inst.GroupRunID != nil && *inst.GroupRunID != uuid.Nil &&
		inst.GroupTaskKey != nil && *inst.GroupTaskKey != "" &&
		inst.GroupTaskKind.Valid()
}

func (r *InstanceRepo) Get(ctx context.Context, id uuid.UUID) (*instance.Instance, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+instanceColumnsWithLink+` FROM instances WHERE id = ?`, uuidToString(id))
	inst, err := scanInstanceWithLink(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
		}
		return nil, fmt.Errorf("getting instance: %w", err)
	}
	return inst, nil
}

// List returns a page of instance summaries.
//
// The statement is assembled here rather than generated so the parent
// narrowing can be appended without a sqlc regeneration step
// (queries/instances.sql carries the matching source-of-truth SQL).
//
// SECURITY — the disjunctive RBAC visibility filter comes from the ONE shared
// visibilityPredicate, the same one the child-count query uses: a caller sees a
// row when it is owned by them, OR public (when IncludePublic), OR a department
// run in one of their departments. It FAILS CLOSED — admitting every row
// requires filters.Unconstrained, the explicit admin assertion service-set by
// ListInstances/ListEnriched; "no narrowing supplied at all" now yields nothing
// rather than the full table. The parent predicate is appended as a further
// AND, so it can only ever NARROW the authorized set — never widen it.
func (r *InstanceRepo) List(ctx context.Context, filters instance.ListFilters) ([]instance.InstanceSummary, error) {
	if filters.Limit <= 0 {
		filters.Limit = 50
	}

	var where []string
	var args []any

	if filters.State != nil {
		where = append(where, "state = ?")
		args = append(args, string(*filters.State))
	}
	if filters.WorkflowName != nil {
		where = append(where, "workflow_name = ?")
		args = append(args, *filters.WorkflowName)
	}
	if filters.Since != nil {
		where = append(where, "created_at >= ?")
		args = append(args, timeToString(*filters.Since))
	}

	// THE SAME predicate the child-count query applies, over the same
	// service-set scope. Admission of every row requires filters.Unconstrained —
	// the explicit admin assertion — never the mere absence of a narrowing.
	vis := instance.ChildVisibility{
		Unconstrained: filters.Unconstrained,
		OwnerID:       filters.OwnerID,
		Departments:   filters.Departments,
		IncludePublic: filters.IncludePublic,
	}
	visClause, visArgs, err := visibilityPredicate(vis, "")
	if err != nil {
		return nil, err
	}
	where = append(where, visClause)
	args = append(args, visArgs...)

	switch filters.ParentScope {
	case instance.ParentScopeRoots:
		where = append(where, "parent_instance_id IS NULL")
	case instance.ParentScopeChildren:
		if filters.ParentInstanceID == nil {
			// A children scope with no parent id is a caller bug. Return an empty
			// page rather than silently degrading to an unfiltered list.
			return []instance.InstanceSummary{}, nil
		}
		where = append(where, "parent_instance_id = ?")
		args = append(args, uuidToString(*filters.ParentInstanceID))
	}

	// Both the ancestor row and the active descendant must be caller-visible.
	var with string
	var withArgs []any
	if filters.ActiveDescendantAncestors {
		descClause, descArgs, err := visibilityPredicate(vis, "d")
		if err != nil {
			return nil, err
		}
		with = activeDescendantAncestorsCTE(descClause)
		withArgs = descArgs
		where = append(where,
			"state IN ('completed','failed','cancelled')",
			"id IN (SELECT id FROM active_ancestors)")
	}

	q := with + "SELECT " + instanceSummaryColumns + " FROM instances"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY created_at DESC"
	if filters.ActiveDescendantAncestors {
		// Many ancestors share a one-second creation timestamp. Discovery pages
		// need a total order so ties cannot move rows across page boundaries.
		q += ", id DESC"
	}
	q += " LIMIT ? OFFSET ?"
	// CTE placeholders precede the outer WHERE placeholders.
	args = append(append(withArgs, args...), int64(filters.Limit), int64(filters.Offset))

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("listing instances: %w", err)
	}
	defer rows.Close()

	summaries := []instance.InstanceSummary{}
	for rows.Next() {
		var (
			idRaw, ownerRaw, workflowName, state, priority string
			currentNodeID, failedNodeID                    sql.NullString
			variablesRaw, snapshotRaw                      string
			createdAtRaw, updatedAtRaw                     string
			completedAtRaw, queuedAtRaw                    sql.NullString
			department                                     sql.NullString
			visibility, triggerSource                      string
			scheduleIDRaw, parentIDRaw                     sql.NullString
			pinsRaw, pinRoutesRaw                          sql.NullString
			pinsVersionRaw                                 any
		)
		if err := rows.Scan(
			&idRaw, &ownerRaw, &workflowName, &state, &priority, &currentNodeID,
			&failedNodeID, &variablesRaw, &snapshotRaw, &createdAtRaw, &updatedAtRaw,
			&completedAtRaw, &queuedAtRaw, &department, &visibility, &triggerSource,
			&scheduleIDRaw, &parentIDRaw, &pinsRaw, &pinsVersionRaw, &pinRoutesRaw,
		); err != nil {
			return nil, fmt.Errorf("listing instances: %w", err)
		}

		id, err := parseUUID(idRaw)
		if err != nil {
			return nil, fmt.Errorf("listing instances: %w", err)
		}
		ownerID, err := parseUUID(ownerRaw)
		if err != nil {
			return nil, fmt.Errorf("listing instances: %w", err)
		}
		createdAt, err := parseTime(createdAtRaw)
		if err != nil {
			return nil, fmt.Errorf("listing instances: %w", err)
		}
		updatedAt, err := parseTime(updatedAtRaw)
		if err != nil {
			return nil, fmt.Errorf("listing instances: %w", err)
		}
		completedAt, err := nullStringToTimePtr(completedAtRaw)
		if err != nil {
			return nil, fmt.Errorf("listing instances: %w", err)
		}
		queuedAt, err := nullStringToTimePtr(queuedAtRaw)
		if err != nil {
			return nil, fmt.Errorf("listing instances: %w", err)
		}
		scheduleID, err := nullStringToScheduleID(scheduleIDRaw)
		if err != nil {
			return nil, fmt.Errorf("listing instances: %w", err)
		}
		parentID, err := nullStringToParentID(parentIDRaw)
		if err != nil {
			return nil, fmt.Errorf("listing instances: %w", err)
		}
		variables, err := fromJSONString(variablesRaw)
		if err != nil {
			return nil, fmt.Errorf("listing instances: unmarshalling variables: %w", err)
		}
		snapshot, err := fromJSONString(snapshotRaw)
		if err != nil {
			return nil, fmt.Errorf("listing instances: unmarshalling workflow snapshot: %w", err)
		}
		// A corrupt pin record cannot execute (Get refuses it), but it must not
		// blank the whole page either: the row lists with its pins marked
		// unreadable, so the enricher never shows the live model for it.
		pins, pinsVersion, err := decodeStoredModelPins(pinsRaw, pinsVersionRaw)
		var pinRoutes, pinRouteConns map[string]string
		if err == nil {
			pinRoutes, pinRouteConns, err = decodeModelPinRoutes(pinRoutesRaw, pins, pinsVersion)
		}
		if err != nil {
			slog.Warn("listing instances: unreadable model pins", "instance", idRaw, "error", err)
			pins, pinsVersion, pinRoutes, pinRouteConns = nil, instance.ModelPinsVersionUnreadable, nil, nil
		}

		summaries = append(summaries, instance.InstanceSummary{
			ID:                       id,
			OwnerID:                  ownerID,
			WorkflowName:             workflowName,
			State:                    instance.State(state),
			Priority:                 instance.Priority(priority),
			Department:               nullStringToString(department),
			Visibility:               instance.Visibility(visibility),
			CurrentNodeID:            nullStringToPtr(currentNodeID),
			FailedNodeID:             nullStringToPtr(failedNodeID),
			Variables:                variables,
			WorkflowSnapshot:         snapshot,
			CreatedAt:                createdAt,
			UpdatedAt:                updatedAt,
			CompletedAt:              completedAt,
			QueuedAt:                 queuedAt,
			TriggerSource:            instance.TriggerSource(triggerSource),
			ScheduleID:               scheduleID,
			ParentInstanceID:         parentID,
			ModelPins:                pins,
			ModelPinsVersion:         pinsVersion,
			ModelPinRoutes:           pinRoutes,
			ModelPinRouteConnections: pinRouteConns,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing instances: %w", err)
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

// listRunningWithLinkSQL keeps recovery reads on the full spawn/group link
// projection, including depth. queries/instances.sql documents the predicate.
const listRunningWithLinkSQL = `SELECT ` + instanceColumnsWithLink + ` FROM instances WHERE state = 'running'`

// ListRunning returns the healthy rows plus an *instance.CorruptPinsError when
// any row's pin record is corrupt; crash recovery fails those through the engine.
func (r *InstanceRepo) ListRunning(ctx context.Context) ([]instance.Instance, error) {
	return executionList(ctx, r.db, listRunningWithLinkSQL, "listing running instances")
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
// in workflow.Service.Stats, which gates the name before delegating here.
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

// CountActiveByWorkflowID returns how many non-terminal instances carry the
// stable workflowID. "Active" is any state other than completed/failed/cancelled
// (i.e. queued/running/waiting/paused), mirroring HasActiveRun's vocabulary. It is
// the admin Delete guard's primitive (workflow-management Phase 3): a workflow
// with live runs cannot be deleted. It keys on workflow_id (migration 017), not
// the human-facing workflow_name, so a rename does not hide live runs.
//
// It is a hand-written query (the generated GetInstance/List predate the
// workflow_id column) to avoid a blanket sqlc regeneration of the hand-curated
// generated/ tree, matching the Create/Get workflow_id handling above.
func (r *InstanceRepo) CountActiveByWorkflowID(ctx context.Context, workflowID uuid.UUID) (int, error) {
	const q = `SELECT COUNT(*) FROM instances
  WHERE workflow_id = ?
    AND state IN ('queued','running','waiting','paused')`
	var n int
	if err := r.db.QueryRowContext(ctx, q, uuidToString(workflowID)).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting active instances for workflow %q: %w", workflowID, err)
	}
	return n, nil
}

// visibilityPredicate renders a caller's service-set visibility scope as a SQL
// predicate, plus its bound arguments. alias is the table alias to qualify the
// columns with ("c" for the child-count join, "" for a bare single-table
// query).
//
// THIS IS THE ONE VISIBILITY PREDICATE. It is shared by List and by
// ChildCountsByParents because those two previously read the SAME input in
// OPPOSITE directions: List treated "no scope at all" as an admin list and
// returned the whole table, while the child-count clause treated it as "no
// authority" and returned nothing. Only a caller invariant — written down
// nowhere — kept the first from being a data leak.
//
// It FAILS CLOSED. Admission of every row requires the EXPLICIT Unconstrained
// assertion, which only listConstraint sets, and only for an admin. Absence of
// a scope is absence of authority and yields "0=1".
func visibilityPredicate(vis instance.ChildVisibility, alias string) (string, []any, error) {
	if vis.Unconstrained {
		return "1=1", nil, nil
	}
	col := func(name string) string {
		if alias == "" {
			return name
		}
		return alias + "." + name
	}
	var ors []string
	var args []any
	if vis.OwnerID != nil {
		ors = append(ors, col("owner_id")+" = ?")
		args = append(args, uuidToString(*vis.OwnerID))
	}
	if vis.IncludePublic {
		ors = append(ors, col("visibility")+" = 'public'")
	}
	if len(vis.Departments) > 0 {
		b, err := json.Marshal(vis.Departments)
		if err != nil {
			return "", nil, fmt.Errorf("marshalling visibility departments: %w", err)
		}
		// json_each turns the JSON array into a membership test. Portable to
		// Postgres as `department = ANY($departments)`.
		ors = append(ors, "("+col("visibility")+" = 'department' AND "+col("department")+" IN (SELECT value FROM json_each(?)))")
		args = append(args, string(b))
	}
	if len(ors) == 0 {
		// No owner, no departments, no public, and no admin assertion: this
		// caller has no authority to list anything. Admitting every row here was
		// the divergence this function exists to close.
		return "0=1", nil, nil
	}
	return "(" + strings.Join(ors, " OR ") + ")", args, nil
}

// activeDescendantAncestorsCTE finds every ancestor of active work admitted by
// descVis (visibilityPredicate over alias "d"). UNION de-duplicates IDs and
// terminates recursion even on a corrupt parent cycle.
func activeDescendantAncestorsCTE(descVis string) string {
	return `WITH RECURSIVE active_ancestors(id) AS (
  SELECT d.parent_instance_id FROM instances d
   WHERE d.parent_instance_id IS NOT NULL
     AND d.state IN ('queued','running','waiting','paused')
     AND ` + descVis + `
  UNION
  SELECT p.parent_instance_id FROM instances p
    JOIN active_ancestors a ON p.id = a.id
   WHERE p.parent_instance_id IS NOT NULL
) `
}

// ChildCountsByParents implements instance.SpawnLinkRepository.
//
// Contract (mirrors AggregateByInstances): parents with no VISIBLE children are
// absent from the map — absence means zero, not an error — and an empty
// parentIDs slice returns an empty map without touching storage. It exists so a
// page of the run list resolves every expander in one round trip rather than
// issuing N+1 queries.
func (r *InstanceRepo) ChildCountsByParents(ctx context.Context, parentIDs []uuid.UUID, vis instance.ChildVisibility) (map[uuid.UUID]int, error) {
	out := map[uuid.UUID]int{}
	if len(parentIDs) == 0 {
		return out, nil
	}

	visClause, visArgs, err := visibilityPredicate(vis, "c")
	if err != nil {
		return nil, err
	}

	placeholders := make([]string, len(parentIDs))
	args := make([]any, 0, len(parentIDs)+len(visArgs))
	for i, id := range parentIDs {
		placeholders[i] = "?"
		args = append(args, uuidToString(id))
	}
	args = append(args, visArgs...)

	q := `SELECT c.parent_instance_id, COUNT(*) FROM instances c
  WHERE c.parent_instance_id IN (` + strings.Join(placeholders, ",") + `)
    AND ` + visClause + `
  GROUP BY c.parent_instance_id`

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("counting children by parent: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var parentRaw string
		var n int
		if err := rows.Scan(&parentRaw, &n); err != nil {
			return nil, fmt.Errorf("counting children by parent: %w", err)
		}
		id, err := parseUUID(parentRaw)
		if err != nil {
			return nil, fmt.Errorf("counting children by parent: %w", err)
		}
		out[id] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("counting children by parent: %w", err)
	}
	return out, nil
}

// ListActiveChildren implements instance.SpawnLinkRepository.
//
// Spawn mode is deliberately NOT a filter here: the cancel cascade covers every
// active child, both awaited and fire-and-forget. A fire-and-forget child left
// running after its parent is cancelled burns queue capacity and a workspace on
// work whose requester is gone.
//
// It returns ONE level. Recursion belongs in the domain cascade, which carries
// a visited set.
func (r *InstanceRepo) ListActiveChildren(ctx context.Context, parentID uuid.UUID) ([]uuid.UUID, error) {
	const q = `SELECT id FROM instances
  WHERE parent_instance_id = ?
    AND state IN ('queued','running','waiting','paused')
  ORDER BY created_at ASC`

	rows, err := r.db.QueryContext(ctx, q, uuidToString(parentID))
	if err != nil {
		return nil, fmt.Errorf("listing active children of %s: %w", parentID, err)
	}
	defer rows.Close()

	out := []uuid.UUID{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("listing active children of %s: %w", parentID, err)
		}
		id, err := parseUUID(raw)
		if err != nil {
			return nil, fmt.Errorf("listing active children of %s: %w", parentID, err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing active children of %s: %w", parentID, err)
	}
	return out, nil
}

// ListChildren returns every child, including terminal intermediates that may
// still own active descendants. rowid breaks created_at's one-second ties.
func (r *InstanceRepo) ListChildren(ctx context.Context, parentID uuid.UUID) ([]instance.ChildRef, error) {
	const q = `SELECT id, state FROM instances
  WHERE parent_instance_id = ?
  ORDER BY created_at ASC, rowid ASC`
	rows, err := r.db.QueryContext(ctx, q, uuidToString(parentID))
	if err != nil {
		return nil, fmt.Errorf("listing children of %s: %w", parentID, err)
	}
	defer rows.Close()
	out := []instance.ChildRef{}
	for rows.Next() {
		var raw, state string
		if err := rows.Scan(&raw, &state); err != nil {
			return nil, fmt.Errorf("listing children of %s: %w", parentID, err)
		}
		id, err := parseUUID(raw)
		if err != nil {
			return nil, fmt.Errorf("listing children of %s: %w", parentID, err)
		}
		out = append(out, instance.ChildRef{ID: id, State: instance.State(state)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing children of %s: %w", parentID, err)
	}
	return out, nil
}

// MarkSubtreeCancelRequested persists cancellation intent without rewriting a
// terminal run's outcome. A repeated request covers descendants created since
// the previous request; active or unknown rows are left untouched. Migration
// 021's trigger captures the creation sequence in this same atomic write, even
// when the timestamp has not changed since the previous request.
func (r *InstanceRepo) MarkSubtreeCancelRequested(ctx context.Context, id uuid.UUID) error {
	const q = `UPDATE instances
   SET subtree_cancel_requested_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
 WHERE id = ? AND state IN ('completed','failed','cancelled')`
	if _, err := r.db.ExecContext(ctx, q, uuidToString(id)); err != nil {
		return fmt.Errorf("recording subtree cancellation request for %s: %w", id, err)
	}
	return nil
}

// orphanAncestorWalkLimit exceeds MaxSpawnDepth and bounds corrupt cycles.
const orphanAncestorWalkLimit = 64

const listOrphanedActiveChildrenSQL = `WITH RECURSIVE up(orphan_id, ancestor_id, hops) AS (
  SELECT c.id, c.parent_instance_id, 1
    FROM instances c
   WHERE c.state IN ('queued','running','waiting','paused')
     AND c.parent_instance_id IS NOT NULL
  UNION ALL
  SELECT up.orphan_id, a.parent_instance_id, up.hops + 1
    FROM up
    JOIN instances a ON a.id = up.ancestor_id
   WHERE a.state IN ('completed','failed')
     AND a.parent_instance_id IS NOT NULL
     AND up.hops < ?
)
SELECT c.id
  FROM instances c
 WHERE c.id IN (
   SELECT up.orphan_id
     FROM up
     JOIN instances a ON a.id = up.ancestor_id
     JOIN instance_creation_order o ON o.instance_id = up.orphan_id
     JOIN instance_creation_order boundary ON boundary.instance_id = a.id
    WHERE a.state = 'cancelled'
       OR (a.subtree_cancel_requested_at IS NOT NULL
           AND o.sequence <= boundary.cancel_through_sequence))
 ORDER BY c.created_at ASC, c.rowid ASC`

// ListOrphanedActiveChildren finds active descendants of cancelled ancestors or
// terminal ancestors with cancellation intent covering the descendant's durable
// creation sequence (not its wall-clock timestamp). The climb
// crosses completed/failed intermediates but stops at active ones: cancelling
// that topmost orphan cascades through its subtree. Unstamped completed roots
// retain legitimate independent work. The bounded climb tolerates corrupt links.
func (r *InstanceRepo) ListOrphanedActiveChildren(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := r.db.QueryContext(ctx, listOrphanedActiveChildrenSQL, orphanAncestorWalkLimit)
	if err != nil {
		return nil, fmt.Errorf("listing orphaned active children: %w", err)
	}
	defer rows.Close()

	out := []uuid.UUID{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("listing orphaned active children: %w", err)
		}
		id, err := parseUUID(raw)
		if err != nil {
			return nil, fmt.Errorf("listing orphaned active children: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing orphaned active children: %w", err)
	}
	return out, nil
}

// FindChildBySpawnNode implements instance.SpawnWaitRepository.
//
// All THREE key parts are load-bearing. Dropping the node id would let a child
// of a different spawn node in the same parent answer here; dropping the mode
// would let a fire-and-forget child answer an awaited lookup. Either mistake
// routes a parent by an event that had nothing to do with it.
//
// (nil, nil) means "no child yet" — the FIRST-EXECUTION signal — and is
// deliberately distinguishable from an error, because treating a storage
// failure as "no child" would spawn a second child.
//
// ORDER BY created_at DESC LIMIT 1 picks the most recent child. A node-level
// retry after a partially failed suspension can leave more than one row; the
// newest is the one this pass is waiting on.
func (r *InstanceRepo) FindChildBySpawnNode(ctx context.Context, parentID uuid.UUID, nodeID string, mode instance.SpawnMode) (*instance.ChildLink, error) {
	const q = `SELECT id, state FROM instances
  WHERE parent_instance_id = ? AND parent_node_id = ? AND spawn_mode = ?
  ORDER BY created_at DESC, rowid DESC
  LIMIT 1`

	var idRaw, state string
	err := r.db.QueryRowContext(ctx, q, uuidToString(parentID), nodeID, string(mode)).Scan(&idRaw, &state)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("finding child of %s node %q: %w", parentID, nodeID, err)
	}
	childID, err := parseUUID(idRaw)
	if err != nil {
		return nil, fmt.Errorf("finding child of %s node %q: %w", parentID, nodeID, err)
	}
	return &instance.ChildLink{ChildID: childID, ChildState: instance.State(state)}, nil
}

// ClaimWaitingParent implements instance.SpawnWaitRepository.
//
// ONE conditional UPDATE is the whole mechanism: the `state = 'waiting' AND
// current_node_id = ?` predicate makes the transition atomic against the wake
// path's competing SetQueuedIfWaiting on the same row, so exactly one mover
// wins and a parent can never be queued and executing at the same time.
//
// The node predicate matters as much as the state one: a parent that has since
// advanced to a LATER spawn node is not the parent this claim is about, and
// taking it would resume the wrong node.
func (r *InstanceRepo) ClaimWaitingParent(ctx context.Context, parentID uuid.UUID, nodeID string) (bool, error) {
	const q = `UPDATE instances
  SET state = 'running', updated_at = strftime('%Y-%m-%dT%H:%M:%SZ')
  WHERE id = ? AND state = 'waiting' AND current_node_id = ?`
	res, err := r.db.ExecContext(ctx, q, uuidToString(parentID), nodeID)
	if err != nil {
		return false, fmt.Errorf("claiming waiting parent %s: %w", parentID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("claiming waiting parent %s: %w", parentID, err)
	}
	return n > 0, nil
}

// SuspendRunning implements instance.SpawnWaitRepository.
func (r *InstanceRepo) SuspendRunning(ctx context.Context, id uuid.UUID) (bool, error) {
	const q = `UPDATE instances
  SET state = 'waiting', updated_at = strftime('%Y-%m-%dT%H:%M:%SZ')
  WHERE id = ? AND state = 'running'`
	res, err := r.db.ExecContext(ctx, q, uuidToString(id))
	if err != nil {
		return false, fmt.Errorf("suspending running instance %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("suspending running instance %s: %w", id, err)
	}
	return n > 0, nil
}

// ListWaitingOnTerminalChild implements instance.SpawnWaitRepository.
//
// The WHERE clause is the reconciler's correctness core and mirrors the
// in-process wake's three-part match exactly:
//
//	the parent is WAITING
//	∧ the parent's CURRENT node id == the child's recorded parent node id
//	∧ the link's mode is wait_for_child
//
// plus "the child is already terminal". Any one of the three conjuncts dropped
// would resume a parent by an unrelated event.
func (r *InstanceRepo) ListWaitingOnTerminalChild(ctx context.Context) ([]instance.SpawnWaitCandidate, error) {
	const q = `SELECT p.id, p.current_node_id, c.id, c.state
  FROM instances p
  JOIN instances c ON c.parent_instance_id = p.id
  WHERE p.state = 'waiting'
    AND p.current_node_id IS NOT NULL
    AND c.parent_node_id = p.current_node_id
    AND c.spawn_mode = 'wait_for_child'
    AND c.state IN ('completed','failed','cancelled')`

	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("listing parents waiting on a terminal child: %w", err)
	}
	defer rows.Close()

	out := []instance.SpawnWaitCandidate{}
	for rows.Next() {
		var parentRaw, nodeID, childRaw, state string
		if err := rows.Scan(&parentRaw, &nodeID, &childRaw, &state); err != nil {
			return nil, fmt.Errorf("listing parents waiting on a terminal child: %w", err)
		}
		parentID, err := parseUUID(parentRaw)
		if err != nil {
			return nil, fmt.Errorf("listing parents waiting on a terminal child: %w", err)
		}
		childID, err := parseUUID(childRaw)
		if err != nil {
			return nil, fmt.Errorf("listing parents waiting on a terminal child: %w", err)
		}
		out = append(out, instance.SpawnWaitCandidate{
			ParentID:     parentID,
			ParentNodeID: nodeID,
			ChildID:      childID,
			ChildState:   instance.State(state),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing parents waiting on a terminal child: %w", err)
	}
	return out, nil
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
	// An empty input object is a valid trigger snapshot. Preserve it as a
	// non-nil map: Rerun uses nil to distinguish missing original inputs and
	// would otherwise refuse every workflow triggered without variables.
	var origVars map[string]any
	if err := json.Unmarshal([]byte(row.OriginalVariables), &origVars); err != nil {
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
	// Stable workflow_id (migration 017) folded into the GetInstance query. A NULL
	// (legacy run, or local mode with no registry) maps to uuid.Nil.
	workflowID, err := nullStringToWorkflowID(row.WorkflowID)
	if err != nil {
		return nil, fmt.Errorf("converting instance row: %w", err)
	}
	// Spawn link (018), read as an ALL-OR-NONE tuple. scanInstanceWithLink
	// overwrites it from its explicit projection, which also carries the 019
	// group-task link and the pin record, so every link-aware full-row read (Get,
	// ListRunning, NextEligible, ListWaitingWithTimer/Webhook) uses it;
	// rowToInstance is the shared base conversion, including the immutable pin
	// discriminator. ListQueued's consumer uses IDs/priorities only.
	parentInstanceID, parentNodeID, spawnMode, err := nullStringToSpawnLink(
		row.ParentInstanceID, row.ParentNodeID, row.SpawnMode)
	if err != nil {
		return nil, fmt.Errorf("converting instance row: %w", err)
	}

	pins, pinsVersion, err := decodeStoredModelPins(row.ModelPins, row.ModelPinsVersion)
	if err != nil {
		return nil, fmt.Errorf("converting instance row %s: %w", id, err)
	}
	routes, routeConns, err := decodeModelPinRoutes(row.ModelPinRoutes, pins, pinsVersion)
	if err != nil {
		return nil, fmt.Errorf("converting instance row %s: %w", id, err)
	}

	return &instance.Instance{
		ID:                       id,
		OwnerID:                  ownerID,
		WorkflowID:               workflowID,
		WorkflowName:             row.WorkflowName,
		WorkflowSnapshot:         snapshot,
		Department:               nullStringToString(row.Department),
		Visibility:               instance.Visibility(row.Visibility),
		State:                    instance.State(row.State),
		Priority:                 instance.Priority(row.Priority),
		Variables:                vars,
		OriginalVariables:        origVars,
		CurrentNodeID:            nullStringToPtr(row.CurrentNodeID),
		FailedNodeID:             nullStringToPtr(row.FailedNodeID),
		CreatedAt:                createdAt,
		UpdatedAt:                updatedAt,
		CompletedAt:              completedAt,
		QueuedAt:                 queuedAt,
		WaitingUntil:             waitingUntil,
		WebhookToken:             nullStringToPtr(row.WebhookToken),
		WebhookSecretHash:        nullStringToPtr(row.WebhookSecretHash),
		WebhookReceivedAt:        webhookReceivedAt,
		TriggerSource:            instance.TriggerSource(row.TriggerSource),
		ScheduleID:               scheduleID,
		ParentInstanceID:         parentInstanceID,
		ParentNodeID:             parentNodeID,
		SpawnMode:                spawnMode,
		SpawnDepth:               int(row.SpawnDepth),
		ModelPins:                pins,
		ModelPinsVersion:         pinsVersion,
		ModelPinRoutes:           routes,
		ModelPinRouteConnections: routeConns,
	}, nil
}

// ListExecutionSnapshots implements instance.ExecutionSnapshotSource.
//
// Failed legacy runs are included because Retry resolves them live. It is a
// pure inventory read: a run with corrupt pins cannot execute (Get refuses it,
// and the engine fails it through its normal terminal path), so it is skipped
// and logged, never transitioned here. Healthy legacy snapshots remain visible
// to route validation.
func (r *InstanceRepo) ListExecutionSnapshots(ctx context.Context) ([]instance.ExecutionSnapshot, error) {
	const q = `SELECT id, workflow_snapshot, model_pins, model_pins_version, model_pin_routes FROM instances
  WHERE state IN ('queued','running','waiting','paused','failed')
  ORDER BY created_at ASC, rowid ASC`

	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("listing execution snapshots: %w", err)
	}
	defer rows.Close()

	out := []instance.ExecutionSnapshot{}
	for rows.Next() {
		var (
			rawID, rawSnapshot string
			rawPins, rawRoutes sql.NullString
			pinsVersion        any
		)
		if err := rows.Scan(&rawID, &rawSnapshot, &rawPins, &pinsVersion, &rawRoutes); err != nil {
			return nil, fmt.Errorf("listing execution snapshots: %w", err)
		}
		id, err := parseUUID(rawID)
		if err != nil {
			return nil, fmt.Errorf("listing execution snapshots: %w", err)
		}
		pins, version, err := decodeStoredModelPins(rawPins, pinsVersion)
		if err == nil {
			_, _, err = decodeModelPinRoutes(rawRoutes, pins, version)
		}
		if err != nil {
			slog.Warn("execution snapshot inventory: skipping run with corrupt model pins", "instanceID", rawID, "error", err)
			continue
		}
		if version != instance.ModelPinsVersionLegacy {
			continue
		}
		snapshot, err := fromJSONString(rawSnapshot)
		if err != nil {
			return nil, fmt.Errorf("listing execution snapshots: instance %s: unmarshalling workflow snapshot: %w", id, err)
		}
		out = append(out, instance.ExecutionSnapshot{InstanceID: id, WorkflowSnapshot: snapshot})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing execution snapshots: %w", err)
	}
	return out, nil
}
