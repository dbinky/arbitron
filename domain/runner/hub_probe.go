package runner

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// DefaultRemoteProbeTimeout bounds one correlated read-only remote check.
const DefaultRemoteProbeTimeout = 10 * time.Second

type pendingProbe struct {
	runner   RunnerID
	serial   uint64
	source   SourceID
	kind     CredentialKind
	ref      string
	revision uint64
	sentAt   time.Time
	done     chan probeOutcome // buffered 1; written once under h.mu
}

type probeOutcome struct {
	res ProbeResult
	err error
}

type observationMark struct{ serial, seq uint64 }

// ProbeSource asks the owning runner's current session for a fresh read-only
// probe and waits for the exact correlated reply. It spends no tokens and no
// slot, and never returns cached health.
func (h *Hub) ProbeSource(ctx context.Context, id SourceID) (ProbeResult, error) {
	src, err := h.reg.GetSource(ctx, id)
	if err != nil {
		return ProbeResult{}, err
	}
	if src.RunnerID == BuiltInRunnerID || !remoteKind(src.Kind) {
		return ProbeResult{}, fmt.Errorf("%w: %s is not a remote source", ErrRemoteProbeUnavailable, id)
	}
	timeout := h.cfg.ProbeTimeout
	if timeout <= 0 {
		timeout = DefaultRemoteProbeTimeout
	}
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	reqID := uuid.New()
	h.mu.Lock()
	s := h.sessions[src.RunnerID]
	if h.closed || s == nil || h.revoked[src.RunnerID] {
		h.mu.Unlock()
		return ProbeResult{}, fmt.Errorf("%w: runner %s has no live session", ErrRemoteProbeUnavailable, src.RunnerID)
	}
	p := &pendingProbe{runner: src.RunnerID, serial: s.serial, source: id, kind: src.Kind, ref: src.CredentialRef,
		revision: src.Health.Revision, sentAt: h.now(), done: make(chan probeOutcome, 1)}
	h.probes[reqID] = p // registered before send
	h.mu.Unlock()
	defer h.dropProbe(reqID)
	msg := Outbound{Kind: MsgProbe, Probe: &ProbePayload{RequestID: reqID, HealthRevision: p.revision, SourceKind: src.Kind, CredentialRef: src.CredentialRef}}
	if err := s.conn.Send(pctx, msg); err != nil {
		return ProbeResult{}, fmt.Errorf("%w: send: %v", ErrRemoteProbeUnavailable, err)
	}
	select {
	case o := <-p.done:
		return o.res, o.err
	case <-pctx.Done():
		if ctx.Err() != nil {
			return ProbeResult{}, ctx.Err() // caller cancellation wins
		}
		return ProbeResult{}, ErrRemoteProbeTimeout
	}
}

// PendingProbes reports outstanding correlated checks.
func (h *Hub) PendingProbes() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.probes)
}

func (h *Hub) dropProbe(id uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.probes, id)
}

// failProbesLocked completes and removes every matching pending probe. Caller holds h.mu.
func (h *Hub) failProbesLocked(match func(*pendingProbe) bool, err error) {
	for id, p := range h.probes {
		if match(p) {
			p.done <- probeOutcome{err: err}
			delete(h.probes, id)
		}
	}
}

func (h *Hub) closeProbes() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	h.failProbesLocked(func(*pendingProbe) bool { return true }, fmt.Errorf("%w: hub stopped", ErrRemoteProbeUnavailable))
}

// handleProbeResult fulfils only the exact pending request of this session
// with a sample taken after the request. Anything else is ignored.
func (h *Hub) handleProbeResult(ctx context.Context, s *Session, r *ProbeReply, now time.Time) error {
	if r == nil {
		return fmt.Errorf("%w: probe_result without payload", ErrMalformedMessage)
	}
	if err := validateSourceReports([]SourceReport{r.Source}); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	p, ok := h.probes[r.RequestID]
	if !ok || p.runner != s.RunnerID || p.serial != s.serial || p.kind != r.Source.Kind ||
		p.ref != r.Source.CredentialRef || p.revision != r.HealthRevision {
		return nil // unsolicited, duplicate, wrong source/credential/revision
	}
	obs := r.Source.Observation
	var out probeOutcome
	if obs == nil {
		out.err = fmt.Errorf("%w: runner reported %q", ErrRemoteProbeUnavailable, r.Source.UnavailableReason)
	} else {
		// Compare milliseconds before any Duration conversion: an untrusted
		// wire age must not wrap into a fresh sample.
		elapsed := now.Sub(p.sentAt)
		if r.Source.SampleAgeMs < 0 || elapsed < 0 || r.Source.SampleAgeMs > elapsed.Milliseconds() {
			return nil // sampled before the request: not an answer
		}
		res := ProbeResult{Auth: obs.Auth, HealthRevision: p.revision} // stamped from the pending entry
		if obs.Capacity != nil {
			c := *obs.Capacity
			res.Capacity = &c
		}
		out.res = res
		if m := h.observed[p.source]; m.serial != s.serial || r.Source.ObservationSeq > m.seq {
			h.observed[p.source] = observationMark{serial: s.serial, seq: r.Source.ObservationSeq}
		}
	}
	delete(h.probes, r.RequestID)
	p.done <- out
	return nil
}
