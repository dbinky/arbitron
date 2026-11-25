// domain/runner/hub_adopt.go
package runner

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
)

// ErrAdoptionUnsupported: a stream is adopted by Hub.Adopt (replay + live
// frames), never converted from one terminal result.
var ErrAdoptionUnsupported = errors.New("runner: this operation cannot be adopted from a single result")

// ErrJobAbsentOnReconnect: the owning runner reconnected and its hello did not
// list the job, and that report is durably recorded (decision D6). It always
// wraps ErrRunnerLost; JobAdopter takes the claim over with
// SupersedeReconnectAbsent evidence.
var ErrJobAbsentOnReconnect = errors.New("runner: reconnected runner no longer holds the job")

const maxRetainedResults = 1024

type adoptMsg struct {
	res RemoteResult
	err error
}

// AdoptionBook wakes an adoption wait when a result (or failure) for the
// exact fence arrives with no in-flight Hub.Run. It caches, it does not own:
// the durable RemoteInbox receipt is the authority. Keyed by the full fence,
// so a stale generation never satisfies a newer wait. Zero value is ready.
type AdoptionBook struct {
	mu       sync.Mutex
	waiters  map[Fence]chan adoptMsg
	retained map[Fence]adoptMsg
	lost     map[RunnerID]error
	order    []Fence
}

func (b *AdoptionBook) init() {
	if b.waiters == nil {
		b.waiters = map[Fence]chan adoptMsg{}
		b.retained = map[Fence]adoptMsg{}
		b.lost = map[RunnerID]error{}
	}
}

// Wait blocks until the job's result (or failure) is delivered, or ctx ends.
func (b *AdoptionBook) Wait(ctx context.Context, f Fence) (RemoteResult, error) {
	b.mu.Lock()
	b.init()
	if m, ok := b.retained[f]; ok {
		delete(b.retained, f)
		b.mu.Unlock()
		return m.res, m.err
	}
	if err := b.lost[f.RunnerID]; err != nil {
		b.mu.Unlock()
		return RemoteResult{}, err
	}
	ch := make(chan adoptMsg, 1)
	b.waiters[f] = ch
	b.mu.Unlock()
	select {
	case m := <-ch:
		return m.res, m.err
	case <-ctx.Done():
		b.mu.Lock()
		if b.waiters[f] == ch {
			delete(b.waiters, f)
		}
		b.mu.Unlock()
		select {
		case m := <-ch: // delivered while giving up: keep it for the next wait
			b.put(f, m)
		default:
		}
		return RemoteResult{}, ctx.Err()
	}
}

// Waiting reports whether an adoption wait is registered for f.
func (b *AdoptionBook) Waiting(f Fence) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.waiters[f]
	return ok
}

func (b *AdoptionBook) put(f Fence, m adoptMsg) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	if ch, ok := b.waiters[f]; ok {
		delete(b.waiters, f)
		ch <- m
		return
	}
	if _, ok := b.retained[f]; !ok {
		b.order = append(b.order, f)
	}
	b.retained[f] = m
	for len(b.retained) > maxRetainedResults && len(b.order) > 0 {
		delete(b.retained, b.order[0])
		b.order = b.order[1:]
	}
}

// forget drops a cached entry once the durable receipt answered the wait.
func (b *AdoptionBook) forget(f Fence) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.retained, f)
}

// Deliver wakes the waiter for a fence-checked, already-journaled result.
func (b *AdoptionBook) Deliver(f Fence, r RemoteResult) { b.put(f, adoptMsg{res: r}) }

// FailJob fails one job's adoption (the reconnecting runner no longer holds it).
func (b *AdoptionBook) FailJob(f Fence, err error) { b.put(f, adoptMsg{err: err}) }

// FailRunner releases every waiter for jobs on runner id (lost or revoked)
// and returns their fences: the waiting adopter owns their recovery, so the
// hub must not also fail them.
func (b *AdoptionBook) FailRunner(id RunnerID, err error) []Fence {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	b.lost[id] = err
	var released []Fence
	for f, ch := range b.waiters {
		if f.RunnerID == id {
			delete(b.waiters, f)
			ch <- adoptMsg{err: err}
			released = append(released, f)
		}
	}
	return released
}

// runnerReconnected clears the loss latch under the hub's admission lock.
// Wait and FailRunner share the book lock so loss cannot race waiter registration.
func (b *AdoptionBook) runnerReconnected(id RunnerID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.lost, id)
}

// AdoptedOutput converts an adopted terminal result exactly as Hub.Run does,
// refusing a payload that does not match the operation.
func AdoptedOutput(op OpType, res RemoteResult) (StepOutput, error) {
	switch op {
	case OpAgent, OpProcess, OpChat, OpConfirm:
	default:
		return StepOutput{}, fmt.Errorf("%w: %s", ErrAdoptionUnsupported, op)
	}
	if !res.OK {
		return StepOutput{}, remoteError(&res)
	}
	if (op == OpAgent && res.Agent == nil) || (op == OpProcess && res.Process == nil) ||
		(op == OpChat && res.Chat == nil) || (op == OpConfirm && res.Confirmation == nil) {
		return StepOutput{}, WithOutcome(fmt.Errorf("%w: adopted %s result carries no %s payload", ErrRemoteStepFailed, op, op),
			SourceOutcome{Class: OutcomeOther})
	}
	return remoteOutput(&res)
}

// Adopt waits for the outcome of a job this hub did not dispatch in this
// process lifetime. It never sends a dispatch. The ledger fence is checked
// first (h.receipt), then the durable receipt answers if it can.
func (h *Hub) Adopt(ctx context.Context, a Assignment, job StepJob) (StepOutput, error) {
	f := a.Fence
	if f.RunnerID == "" || f.RunnerID == BuiltInRunnerID {
		return StepOutput{}, fmt.Errorf("runner: hub cannot adopt a job on runner %q", f.RunnerID)
	}
	receipt, retained, err := h.receipt(ctx, f)
	if err != nil {
		return StepOutput{}, err
	}
	if job.Op == OpStream {
		return h.adoptStream(ctx, a)
	}
	if retained && receipt.Result != nil {
		h.adopt.forget(f)
		return AdoptedOutput(job.Op, *receipt.Result) // no provider repeat
	}
	res, err := h.adopt.Wait(ctx, f)
	if err != nil {
		return StepOutput{}, err
	}
	return AdoptedOutput(job.Op, res)
}

// adoptStream re-attaches an in-flight stream WITHOUT a dispatch: persisted
// frames replay first, then live frames continue after the persisted cursor
// through the normal handleJob/handleOutput path; the terminal result
// closes it via finish.
func (h *Hub) adoptStream(ctx context.Context, a Assignment) (StepOutput, error) {
	j := &remoteJob{ctx: ctx, a: a, offeredAt: h.now(), stream: make(chan engine.ChatChunk, streamBuffer),
		acceptedCh: make(chan struct{}), done: make(chan struct{})}
	h.mu.Lock()
	if h.revoked[a.Fence.RunnerID] {
		h.mu.Unlock()
		return StepOutput{}, WithOutcome(fmt.Errorf("%w: %s", ErrRunnerRevoked, a.Fence.RunnerID), SourceOutcome{Class: OutcomeRunnerLost})
	}
	j.emitMu.Lock()
	h.jobs[a.Fence.JobID] = j
	h.mu.Unlock()
	h.markAccepted(j)
	// Close the orphan-to-live handoff gap: a result may have been journaled
	// after Adopt's first read but before this waiter became visible. Results
	// arriving after registration finish this waiter through handleJob.
	receipt, retained, err := h.receipt(ctx, a.Fence)
	if err != nil {
		j.emitMu.Unlock()
		h.finish(j, nil, err)
		return StepOutput{}, err
	}
	frames, err := h.inbox.Outputs(ctx, a.Fence, 0)
	if err != nil {
		j.emitMu.Unlock()
		h.finish(j, nil, err)
		return StepOutput{}, err
	}
	go func() {
		last := receipt.LastOutputSeq
		for _, frame := range frames {
			select {
			case j.stream <- engine.ChatChunk{Token: frame.Token}:
			case <-ctx.Done():
			}
			if frame.Seq > last {
				last = frame.Seq
			}
		}
		h.mu.Lock()
		j.lastSeq = last
		h.mu.Unlock()
		j.emitMu.Unlock()
		if retained && receipt.Result != nil {
			h.finish(j, receipt.Result, nil)
		}
	}()
	return StepOutput{Stream: j.stream}, nil
}

// BeginRestartGrace stops Tick from declaring runners lost before until.
func (h *Hub) BeginRestartGrace(until time.Time) { h.restartGraceUntil.Store(until.UnixNano()) }

// InRestartGrace reports whether now is inside the restart grace window.
func (h *Hub) InRestartGrace(now time.Time) bool {
	u := h.restartGraceUntil.Load()
	return u != 0 && now.UnixNano() < u
}

// absentOnReconnect is the adoption failure for a ledger-live job that the
// reconnecting runner did not list (decision D6). When the inbox records the
// report durably, the error carries ErrJobAbsentOnReconnect and a run-scoped
// job.absent_on_reconnect event is logged; otherwise (no recorder, the fence
// moved on, or a storage error) it is a plain ErrRunnerLost and the adopter's
// takeover stays fail-closed.
func (h *Hub) absentOnReconnect(ctx context.Context, job Job, f Fence) error {
	base := fmt.Errorf("%w: %s no longer holds job %s", ErrRunnerLost, f.RunnerID, job.ID)
	rec, ok := h.inbox.(JobAbsenceRecorder)
	if !ok {
		return WithOutcome(base, SourceOutcome{Class: OutcomeRunnerLost})
	}
	if err := rec.RecordAbsent(ctx, f); err != nil {
		return WithOutcome(fmt.Errorf("%w (absence not recorded: %v)", base, err), SourceOutcome{Class: OutcomeRunnerLost})
	}
	run := job.Key.RunID
	h.events.Record(ctx, PoolEvent{ID: uuid.New(), At: h.now(), Kind: EventJobAbsentOnReconnect, RunnerID: f.RunnerID, SourceID: job.SourceID,
		RunID: &run, Cause: Cause{Kind: CauseSystem, Outcome: OutcomeRunnerLost},
		Detail: map[string]any{"jobId": job.ID.String(), "nodeId": job.NodeID, "generation": f.Generation}})
	return WithOutcome(fmt.Errorf("%w: %w", ErrJobAbsentOnReconnect, base), SourceOutcome{Class: OutcomeRunnerLost})
}
