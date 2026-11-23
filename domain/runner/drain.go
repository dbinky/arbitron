// domain/runner/drain.go
package runner

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Pool event kinds (contracts §9).
const (
	EventRunnerDraining = "runner.draining"
	EventRunnerDrained  = "runner.drained"
)

const (
	DrainKindWorkspace = "workspace"
	DrainKindSession   = "session"
)

type DrainItemState string

const (
	DrainItemPending DrainItemState = "pending"
	DrainItemMoving  DrainItemState = "moving"
	DrainItemBlocked DrainItemState = "blocked"
	DrainItemDone    DrainItemState = "done"
)

// DrainItem is one unit of drain work (drain_items row).
type DrainItem struct {
	RunnerID  RunnerID
	Kind      string
	RunID     uuid.UUID
	GroupKey  string
	State     DrainItemState
	Reason    string
	UpdatedAt time.Time
}

// DrainStore persists drain items so a blocked item stays visible across restarts.
type DrainStore interface {
	Upsert(ctx context.Context, it DrainItem) error
	List(ctx context.Context, id RunnerID) ([]DrainItem, error)
	Clear(ctx context.Context, id RunnerID) error
}

// DrainProgress is what runner_get and runner_drain report (contract §16).
type DrainProgress struct {
	ActiveSteps     int
	HomedWorkspaces int
	Sessions        int
	Blocked         []DrainBlocker
	Complete        bool
}

// DrainBlocker names work that cannot leave the runner and the missing capacity.
type DrainBlocker struct {
	Kind   string
	RunID  *uuid.UUID
	Group  string
	Reason string
}

var (
	ErrNotDraining      = errors.New("runner: runner is not draining")
	ErrDrainUnavailable = errors.New("runner: drain is not configured")
)
