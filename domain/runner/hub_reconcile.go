// domain/runner/hub_reconcile.go
package runner

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// reconcileHeld answers each job the runner reports holding at admission:
// a live generation is (re)accepted, its lease extended, and the last
// contiguous output sequence acked so the runner resends only what is
// missing; an already-consumed terminal result is acked; anything else is
// fenced. It returns the set of job ids the runner claimed.
func (h *Hub) reconcileHeld(ctx context.Context, s *Session, held []HeldJobInfo, now time.Time) map[uuid.UUID]bool {
	claimed := make(map[uuid.UUID]bool, len(held))
	for _, hj := range held {
		claimed[hj.JobID] = true
		f := Fence{JobID: hj.JobID, RunnerID: s.RunnerID, Generation: hj.Generation}
		j := h.liveJob(f)
		if j == nil {
			receipt, ok, err := h.receipt(ctx, f)
			if err != nil {
				if errors.Is(err, ErrFenced) || errors.Is(err, ErrJobNotFound) {
					h.send(ctx, s, Outbound{Kind: MsgFenced, JobID: f.JobID, Generation: f.Generation})
				}
				continue
			}
			if hj.State == HeldTerminal && ok && receipt.Result != nil {
				h.send(ctx, s, Outbound{Kind: MsgAck, JobID: f.JobID, Generation: f.Generation, Ack: &AckPayload{Result: true}})
				continue
			}
			if h.adoptable(ctx, hj, f) && h.ledger.ExtendLease(ctx, f, now.Add(h.cfg.LostAfter)) == nil {
				h.send(ctx, s, Outbound{Kind: MsgAck, JobID: f.JobID, Generation: f.Generation, Ack: &AckPayload{Seq: receipt.LastOutputSeq}})
				continue
			}
			h.send(ctx, s, Outbound{Kind: MsgFenced, JobID: f.JobID, Generation: f.Generation})
			continue
		}
		h.markAccepted(j)
		if err := h.ledger.ExtendLease(ctx, f, now.Add(h.cfg.LostAfter)); err != nil {
			if errors.Is(err, ErrFenced) {
				_ = h.fenceLive(ctx, s, j, err)
			}
			continue
		}
		receipt, _, err := h.receipt(ctx, f)
		if err != nil {
			continue
		}
		seq := receipt.LastOutputSeq
		h.send(ctx, s, Outbound{Kind: MsgAck, JobID: f.JobID, Generation: f.Generation, Ack: &AckPayload{Seq: seq}})
	}
	return claimed
}

// adoptable requires a running claim backed by the current durable fence.
func (h *Hub) adoptable(ctx context.Context, hj HeldJobInfo, f Fence) bool {
	return hj.State == HeldRunning && h.ledger.FenceCheck(ctx, f) == nil
}

// resendOffers re-sends every unaccepted offer for this runner that the
// runner did not claim. The runner dedupes by (job, generation).
func (h *Hub) resendOffers(ctx context.Context, s *Session, claimed map[uuid.UUID]bool) {
	h.mu.Lock()
	var pending []*remoteJob
	for _, j := range h.jobs {
		if j.a.Fence.RunnerID == s.RunnerID && !j.finished && !j.accepted && !claimed[j.a.Fence.JobID] {
			pending = append(pending, j)
		}
	}
	h.mu.Unlock()
	for _, j := range pending {
		h.send(ctx, s, dispatchMsg(j))
	}
}
