package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/arbitron/arbitron/adapter/sqlite/generated"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// NodeExecutionRepo implements instance.NodeExecutionRepository backed by SQLite.
type NodeExecutionRepo struct {
	db      *sql.DB
	queries *generated.Queries
}

// NewNodeExecutionRepo creates a NodeExecutionRepo using the given database connection.
func NewNodeExecutionRepo(db *sql.DB) *NodeExecutionRepo {
	return &NodeExecutionRepo{
		db:      db,
		queries: generated.New(db),
	}
}

func (r *NodeExecutionRepo) Create(ctx context.Context, exec *instance.NodeExecution) error {
	resolvedInputsJSON, err := toNullJSONString(exec.ResolvedInputs)
	if err != nil {
		return fmt.Errorf("marshalling resolved inputs: %w", err)
	}
	outputJSON, err := toNullJSONString(exec.Output)
	if err != nil {
		return fmt.Errorf("marshalling output: %w", err)
	}
	errorJSON, err := toNullJSONString(exec.Error)
	if err != nil {
		return fmt.Errorf("marshalling error: %w", err)
	}
	llmConvJSON, err := toNullJSONString(exec.LLMConversation)
	if err != nil {
		return fmt.Errorf("marshalling llm conversation: %w", err)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	row, err := r.queries.WithTx(tx).CreateNodeExecution(ctx, generated.CreateNodeExecutionParams{
		ID:              uuidToString(exec.ID),
		InstanceID:      uuidToString(exec.InstanceID),
		NodeID:          exec.NodeID,
		Attempt:         int64(exec.Attempt),
		Status:          string(exec.Status),
		ResolvedInputs:  resolvedInputsJSON,
		Output:          outputJSON,
		Error:           errorJSON,
		LLMConversation: llmConvJSON,
		LLMModel:        ptrToNullString(exec.LLMModel),
		LLMTokensIn:     intPtrToNullInt64(exec.LLMTokensIn),
		LLMTokensOut:    intPtrToNullInt64(exec.LLMTokensOut),
		ScriptStdout:    ptrToNullString(exec.ScriptStdout),
		ScriptStderr:    ptrToNullString(exec.ScriptStderr),
		ScriptExitCode:  intPtrToNullInt64(exec.ScriptExitCode),
		StartedAt:       timeToString(exec.StartedAt),
		CompletedAt:     timeToNullString(exec.CompletedAt),
	})
	if err != nil {
		return fmt.Errorf("creating node execution: %w", err)
	}

	startedAt, err := parseTime(row.StartedAt)
	if err != nil {
		return fmt.Errorf("parsing started_at: %w", err)
	}
	completedAt, err := nullStringToTimePtr(row.CompletedAt)
	if err != nil {
		return fmt.Errorf("parsing completed_at: %w", err)
	}
	exec.StartedAt = startedAt
	exec.CompletedAt = completedAt
	if err := writePlacement(ctx, tx, exec); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *NodeExecutionRepo) Update(ctx context.Context, exec *instance.NodeExecution) error {
	outputJSON, err := toNullJSONString(exec.Output)
	if err != nil {
		return fmt.Errorf("marshalling output: %w", err)
	}
	errorJSON, err := toNullJSONString(exec.Error)
	if err != nil {
		return fmt.Errorf("marshalling error: %w", err)
	}
	llmConvJSON, err := toNullJSONString(exec.LLMConversation)
	if err != nil {
		return fmt.Errorf("marshalling llm conversation: %w", err)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	err = r.queries.WithTx(tx).UpdateNodeExecution(ctx, generated.UpdateNodeExecutionParams{
		ID:              uuidToString(exec.ID),
		Status:          string(exec.Status),
		Output:          outputJSON,
		Error:           errorJSON,
		LLMConversation: llmConvJSON,
		LLMModel:        ptrToNullString(exec.LLMModel),
		LLMTokensIn:     intPtrToNullInt64(exec.LLMTokensIn),
		LLMTokensOut:    intPtrToNullInt64(exec.LLMTokensOut),
		ScriptStdout:    ptrToNullString(exec.ScriptStdout),
		ScriptStderr:    ptrToNullString(exec.ScriptStderr),
		ScriptExitCode:  intPtrToNullInt64(exec.ScriptExitCode),
		CompletedAt:     timeToNullString(exec.CompletedAt),
	})
	if err != nil {
		return fmt.Errorf("updating node execution: %w", err)
	}
	if err := writePlacement(ctx, tx, exec); err != nil {
		return err
	}
	return tx.Commit()
}

var _ instance.OpenSessionRecordUpdater = (*NodeExecutionRepo)(nil)

// UpdateIfSessionOpen is Update guarded in the same statement by the stored
// llm_conversation having no endedAt, so a session end written concurrently
// is never overwritten. It reports whether the row was written.
func (r *NodeExecutionRepo) UpdateIfSessionOpen(ctx context.Context, exec *instance.NodeExecution) (bool, error) {
	outputJSON, err := toNullJSONString(exec.Output)
	if err != nil {
		return false, fmt.Errorf("marshalling output: %w", err)
	}
	errorJSON, err := toNullJSONString(exec.Error)
	if err != nil {
		return false, fmt.Errorf("marshalling error: %w", err)
	}
	llmConvJSON, err := toNullJSONString(exec.LLMConversation)
	if err != nil {
		return false, fmt.Errorf("marshalling llm conversation: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE node_executions SET status = ?, output = ?, error = ?, llm_conversation = ?, llm_model = ?, llm_tokens_in = ?, llm_tokens_out = ?, script_stdout = ?, script_stderr = ?, script_exit_code = ?, completed_at = ?
WHERE id = ? AND (llm_conversation IS NULL OR json_extract(llm_conversation, '$.endedAt') IS NULL)`,
		string(exec.Status), outputJSON, errorJSON, llmConvJSON, ptrToNullString(exec.LLMModel),
		intPtrToNullInt64(exec.LLMTokensIn), intPtrToNullInt64(exec.LLMTokensOut),
		ptrToNullString(exec.ScriptStdout), ptrToNullString(exec.ScriptStderr), intPtrToNullInt64(exec.ScriptExitCode),
		timeToNullString(exec.CompletedAt), uuidToString(exec.ID))
	if err != nil {
		return false, fmt.Errorf("updating open session node execution: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("updating open session node execution: %w", err)
	}
	if n == 1 {
		if err := writePlacement(ctx, tx, exec); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return n == 1, nil
}

// PatchSessionFields merges patch into the stored llm_conversation (RFC 7396
// json_patch) in one statement guarded by that conversation having no endedAt.
// Only the patched keys change, so a turn saved concurrently (messages,
// provider session ID) is never reverted. It reports whether the row was
// written.
func (r *NodeExecutionRepo) PatchSessionFields(ctx context.Context, id uuid.UUID, patch map[string]any) (bool, error) {
	patchJSON, err := json.Marshal(patch)
	if err != nil {
		return false, fmt.Errorf("marshalling session patch: %w", err)
	}
	res, err := r.db.ExecContext(ctx, `UPDATE node_executions SET llm_conversation = json_patch(COALESCE(llm_conversation, '{}'), ?)
WHERE id = ? AND (llm_conversation IS NULL OR json_extract(llm_conversation, '$.endedAt') IS NULL)`,
		string(patchJSON), uuidToString(id))
	if err != nil {
		return false, fmt.Errorf("patching open session node execution: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("patching open session node execution: %w", err)
	}
	return n == 1, nil
}

func (r *NodeExecutionRepo) ListByInstance(ctx context.Context, instanceID uuid.UUID) ([]instance.NodeExecution, error) {
	rows, err := r.queries.ListNodeExecutionsByInstance(ctx, uuidToString(instanceID))
	if err != nil {
		return nil, fmt.Errorf("listing node executions: %w", err)
	}

	executions := make([]instance.NodeExecution, 0, len(rows))
	for _, row := range rows {
		exec, err := r.rowToNodeExecution(row)
		if err != nil {
			return nil, err
		}
		executions = append(executions, *exec)
	}
	placements, err := r.readPlacements(ctx, `instance_id = ?`, uuidToString(instanceID))
	if err != nil {
		return nil, err
	}
	for i := range executions {
		if p, ok := placements[executions[i].ID]; ok {
			p.applyTo(&executions[i])
		}
	}
	ptrs := make([]*instance.NodeExecution, len(executions))
	for i := range executions {
		ptrs[i] = &executions[i]
	}
	if err := r.attachSessionState(ctx, instanceID, ptrs); err != nil {
		return nil, err
	}
	return executions, nil
}

func (r *NodeExecutionRepo) GetByInstanceAndNode(ctx context.Context, instanceID uuid.UUID, nodeID string) (*instance.NodeExecution, error) {
	// Attempt numbers reset on a new visit, so select the latest execution by
	// start order before loading the explicit sqlc column projection.
	var id string
	err := r.db.QueryRowContext(ctx, `SELECT id FROM node_executions WHERE instance_id = ? AND node_id = ? ORDER BY started_at DESC, rowid DESC LIMIT 1`, uuidToString(instanceID), nodeID).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, instance.ErrNotFound
		}
		return nil, fmt.Errorf("finding latest node execution: %w", err)
	}
	executions, err := r.ListByInstance(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	for i := range executions {
		if uuidToString(executions[i].ID) == id {
			return &executions[i], nil
		}
	}
	return nil, instance.ErrNotFound
}

// AggregateByInstances collapses node executions for the given instance IDs
// into one aggregate per instance. It uses a single grouped query with a
// placeholder per id. An empty ids slice returns an empty map.
func (r *NodeExecutionRepo) AggregateByInstances(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]instance.NodeExecutionAggregate, error) {
	out := make(map[uuid.UUID]instance.NodeExecutionAggregate, len(ids))
	if len(ids) == 0 {
		return out, nil
	}

	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = uuidToString(id)
	}
	inClause := strings.Join(placeholders, ",")
	query := `SELECT ne.instance_id,
		COALESCE(MAX(ne.attempt), 0) AS max_attempt
	FROM node_executions ne
	WHERE ne.instance_id IN (` + inClause + `)
	GROUP BY ne.instance_id`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("aggregating node executions: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var instIDStr string
		var maxAttempt int64
		if err := rows.Scan(&instIDStr, &maxAttempt); err != nil {
			return nil, fmt.Errorf("scanning node execution aggregate: %w", err)
		}
		instID, err := parseUUID(instIDStr)
		if err != nil {
			return nil, fmt.Errorf("parsing instance id in aggregate: %w", err)
		}
		out[instID] = instance.NodeExecutionAggregate{
			InstanceID: instID,
			MaxAttempt: int(maxAttempt),
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating node execution aggregates: %w", err)
	}
	return out, nil
}

func (r *NodeExecutionRepo) rowToNodeExecution(row generated.NodeExecution) (*instance.NodeExecution, error) {
	resolvedInputs, err := fromNullJSONString(row.ResolvedInputs)
	if err != nil {
		return nil, fmt.Errorf("unmarshalling resolved inputs: %w", err)
	}
	output, err := fromNullJSONString(row.Output)
	if err != nil {
		return nil, fmt.Errorf("unmarshalling output: %w", err)
	}
	execError, err := fromNullJSONString(row.Error)
	if err != nil {
		return nil, fmt.Errorf("unmarshalling error: %w", err)
	}
	llmConv, err := fromNullJSONString(row.LLMConversation)
	if err != nil {
		return nil, fmt.Errorf("unmarshalling llm conversation: %w", err)
	}

	id, err := parseUUID(row.ID)
	if err != nil {
		return nil, fmt.Errorf("converting node execution row: %w", err)
	}
	instanceID, err := parseUUID(row.InstanceID)
	if err != nil {
		return nil, fmt.Errorf("converting node execution row: %w", err)
	}
	startedAt, err := parseTime(row.StartedAt)
	if err != nil {
		return nil, fmt.Errorf("converting node execution row: %w", err)
	}
	completedAt, err := nullStringToTimePtr(row.CompletedAt)
	if err != nil {
		return nil, fmt.Errorf("converting node execution row: %w", err)
	}

	return &instance.NodeExecution{
		ID:              id,
		InstanceID:      instanceID,
		NodeID:          row.NodeID,
		Attempt:         int(row.Attempt),
		Status:          instance.NodeExecutionStatus(row.Status),
		ResolvedInputs:  resolvedInputs,
		Output:          output,
		Error:           execError,
		LLMConversation: llmConv,
		LLMModel:        nullStringToPtr(row.LLMModel),
		LLMTokensIn:     nullInt64ToIntPtr(row.LLMTokensIn),
		LLMTokensOut:    nullInt64ToIntPtr(row.LLMTokensOut),
		ScriptStdout:    nullStringToPtr(row.ScriptStdout),
		ScriptStderr:    nullStringToPtr(row.ScriptStderr),
		ScriptExitCode:  nullInt64ToIntPtr(row.ScriptExitCode),
		StartedAt:       startedAt,
		CompletedAt:     completedAt,
	}, nil
}

// placementRow is the hand-written read of migration 042's columns; the
// sqlc-generated queries predate them and stay unchanged.
type placementRow struct {
	runnerID, sourceID, reason sql.NullString
	history                    sql.NullString
}

func (p placementRow) applyTo(exec *instance.NodeExecution) {
	exec.RunnerID = nullStringToPtr(p.runnerID)
	exec.SourceID = nullStringToPtr(p.sourceID)
	exec.PlacementReason = nullStringToPtr(p.reason)
	if p.history.Valid && p.history.String != "" {
		var hist []instance.PlacementRecord
		if err := json.Unmarshal([]byte(p.history.String), &hist); err == nil {
			exec.PlacementHistory = hist
		}
	}
}

// writePlacement persists placement in the same transaction as the trace or
// session mutation. An update carrying no runner or history preserves placement.
func writePlacement(ctx context.Context, tx *sql.Tx, exec *instance.NodeExecution) error {
	if exec.RunnerID == nil && len(exec.PlacementHistory) == 0 {
		return nil
	}
	var hist sql.NullString
	if len(exec.PlacementHistory) > 0 {
		b, err := json.Marshal(exec.PlacementHistory)
		if err != nil {
			return fmt.Errorf("marshalling placement history: %w", err)
		}
		hist = sql.NullString{String: string(b), Valid: true}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE node_executions SET runner_id = ?, source_id = ?, placement_reason = ?, placement_history = ? WHERE id = ?`,
		ptrToNullString(exec.RunnerID), ptrToNullString(exec.SourceID), ptrToNullString(exec.PlacementReason), hist, uuidToString(exec.ID),
	); err != nil {
		return fmt.Errorf("writing node execution placement: %w", err)
	}
	return nil
}

// readPlacements loads placement columns for the rows matching where/arg,
// keyed by node-execution id. Rows without any placement are omitted.
func (r *NodeExecutionRepo) readPlacements(ctx context.Context, where string, arg string) (map[uuid.UUID]placementRow, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, runner_id, source_id, placement_reason, placement_history FROM node_executions
		 WHERE `+where+` AND (runner_id IS NOT NULL OR placement_history IS NOT NULL)`, arg)
	if err != nil {
		return nil, fmt.Errorf("reading node execution placement: %w", err)
	}
	defer rows.Close()
	out := map[uuid.UUID]placementRow{}
	for rows.Next() {
		var idStr string
		var p placementRow
		if err := rows.Scan(&idStr, &p.runnerID, &p.sourceID, &p.reason, &p.history); err != nil {
			return nil, fmt.Errorf("scanning node execution placement: %w", err)
		}
		id, err := parseUUID(idStr)
		if err != nil {
			return nil, fmt.Errorf("parsing node execution id: %w", err)
		}
		out[id] = p
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating node execution placement: %w", err)
	}
	return out, nil
}
