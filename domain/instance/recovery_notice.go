package instance

import (
	"time"

	"github.com/google/uuid"
)

// RecoveryNoticeText is the disclosure shown wherever recovered work is read
// (design §7): recovery repeats interrupted steps, and their external side
// effects may happen twice.
const RecoveryNoticeText = "interrupted work may repeat external effects; exactly-once effects are not guaranteed"

// RecoveryNotice records one interrupted attempt of a step that was (or will
// be) repeated: a built-in job interrupted by a server restart, a lost or
// revoked runner. Derived at read time from the runner ledger.
type RecoveryNotice struct {
	NodeID   string    `json:"nodeId"`
	JobID    uuid.UUID `json:"jobId"`
	RunnerID string    `json:"runnerId"`
	Reason   string    `json:"reason"`
	At       time.Time `json:"at"`
	Text     string    `json:"text"`
}
