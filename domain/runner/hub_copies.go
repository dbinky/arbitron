package runner

import (
	"context"
	"log/slog"
)

// SetCopyReconciler installs the reconciler consulted on every admission.
func (h *Hub) SetCopyReconciler(r CopyReconciler) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.copies = r
}

// DeleteCopy sends a best-effort delete to a live session. Inventory remains
// pending until a later hello confirms absence, including after a failed send.
func (h *Hub) DeleteCopy(ctx context.Context, id RunnerID, p DeleteCopyPayload) bool {
	s := h.session(id)
	if s == nil {
		return false
	}
	h.send(ctx, s, Outbound{Kind: MsgDeleteCopy, DeleteCopy: &p})
	return true
}

func (h *Hub) reconcileCopies(ctx context.Context, s *Session, held []HeldCopyInfo) {
	h.mu.Lock()
	r := h.copies
	h.mu.Unlock()
	if r == nil {
		return
	}
	// The reconciler sends through DeleteCopy while holding its authorization.
	if _, err := r.ReconcileCopies(ctx, s.RunnerID, held); err != nil {
		slog.Warn("reconciling runner copies", "runner", s.RunnerID, "error", err)
	}
}
