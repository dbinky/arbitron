package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/arbitron/arbitron/domain/runner"
)

var _ runner.PinnedSessionIndex = (*SessionPinIndex)(nil)
var _ runner.RunnerSessionIndex = (*SessionPinIndex)(nil)

// activeSessionExecSQL selects the active record of the outer ne row's node
// in its open visit. Attempts reset on back-edges and are not session identity.
// Shared with ListByRunner; the outer alias must be ne.
const activeSessionExecSQL = `SELECT x.id FROM node_executions x
JOIN node_invocations ni ON ni.id = x.session_invocation_id
WHERE x.instance_id = ne.instance_id AND x.node_id = ne.node_id
  AND ni.run_id = x.instance_id AND ni.node_id = x.node_id AND ni.committed_at IS NULL
ORDER BY x.session_generation DESC, x.started_at DESC, x.rowid DESC LIMIT 1`

// SessionPinIndex implements runner.PinnedSessionIndex over node_executions.
type SessionPinIndex struct{ db *sql.DB }

// NewSessionPinIndex creates a SessionPinIndex.
func NewSessionPinIndex(db *sql.DB) *SessionPinIndex { return &SessionPinIndex{db: db} }

// ListByRunner lists live pins at the run's current node for runner detail.
// Only the active record of the open visit counts, never an older attempt or visit.
func (x *SessionPinIndex) ListByRunner(ctx context.Context, id runner.RunnerID) ([]runner.RunnerSession, error) {
	rows, err := x.db.QueryContext(ctx, `
SELECT ne.instance_id, ne.node_id, ne.session_generation
FROM node_executions ne
JOIN instances i ON i.id = ne.instance_id
WHERE ne.session_pin_runner = ?
  AND i.state NOT IN ('completed', 'failed', 'cancelled')
  AND i.current_node_id = ne.node_id
  AND ne.id = (`+activeSessionExecSQL+`)
ORDER BY ne.instance_id, ne.node_id`, string(id))
	if err != nil {
		return nil, fmt.Errorf("listing sessions pinned to %s: %w", id, err)
	}
	defer rows.Close()
	out := []runner.RunnerSession{}
	for rows.Next() {
		var run, node string
		var gen int64
		if err := rows.Scan(&run, &node, &gen); err != nil {
			return nil, fmt.Errorf("scanning session pinned to %s: %w", id, err)
		}
		runID, err := parseUUID(run)
		if err != nil {
			return nil, err
		}
		out = append(out, runner.RunnerSession{RunID: runID, NodeID: node, Generation: gen})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing sessions pinned to %s: %w", id, err)
	}
	return out, nil
}

// ListPinned returns active pins at waiting runs' current nodes. Sources left
// over from a previous runner are unknown, not evidence for the new pin.
func (x *SessionPinIndex) ListPinned(ctx context.Context) ([]runner.PinnedSession, error) {
	rows, err := x.db.QueryContext(ctx, `
SELECT ne.id, ne.instance_id, ne.node_id, ne.session_pin_runner,
       CASE WHEN substr(COALESCE(ne.source_id, ''), 1, length(ne.session_pin_runner) + 1) = ne.session_pin_runner || '/'
            THEN ne.source_id ELSE '' END
FROM node_executions ne
JOIN instances i ON i.id = ne.instance_id
WHERE i.state = 'waiting'
  AND i.current_node_id = ne.node_id
  AND ne.session_pin_runner IS NOT NULL AND ne.session_pin_runner != ''
  AND ne.id = (`+activeSessionExecSQL+`)
ORDER BY ne.instance_id`)
	if err != nil {
		return nil, fmt.Errorf("listing pinned sessions: %w", err)
	}
	defer rows.Close()
	var out []runner.PinnedSession
	for rows.Next() {
		var id, run, node, pin, src string
		if err := rows.Scan(&id, &run, &node, &pin, &src); err != nil {
			return nil, fmt.Errorf("scanning pinned session: %w", err)
		}
		execID, err := parseUUID(id)
		if err != nil {
			return nil, err
		}
		runID, err := parseUUID(run)
		if err != nil {
			return nil, err
		}
		out = append(out, runner.PinnedSession{RunID: runID, NodeID: node, ExecutionID: execID,
			RunnerID: runner.RunnerID(pin), SourceID: runner.SourceID(src)})
	}
	return out, rows.Err()
}
