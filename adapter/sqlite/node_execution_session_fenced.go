package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

var _ instance.SessionConversationFencer = (*NodeExecutionRepo)(nil)

// PatchSessionFieldsAtGeneration implements instance.SessionConversationFencer:
// one RFC 7396 merge (null deletes) applied only while session_generation
// equals generation and the session is still open.
func (r *NodeExecutionRepo) PatchSessionFieldsAtGeneration(ctx context.Context, id uuid.UUID, generation int64, patch map[string]any) error {
	patchJSON, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshalling fenced session patch: %w", err)
	}
	res, err := r.db.ExecContext(ctx, `UPDATE node_executions SET llm_conversation = json_patch(COALESCE(llm_conversation, '{}'), ?)
WHERE id = ? AND session_generation = ? AND (llm_conversation IS NULL OR json_extract(llm_conversation, '$.endedAt') IS NULL)`,
		string(patchJSON), uuidToString(id), generation)
	if err != nil {
		return fmt.Errorf("fenced session patch: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("fenced session patch: %w", err)
	}
	if n == 1 {
		return nil
	}
	var gen int64
	var ended any
	err = r.db.QueryRowContext(ctx, `SELECT session_generation, json_extract(llm_conversation, '$.endedAt') FROM node_executions WHERE id = ?`,
		uuidToString(id)).Scan(&gen, &ended)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("node execution %s: %w", id, instance.ErrNotFound)
	case err != nil:
		return fmt.Errorf("fenced session patch: %w", err)
	case gen != generation:
		return fmt.Errorf("node execution %s: generation %d, not %d: %w", id, gen, generation, instance.ErrStaleSessionGeneration)
	}
	return fmt.Errorf("node execution %s: %w", id, instance.ErrSessionClosed)
}
