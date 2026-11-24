package runner

import (
	"context"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
)

// Transfer directions match migration 044's transfer_sessions vocabulary.
const (
	TransferPull   = "pull"
	TransferPush   = "push"
	TransferBundle = "bundle"
)

// DefaultTransferTokenTTL is used when no positive transfer lifetime is supplied.
const DefaultTransferTokenTTL = 15 * time.Minute

// TransferScope names exactly one job fence or durable maintenance operation,
// together with the pinned checkpoint/bundle and exact writer claim.
type TransferScope struct {
	Fence                  Fence
	MaintenanceOperationID string
	RunID                  uuid.UUID
	Direction              string
	BaseRef                string
	BundleHash             string
	Resource               engine.ResourceFenceRef
}

// TransferToken carries the exact authority issued for one transfer.
type TransferToken struct {
	Token                  string
	MaintenanceOperationID string
	JobID                  uuid.UUID
	Generation             int64
	RunnerID               RunnerID
	RunID                  uuid.UUID
	Direction              string
	BaseRef                string
	Resource               engine.ResourceFenceRef
	BundleHash             string
	ExpiresAt              time.Time
}

// Scope projects all authorization fields, excluding token text and expiry.
func (t TransferToken) Scope() TransferScope {
	return TransferScope{
		Fence:                  Fence{JobID: t.JobID, RunnerID: t.RunnerID, Generation: t.Generation},
		MaintenanceOperationID: t.MaintenanceOperationID,
		RunID:                  t.RunID, Direction: t.Direction, BaseRef: t.BaseRef,
		BundleHash: t.BundleHash, Resource: t.Resource,
	}
}

// TransferTokens issues and validates tokens against live job/resource fences.
type TransferTokens interface {
	Issue(ctx context.Context, scope TransferScope, ttl time.Duration) (TransferToken, error)
	Validate(ctx context.Context, token string, runner RunnerID) (TransferToken, error)
}
