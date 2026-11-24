// domain/runner/hub_liveness.go
package runner

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Start runs Tick every Heartbeat until ctx ends.
func (h *Hub) Start(ctx context.Context) {
	h.loop.Add(1)
	go func() {
		defer h.loop.Done()
		defer h.closeProbes()
		t := time.NewTicker(h.cfg.Heartbeat)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_ = h.Tick(ctx)
			}
		}
	}()
}

// Tick is one liveness pass: expire unaccepted offers, then classify every
// online/suspect remote runner by the age of its last contact. Socket state
// is irrelevant: only heartbeat/message gaps count.
func (h *Hub) Tick(ctx context.Context) error {
	now := h.now()
	h.expireOffers(ctx, now)
	runners, err := h.reg.ListRunners(ctx)
	if err != nil {
		return fmt.Errorf("hub tick: %w", err)
	}
	var errs []error
	for _, r := range runners {
		if r.BuiltIn || (r.Liveness != LivenessOnline && r.Liveness != LivenessSuspect) {
			continue
		}
		last, ok := h.lastContact(r)
		if !ok {
			continue
		}
		gap := now.Sub(last)
		switch {
		case gap >= h.cfg.LostAfter:
			errs = append(errs, h.markLost(ctx, r.ID, last, CloseLost, fmt.Sprintf("no contact for %s", gap.Round(time.Second)),
				Cause{Kind: "liveness", Actor: "hub", Outcome: OutcomeRunnerLost}))
		case gap >= h.cfg.SuspectAfter && r.Liveness == LivenessOnline:
			if err := h.reg.SetLiveness(ctx, r.ID, LivenessSuspect, last); err != nil {
				errs = append(errs, err)
				continue
			}
			h.record(ctx, evtRunnerSuspect, r.ID, "", Cause{Kind: "liveness", Actor: "hub"}, map[string]any{"gapSeconds": int(gap.Seconds())})
			h.notifier.Notify()
		}
	}
	return errors.Join(errs...)
}

func (h *Hub) lastContact(r Runner) (time.Time, bool) {
	h.mu.Lock()
	t, ok := h.contact[r.ID]
	h.mu.Unlock()
	if ok {
		return t, true
	}
	if r.LastContact != nil {
		return *r.LastContact, true
	}
	return time.Time{}, false
}

// markLost tears a runner down: close its socket, mark it lost (before any
// waiter learns, so re-placement can never pick it), fail its waiters with
// OutcomeRunnerLost (the dispatcher requeues them with a new generation),
// release slots held by jobs no waiter owns, record runner.lost and wake.
func (h *Hub) markLost(ctx context.Context, id RunnerID, at time.Time, code int, reason string, cause Cause) error {
	h.admitMu.Lock()
	defer h.admitMu.Unlock()
	h.mu.Lock()
	s := h.sessions[id]
	delete(h.sessions, id)
	h.failProbesLocked(func(p *pendingProbe) bool { return p.runner == id }, ErrRemoteProbeUnavailable)
	var waiters []*remoteJob
	for _, j := range h.jobs {
		if j.a.Fence.RunnerID == id && !j.finished {
			waiters = append(waiters, j)
		}
	}
	h.mu.Unlock()
	if s != nil {
		_ = s.conn.Close(code, reason)
	}
	setErr := h.reg.SetLiveness(ctx, id, LivenessLost, at)
	lostErr := WithOutcome(fmt.Errorf("%w: %s: %s", ErrRunnerLost, id, reason), SourceOutcome{Class: OutcomeRunnerLost})
	owned := map[uuid.UUID]bool{}
	for _, j := range waiters {
		owned[j.a.Fence.JobID] = true
		h.finish(j, nil, lostErr)
	}
	active, listErr := h.ledger.ListActiveByRunner(ctx, id)
	var failErrs []error
	for _, job := range active {
		if owned[job.ID] || job.State == stJobQueued {
			continue
		}
		f := Fence{JobID: job.ID, RunnerID: id, Generation: job.Generation}
		if err := h.ledger.Fail(ctx, f, OutcomeRunnerLost); err != nil && !errors.Is(err, ErrFenced) {
			failErrs = append(failErrs, err)
		}
	}
	h.record(ctx, evtRunnerLost, id, "", cause, map[string]any{"reason": reason, "interruptedSteps": len(waiters)})
	h.notifier.Notify()
	return errors.Join(append([]error{setErr, listErr}, failErrs...)...)
}

// expireOffers fails offers not accepted within OfferTTL with OutcomeRunnerLost
// so the dispatcher re-places them (new generation); the runner is told to cancel.
func (h *Hub) expireOffers(ctx context.Context, now time.Time) {
	h.mu.Lock()
	var expired []*remoteJob
	for _, j := range h.jobs {
		if !j.finished && !j.accepted && now.Sub(j.offeredAt) >= h.cfg.OfferTTL {
			expired = append(expired, j)
		}
	}
	h.mu.Unlock()
	for _, j := range expired {
		err := WithOutcome(fmt.Errorf("%w: not accepted within %s", ErrOfferExpired, h.cfg.OfferTTL), SourceOutcome{Class: OutcomeRunnerLost})
		if h.finish(j, nil, err) {
			h.send(ctx, h.session(j.a.Fence.RunnerID), Outbound{Kind: MsgCancel, JobID: j.a.Fence.JobID, Generation: j.a.Fence.Generation})
		}
	}
}

// RevokeRunner implements SessionRevoker: the runner is refused from now on,
// its socket is closed with CloseRevoked and it is treated as lost.
func (h *Hub) RevokeRunner(ctx context.Context, id RunnerID) error {
	h.mu.Lock()
	h.revoked[id] = true
	h.mu.Unlock()
	h.record(ctx, evtRunnerKeyRevoked, id, "", Cause{Kind: "operator", Outcome: OutcomeRunnerLost}, nil)
	return h.markLost(ctx, id, h.now(), CloseRevoked, "runner key revoked", Cause{Kind: "operator", Outcome: OutcomeRunnerLost})
}
