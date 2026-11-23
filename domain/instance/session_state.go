package instance

import (
	"time"

	"github.com/google/uuid"
)

// SessionState is the durable per-session state of an interactive record: the
// latest committed session-bundle reference, the runner the session is pinned
// to, and the session generation that committed them.
type SessionState struct {
	CheckpointRef *string
	PinRunner     *string
	Generation    int64

	InvocationID uuid.UUID
}

// SessionMoveRecord is one committed session move. Reason is one of
// exhausted, logged_out, drain, runner_lost — never load.
type SessionMoveRecord struct {
	ID           uuid.UUID `json:"id"`
	RunID        uuid.UUID `json:"runId"`
	InvocationID uuid.UUID `json:"invocationId"`
	NodeID       string    `json:"nodeId"`
	FromRunner   string    `json:"fromRunner"`
	ToRunner     string    `json:"toRunner"`
	Reason       string    `json:"reason"`
	Lossy        bool      `json:"lossy"`
	Notice       string    `json:"notice,omitempty"`
	Generation   int64     `json:"generation"`
	At           time.Time `json:"at"`

	RequestID uuid.UUID `json:"requestId"`
	TurnIndex int       `json:"turnIndex"`
}
