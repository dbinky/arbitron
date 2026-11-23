package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
)

var _ runner.SessionPins = (*SessionPinRepo)(nil)

// SessionPinRepo implements runner.SessionPins over node_executions.
type SessionPinRepo struct {
	execs *NodeExecutionRepo
}

// NewSessionPinRepo builds the lookup.
func NewSessionPinRepo(db *sql.DB) *SessionPinRepo {
	return &SessionPinRepo{execs: NewNodeExecutionRepo(db)}
}

// PinFor implements runner.SessionPins.
func (r *SessionPinRepo) PinFor(ctx context.Context, runID uuid.UUID, nodeID string) (runner.RunnerID, bool, error) {
	st, ok, err := r.execs.LatestSessionState(ctx, runID, nodeID)
	if err != nil {
		return "", false, fmt.Errorf("session pin: %w", err)
	}
	if !ok || st.PinRunner == nil || *st.PinRunner == "" {
		return "", false, nil
	}
	return runner.RunnerID(*st.PinRunner), true, nil
}
