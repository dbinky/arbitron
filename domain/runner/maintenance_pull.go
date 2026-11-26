// domain/runner/maintenance_pull.go
package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
)

// Pull-only maintenance transfer messages (hub ↔ runner).
const (
	MsgPullWorkspace  = "pull_workspace"   // hub → runner
	MsgPullReceipt    = "pull_receipt"     // runner → hub (live, or replayed in hello)
	MsgPullReceiptAck = "pull_receipt_ack" // hub → runner: stop replaying
)

// PullWorkspacePayload asks a runner to fetch and verify exactly Ref under Resource.
type PullWorkspacePayload struct {
	OperationID    string
	RunID          uuid.UUID
	WorkDir        string
	HomeGeneration int64
	Ref            CheckpointRef
	Resource       engine.ResourceFenceRef
	PullToken      string
	PartBytes      int64
}

// PullReceiptPayload is a runner's terminal answer to one PullWorkspacePayload.
type PullReceiptPayload struct {
	OperationID string
	RunID       uuid.UUID
	Ref         CheckpointRef
	Resource    engine.ResourceFenceRef
	OK          bool
	Error       string
}

// PullReceipt proves a target verified a committed checkpoint under a live claim.
type PullReceipt struct {
	OperationID string
	RunnerID    RunnerID
	Ref         CheckpointRef
	Resource    engine.ResourceFenceRef
}

// DrainPuller fetches a committed checkpoint without consuming an execution slot.
type DrainPuller interface {
	PullCommitted(context.Context, uuid.UUID, RunnerID, CheckpointRef, engine.ResourceFenceRef) (PullReceipt, error)
}

// PullRequester is the hub's send side (*Hub implements it, Cycle 2c).
type PullRequester interface {
	RequestPull(ctx context.Context, target RunnerID, p PullWorkspacePayload) error
	AckPullReceipt(ctx context.Context, target RunnerID, operationID string)
}

// PullTokenIssuer is the slice of P3 T7's TransferTokenService used here.
type PullTokenIssuer interface {
	Issue(ctx context.Context, scope TransferScope, ttl time.Duration) (TransferToken, error)
}

var (
	ErrPullScope           = errors.New("runner: maintenance pull claim does not cover this run and target")
	ErrPullTargetOffline   = errors.New("runner: maintenance pull target is not connected")
	ErrPullFailed          = errors.New("runner: target runner could not pull the workspace")
	ErrPullReceiptMismatch = errors.New("runner: pull receipt does not match the requested transfer")
	ErrPullTimeout         = errors.New("runner: timed out waiting for the target's pull receipt")
)

const maxEarlyPullReceipts = 256

// PullOperationID is the durable identity of the pull authorized by claim:
// identical for a replay, different for any later claim.
func PullOperationID(claim engine.ResourceFenceRef) string {
	return fmt.Sprintf("pull:%s@%d/%s", claim.Key, claim.Generation, claim.Owner)
}

// PullCoordinatorDeps: all required except Clock (time.Now), Timeout (10 min),
// TokenTTL (Timeout) and PartBytes (8 MiB).
type PullCoordinatorDeps struct {
	Hub        PullRequester
	Tokens     PullTokenIssuer
	Leases     ResourceLeaseStore
	Homes      HomeStore
	Workspaces engine.WorkspaceManager
	Clock      func() time.Time
	Timeout    time.Duration
	TokenTTL   time.Duration
	PartBytes  int64
}

type pullWaiter struct {
	target RunnerID
	ch     chan PullReceiptPayload
}

type earlyPull struct {
	from RunnerID
	rc   PullReceiptPayload
}

// WorkspacePullCoordinator implements DrainPuller.
type WorkspacePullCoordinator struct {
	d       PullCoordinatorDeps
	mu      sync.Mutex
	waiting map[string]pullWaiter
	early   map[string]earlyPull
}

func NewWorkspacePullCoordinator(d PullCoordinatorDeps) (*WorkspacePullCoordinator, error) {
	var missing []string
	if d.Hub == nil {
		missing = append(missing, "Hub")
	}
	if d.Tokens == nil {
		missing = append(missing, "Tokens")
	}
	if d.Leases == nil {
		missing = append(missing, "Leases")
	}
	if d.Homes == nil {
		missing = append(missing, "Homes")
	}
	if d.Workspaces == nil {
		missing = append(missing, "Workspaces")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("runner: NewWorkspacePullCoordinator missing %s", strings.Join(missing, ", "))
	}
	if d.Clock == nil {
		d.Clock = time.Now
	}
	if d.Timeout <= 0 {
		d.Timeout = 10 * time.Minute
	}
	if d.TokenTTL <= 0 {
		d.TokenTTL = d.Timeout
	}
	if d.PartBytes <= 0 {
		d.PartBytes = 8 << 20
	}
	return &WorkspacePullCoordinator{d: d, waiting: map[string]pullWaiter{}, early: map[string]earlyPull{}}, nil
}

// PullCommitted makes target fetch and verify exactly ref while claim (owned
// by target) is live, and returns only a receipt matching that request.
func (c *WorkspacePullCoordinator) PullCommitted(ctx context.Context, runID uuid.UUID, target RunnerID, ref CheckpointRef, claim engine.ResourceFenceRef) (PullReceipt, error) {
	if claim.Key != runKey(runID) || claim.Owner != string(target) || (!ref.IsZero() && ref.RunID != runID) {
		return PullReceipt{}, ErrPullScope
	}
	if err := c.d.Leases.Check(ctx, claim); err != nil {
		return PullReceipt{}, err
	}
	opID := PullOperationID(claim)
	want := PullReceipt{OperationID: opID, RunnerID: target, Ref: ref, Resource: claim}
	if ref.IsZero() {
		return want, nil // an empty workspace: nothing to fetch
	}
	ch, stored := c.wait(opID, target)
	defer c.forget(opID)
	if !stored {
		if err := c.request(ctx, runID, target, ref, claim, opID); err != nil {
			return PullReceipt{}, err
		}
	}
	timer := time.NewTimer(c.d.Timeout)
	defer timer.Stop()
	select {
	case rc := <-ch:
		if !rc.OK {
			return PullReceipt{}, fmt.Errorf("%w: %s", ErrPullFailed, rc.Error)
		}
		if rc.RunID != runID || rc.Ref != ref || rc.Resource != claim {
			return PullReceipt{}, ErrPullReceiptMismatch
		}
		if err := c.d.Leases.Check(ctx, claim); err != nil {
			return PullReceipt{}, err
		}
		return want, nil
	case <-timer.C:
		return PullReceipt{}, fmt.Errorf("%w (%s on %s)", ErrPullTimeout, opID, target)
	case <-ctx.Done():
		return PullReceipt{}, ctx.Err()
	}
}

func (c *WorkspacePullCoordinator) request(ctx context.Context, runID uuid.UUID, target RunnerID, ref CheckpointRef, claim engine.ResourceFenceRef, opID string) error {
	home, err := c.d.Homes.GetHome(ctx, runID)
	if err != nil {
		return err
	}
	workDir, err := c.d.Workspaces.EnsureInstanceDir(runID)
	if err != nil {
		return err
	}
	tok, err := c.d.Tokens.Issue(ctx, TransferScope{Fence: Fence{RunnerID: target}, MaintenanceOperationID: opID, RunID: runID,
		Direction: TransferPull, BaseRef: ref.String(), Resource: claim}, c.d.TokenTTL)
	if err != nil {
		return err
	}
	return c.d.Hub.RequestPull(ctx, target, PullWorkspacePayload{OperationID: opID, RunID: runID, WorkDir: workDir,
		HomeGeneration: home.Generation + 1, Ref: ref, Resource: claim, PullToken: tok.Token, PartBytes: c.d.PartBytes})
}

// wait registers the waiter; stored reports that a receipt was already there.
func (c *WorkspacePullCoordinator) wait(opID string, target RunnerID) (<-chan PullReceiptPayload, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan PullReceiptPayload, 1)
	if e, ok := c.early[opID]; ok && e.from == target {
		delete(c.early, opID)
		ch <- e.rc
		return ch, true
	}
	c.waiting[opID] = pullWaiter{target: target, ch: ch}
	return ch, false
}

func (c *WorkspacePullCoordinator) forget(opID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.waiting, opID)
}

// Receive takes every pull receipt the hub reads (live, or replayed in
// hello) and always acknowledges it. A receipt for an operation nobody waits
// for is kept (bounded) for the pass that retries it.
func (c *WorkspacePullCoordinator) Receive(ctx context.Context, from RunnerID, rc PullReceiptPayload) {
	defer c.d.Hub.AckPullReceipt(ctx, from, rc.OperationID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if w, ok := c.waiting[rc.OperationID]; ok {
		if w.target == from {
			select {
			case w.ch <- rc:
			default:
			}
		}
		return
	}
	if len(c.early) >= maxEarlyPullReceipts {
		for k := range c.early {
			delete(c.early, k)
			break
		}
	}
	c.early[rc.OperationID] = earlyPull{from: from, rc: rc}
}
