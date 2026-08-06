package sqlite

import (
	"database/sql"
	"fmt"

	"github.com/arbitron/arbitron/adapter/sqlite/generated"
	"github.com/arbitron/arbitron/domain/instance"
)

// instanceColumnsWithLink is the explicit, ordered column list for every
// full-row instance read that must also carry the spawn link. It is written out
// (never `SELECT *`) so the scan order below is pinned by this file rather than
// by the physical column order of the table.
//
// The first 23 columns are exactly generated.Instance's field order, so a row
// scanned with this list can be assembled into a generated.Instance and handed
// to the existing rowToInstance converter — the three link columns are the only
// thing scanned separately. That keeps ONE conversion routine for instances.
const instanceColumnsWithLink = `id, owner_id, workflow_name, workflow_snapshot, state, priority, ` +
	`variables, original_variables, current_node_id, failed_node_id, created_at, updated_at, ` +
	`completed_at, queued_at, waiting_until, webhook_token, webhook_secret_hash, ` +
	`webhook_received_at, department, visibility, trigger_source, schedule_id, workflow_id, ` +
	`parent_instance_id, parent_node_id, spawn_mode, spawn_depth`

// instanceSummaryColumns is the explicit column list for the LEAN list
// projection. It is the column set the generated ListInstances query selected,
// plus parent_instance_id so the hierarchy read needs no per-row load. Full
// blobs (original_variables, webhook fields) stay out of the page on purpose.
const instanceSummaryColumns = `id, owner_id, workflow_name, state, priority, current_node_id, ` +
	`failed_node_id, variables, workflow_snapshot, created_at, updated_at, completed_at, ` +
	`queued_at, department, visibility, trigger_source, schedule_id, parent_instance_id`

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanInstanceWithLink reads one row selected with instanceColumnsWithLink and
// converts it to a domain Instance, including the parent/child spawn link.
func scanInstanceWithLink(s rowScanner) (*instance.Instance, error) {
	var row generated.Instance
	var parentID, parentNode, spawnMode sql.NullString
	var spawnDepth int
	if err := s.Scan(
		&row.ID,
		&row.OwnerID,
		&row.WorkflowName,
		&row.WorkflowSnapshot,
		&row.State,
		&row.Priority,
		&row.Variables,
		&row.OriginalVariables,
		&row.CurrentNodeID,
		&row.FailedNodeID,
		&row.CreatedAt,
		&row.UpdatedAt,
		&row.CompletedAt,
		&row.QueuedAt,
		&row.WaitingUntil,
		&row.WebhookToken,
		&row.WebhookSecretHash,
		&row.WebhookReceivedAt,
		&row.Department,
		&row.Visibility,
		&row.TriggerSource,
		&row.ScheduleID,
		&row.WorkflowID,
		&parentID,
		&parentNode,
		&spawnMode,
		&spawnDepth,
	); err != nil {
		return nil, err
	}

	inst, err := rowToInstance(row)
	if err != nil {
		return nil, err
	}

	// ALL-OR-NONE on the read side: an incomplete tuple collapses to no link
	// (a root run) rather than being partially populated. See
	// nullStringToSpawnLink.
	pid, nodeID, mode, err := nullStringToSpawnLink(parentID, parentNode, spawnMode)
	if err != nil {
		return nil, fmt.Errorf("converting instance row: %w", err)
	}
	inst.ParentInstanceID = pid
	inst.ParentNodeID = nodeID
	inst.SpawnMode = mode
	inst.SpawnDepth = spawnDepth
	return inst, nil
}
