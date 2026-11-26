// domain/runner/hub_pull.go
package runner

import (
	"context"
	"fmt"
)

// PullReceiver takes pull receipts read from runners (*WorkspacePullCoordinator).
type PullReceiver interface {
	Receive(ctx context.Context, from RunnerID, rc PullReceiptPayload)
}

// SetPullReceiver installs the coordinator that owns pull receipts.
func (h *Hub) SetPullReceiver(r PullReceiver) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pulls = r
}

// RequestPull sends pull_workspace to target's live session.
func (h *Hub) RequestPull(ctx context.Context, target RunnerID, p PullWorkspacePayload) error {
	s := h.session(target)
	if s == nil {
		return fmt.Errorf("%w: %s", ErrPullTargetOffline, target)
	}
	if err := s.conn.Send(ctx, Outbound{Kind: MsgPullWorkspace, PullWorkspace: &p}); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrPullTargetOffline, target, err)
	}
	return nil
}

// AckPullReceipt tells target to stop replaying operationID (best effort).
func (h *Hub) AckPullReceipt(ctx context.Context, target RunnerID, operationID string) {
	h.send(ctx, h.session(target), Outbound{Kind: MsgPullReceiptAck, PullReceiptAck: operationID})
}

func (h *Hub) deliverPullReceipt(ctx context.Context, s *Session, rc PullReceiptPayload) error {
	if rc.OperationID == "" {
		return fmt.Errorf("%w: pull receipt without an operation", ErrMalformedMessage)
	}
	h.mu.Lock()
	r := h.pulls
	h.mu.Unlock()
	if r == nil {
		return ErrDrainUnavailable
	}
	r.Receive(ctx, s.RunnerID, rc)
	return nil
}
