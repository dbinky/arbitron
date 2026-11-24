// domain/runner/hub_run.go
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

const streamBuffer = 64

// Run offers one reserved job generation to its remote runner and waits for
// the outcome. Chat returns after the terminal result; stream returns the
// token channel once the runner accepts. Errors carry outcomes (WithOutcome):
// runner loss and offer expiry are OutcomeRunnerLost (the dispatcher re-places
// without spending a retry); unsupported work and rejections are OutcomeOther.
func (h *Hub) Run(ctx context.Context, a Assignment, job StepJob) (StepOutput, error) {
	id := a.Fence.RunnerID
	if id == "" || id == BuiltInRunnerID {
		return StepOutput{}, fmt.Errorf("runner: hub cannot execute on runner %q", id)
	}
	receipt, retained, err := h.receipt(ctx, a.Fence)
	if err != nil {
		return StepOutput{}, err
	}
	if retained && receipt.Result != nil && job.Op == OpChat {
		return remoteOutput(receipt.Result)
	}
	now := h.now()
	d, err := buildDispatch(a, job, now.Add(h.cfg.LostAfter))
	if err != nil {
		return StepOutput{}, WithOutcome(err, SourceOutcome{Class: OutcomeOther})
	}
	j := &remoteJob{ctx: ctx, a: a, dispatch: d, offeredAt: now, acceptedCh: make(chan struct{}), done: make(chan struct{})}
	if d.Op == OpStream {
		j.stream = make(chan engine.ChatChunk, streamBuffer)
	}
	h.mu.Lock()
	if h.revoked[id] {
		h.mu.Unlock()
		return StepOutput{}, WithOutcome(fmt.Errorf("%w: %s", ErrRunnerRevoked, id), SourceOutcome{Class: OutcomeRunnerLost})
	}
	if retained {
		j.emitMu.Lock()
	}
	h.jobs[a.Fence.JobID] = j
	s := h.sessions[id]
	h.mu.Unlock()
	if retained {
		h.markAccepted(j)
		frames, err := h.inbox.Outputs(ctx, a.Fence, 0)
		if err != nil {
			j.emitMu.Unlock()
			h.finish(j, nil, err)
			return StepOutput{}, err
		}
		go func() {
			for _, frame := range frames {
				if j.stream != nil {
					select {
					case j.stream <- engine.ChatChunk{Token: frame.Token}:
					case <-ctx.Done():
					}
				}
			}
			h.mu.Lock()
			j.lastSeq = receipt.LastOutputSeq
			h.mu.Unlock()
			j.emitMu.Unlock()
			if receipt.Result != nil {
				h.finish(j, receipt.Result, nil)
			}
		}()
	} else {
		h.send(ctx, s, dispatchMsg(j))
	}

	select {
	case <-j.acceptedCh:
	case <-j.done:
	case <-ctx.Done():
		h.abandon(ctx, j, ctx.Err())
		return StepOutput{}, ctx.Err()
	}
	if j.stream != nil {
		h.mu.Lock()
		accepted := j.accepted
		h.mu.Unlock()
		if accepted {
			return StepOutput{Stream: j.stream}, nil
		}
		return h.jobOutput(j)
	}
	select {
	case <-j.done:
	case <-ctx.Done():
		h.abandon(ctx, j, ctx.Err())
		return StepOutput{}, ctx.Err()
	}
	return h.jobOutput(j)
}

func dispatchMsg(j *remoteJob) Outbound {
	d := j.dispatch
	return Outbound{Kind: MsgDispatch, JobID: j.a.Fence.JobID, Generation: j.a.Fence.Generation, Dispatch: &d}
}

// buildDispatch builds the hermetic wire request. It never calls
// Binding.RevealKey and never copies BaseURL, ConnectionRef or WorkDir.
func buildDispatch(a Assignment, job StepJob, leaseUntil time.Time) (DispatchPayload, error) {
	if job.Op != OpChat && job.Op != OpStream {
		return DispatchPayload{}, fmt.Errorf("%w: %s", ErrRemoteOpUnsupported, job.Op)
	}
	if job.Chat == nil {
		return DispatchPayload{}, fmt.Errorf("%w: %s job without a chat request", ErrRemoteOpUnsupported, job.Op)
	}
	kind := a.Job.Group.Kind
	if !remoteKind(kind) {
		return DispatchPayload{}, fmt.Errorf("%w: %s", ErrRemoteCredential, kind)
	}
	fp, err := EndpointFingerprint(job.Binding)
	if err != nil {
		return DispatchPayload{}, fmt.Errorf("%w: %v", ErrRemoteCredential, err)
	}
	c := job.Chat
	var schema json.RawMessage
	if c.OutputSchema != nil {
		b, err := json.Marshal(c.OutputSchema)
		if err != nil {
			return DispatchPayload{}, fmt.Errorf("runner: encode output schema: %w", err)
		}
		schema = b
	}
	tools := c.ToolScope
	if len(job.Binding.AllowedTools) > 0 {
		tools = job.Binding.AllowedTools
	}
	if c.SuppressTools {
		tools = nil
	}
	model := c.Model
	if model == "" {
		model = job.Binding.Model
	}
	return DispatchPayload{Op: job.Op, LeaseUntil: leaseUntil, Chat: &ChatDispatch{
		Provider: provider.Canonicalize(job.Binding.Provider), EndpointFingerprint: fp,
		Model: model, SystemPrompt: c.SystemPrompt, Prompt: c.Prompt, MaxTokens: c.MaxTokens, Temperature: c.Temperature,
		OutputSchema: schema, SessionID: c.SessionID, RequireExecutedModel: c.RequireExecutedModel || job.Binding.RequiresModelEvidence(), Stream: job.Op == OpStream,
		CredentialKind: kind, Backend: job.Binding.Backend, ContextWindow: string(job.Binding.ContextWindow),
		Effort: string(job.Binding.Effort), Tools: append([]string(nil), tools...),
	}}, nil
}

func (h *Hub) jobOutput(j *remoteJob) (StepOutput, error) {
	h.mu.Lock()
	res, err := j.result, j.err
	h.mu.Unlock()
	if err != nil {
		return StepOutput{}, err
	}
	return remoteOutput(res)
}

func remoteOutput(res *RemoteResult) (StepOutput, error) {
	if res == nil {
		return StepOutput{}, WithOutcome(fmt.Errorf("%w: empty result", ErrRemoteStepFailed), SourceOutcome{Class: OutcomeOther})
	}
	if !res.OK {
		return StepOutput{}, remoteError(res)
	}
	if res.Chat == nil {
		return StepOutput{}, WithOutcome(fmt.Errorf("%w: result carries no chat payload", ErrRemoteStepFailed), SourceOutcome{Class: OutcomeOther})
	}
	resp := &engine.ChatResponse{Output: res.Chat.Output, Model: res.Chat.Model, TokensIn: res.Chat.TokensIn,
		TokensOut: res.Chat.TokensOut, SessionID: res.Chat.SessionID}
	if len(res.Chat.Conversation) > 0 {
		var conv any
		if json.Unmarshal(res.Chat.Conversation, &conv) == nil {
			resp.Conversation = conv
		}
	}
	return StepOutput{Chat: resp}, nil
}

// remoteError maps a failed result. A runner may report only source outcomes
// (exhausted, logged_out, transient, other); anything else is other.
func remoteError(res *RemoteResult) error {
	o := res.Outcome
	switch o.Class {
	case OutcomeExhausted, OutcomeLoggedOut, OutcomeTransient, OutcomeOther:
	default:
		o.Class = OutcomeOther
	}
	msg := res.ErrorText
	if msg == "" {
		msg = "no detail"
	}
	return WithOutcome(fmt.Errorf("%w: %s", ErrRemoteStepFailed, msg), o)
}

func terminalChunk(res *RemoteResult, err error) engine.ChatChunk {
	if err != nil {
		return engine.ChatChunk{Done: true, Error: err}
	}
	if res == nil || !res.OK {
		if res == nil {
			return engine.ChatChunk{Done: true, Error: WithOutcome(fmt.Errorf("%w: empty result", ErrRemoteStepFailed), SourceOutcome{Class: OutcomeOther})}
		}
		return engine.ChatChunk{Done: true, Error: remoteError(res)}
	}
	c := engine.ChatChunk{Done: true}
	if res.Chat != nil {
		c.TokensIn, c.TokensOut, c.SessionID = res.Chat.TokensIn, res.Chat.TokensOut, res.Chat.SessionID
	}
	return c
}

func (h *Hub) markAccepted(j *remoteJob) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !j.accepted {
		j.accepted = true
		close(j.acceptedCh)
	}
}

// finish resolves a waiter exactly once. For an accepted stream it emits the
// terminal chunk and closes the channel under emitMu.
func (h *Hub) finish(j *remoteJob, res *RemoteResult, err error) bool {
	h.mu.Lock()
	if j.finished {
		h.mu.Unlock()
		return false
	}
	j.finished, j.result, j.err = true, res, err
	if h.jobs[j.a.Fence.JobID] == j {
		delete(h.jobs, j.a.Fence.JobID)
	}
	accepted := j.accepted
	h.mu.Unlock()
	if j.stream != nil && accepted {
		j.emitMu.Lock()
		select {
		case j.stream <- terminalChunk(res, err):
		case <-j.ctx.Done():
		}
		close(j.stream)
		j.emitMu.Unlock()
	}
	close(j.done)
	return true
}

func (h *Hub) abandon(ctx context.Context, j *remoteJob, cause error) {
	if h.finish(j, nil, cause) {
		h.send(context.WithoutCancel(ctx), h.session(j.a.Fence.RunnerID),
			Outbound{Kind: MsgCancel, JobID: j.a.Fence.JobID, Generation: j.a.Fence.Generation})
	}
}

// liveJob returns the unfinished waiter matching f exactly, or nil.
func (h *Hub) liveJob(f Fence) *remoteJob {
	h.mu.Lock()
	defer h.mu.Unlock()
	j := h.jobs[f.JobID]
	if j == nil || j.finished || j.a.Fence != f {
		return nil
	}
	return j
}

func (h *Hub) handleJob(ctx context.Context, s *Session, in Inbound, now time.Time) error {
	if in.JobID == uuid.Nil || in.Generation < 1 {
		return fmt.Errorf("%w: %s without job id or generation", ErrMalformedMessage, in.Kind)
	}
	f := Fence{JobID: in.JobID, RunnerID: s.RunnerID, Generation: in.Generation}
	j := h.liveJob(f)
	if j == nil {
		return h.handleOrphan(ctx, s, in, f)
	}
	switch in.Kind {
	case MsgAccept:
		h.markAccepted(j)
		if err := h.ledger.ExtendLease(ctx, f, now.Add(h.cfg.LostAfter)); err != nil {
			return h.fenceLive(ctx, s, j, err)
		}
		return nil
	case MsgReject:
		h.finish(j, nil, WithOutcome(fmt.Errorf("%w: %s", ErrJobRejected, in.RejectReason), SourceOutcome{Class: OutcomeOther}))
		return nil
	case MsgOutput:
		if in.Output == nil {
			return fmt.Errorf("%w: output without payload", ErrMalformedMessage)
		}
		return h.handleOutput(ctx, s, j, *in.Output)
	default: // MsgResult
		if in.Result == nil {
			return fmt.Errorf("%w: result without payload", ErrMalformedMessage)
		}
		if err := h.ledger.FenceCheck(ctx, f); err != nil {
			return h.fenceLive(ctx, s, j, err)
		}
		if err := h.inbox.AcceptResult(ctx, f, *in.Result); err != nil {
			return h.fenceLive(ctx, s, j, err)
		}
		h.markAccepted(j)
		h.send(ctx, s, Outbound{Kind: MsgAck, JobID: f.JobID, Generation: f.Generation, Ack: &AckPayload{Result: true}})
		h.finish(j, in.Result, nil)
		return nil
	}
}

// fenceLive handles a ledger fence failure for a live waiter: the runner is
// told to stop and the step fails (its generation is no longer current).
func (h *Hub) fenceLive(ctx context.Context, s *Session, j *remoteJob, err error) error {
	if !errors.Is(err, ErrFenced) {
		return err
	}
	h.send(ctx, s, Outbound{Kind: MsgFenced, JobID: j.a.Fence.JobID, Generation: j.a.Fence.Generation})
	h.finish(j, nil, WithOutcome(err, SourceOutcome{Class: OutcomeOther}))
	return nil
}

// handleOutput delivers in-order frames exactly once; duplicates and gaps are
// answered with the last contiguous sequence so the runner resends from there.
func (h *Hub) handleOutput(ctx context.Context, s *Session, j *remoteJob, f OutputFrame) error {
	ack, err := h.inbox.AcceptOutput(ctx, j.a.Fence, f)
	if err != nil {
		return h.fenceLive(ctx, s, j, err)
	}
	h.markAccepted(j)
	// The ACK is a durable receipt, independent of downstream backpressure.
	h.send(ctx, s, Outbound{Kind: MsgAck, JobID: j.a.Fence.JobID, Generation: j.a.Fence.Generation, Ack: &AckPayload{Seq: ack}})
	j.emitMu.Lock()
	defer j.emitMu.Unlock()
	h.mu.Lock()
	after := j.lastSeq
	finished := j.finished
	h.mu.Unlock()
	if finished {
		return nil
	}
	frames, err := h.inbox.Outputs(ctx, j.a.Fence, after)
	if err != nil {
		return err
	}
	for _, frame := range frames {
		if j.stream != nil {
			select {
			case j.stream <- engine.ChatChunk{Token: frame.Token}:
			case <-j.ctx.Done():
				return j.ctx.Err()
			}
		}
		h.mu.Lock()
		j.lastSeq = frame.Seq
		h.mu.Unlock()
	}
	return nil
}

// receipt checks current ledger authority before using a retained receipt.
func (h *Hub) receipt(ctx context.Context, f Fence) (InboxReceipt, bool, error) {
	job, err := h.ledger.Get(ctx, f.JobID)
	if err != nil {
		return InboxReceipt{}, false, err
	}
	if job.Generation != f.Generation || job.RunnerID != f.RunnerID {
		return InboxReceipt{}, false, ErrFenced
	}
	return h.inbox.Receipt(ctx, f)
}

// hasTerminalReceipt consults durable receipt authority, not ledger state.
func (h *Hub) hasTerminalReceipt(ctx context.Context, f Fence) bool {
	receipt, ok, err := h.receipt(ctx, f)
	return err == nil && ok && receipt.Result != nil
}

// handleOrphan answers a message for a job generation this process is not
// waiting on. Current generations can still journal output/results after a
// restart; retained terminal replays are acknowledged. Other frames are fenced.
func (h *Hub) handleOrphan(ctx context.Context, s *Session, in Inbound, f Fence) error {
	if in.Kind == MsgResult && in.Result != nil {
		receipt, ok, err := h.inbox.Receipt(ctx, f)
		if err != nil {
			return err
		}
		if ok && receipt.Result != nil {
			if err := h.inbox.AcceptResult(ctx, f, *in.Result); err != nil {
				return err
			}
			h.send(ctx, s, Outbound{Kind: MsgAck, JobID: f.JobID, Generation: f.Generation, Ack: &AckPayload{Result: true}})
			return nil
		}
	}
	if h.ledger.FenceCheck(ctx, f) == nil {
		switch {
		case in.Kind == MsgOutput && in.Output != nil:
			if ack, err := h.inbox.AcceptOutput(ctx, f, *in.Output); err == nil {
				h.send(ctx, s, Outbound{Kind: MsgAck, JobID: f.JobID, Generation: f.Generation, Ack: &AckPayload{Seq: ack}})
				return nil
			}
		case in.Kind == MsgResult && in.Result != nil:
			if err := h.inbox.AcceptResult(ctx, f, *in.Result); err == nil {
				h.send(ctx, s, Outbound{Kind: MsgAck, JobID: f.JobID, Generation: f.Generation, Ack: &AckPayload{Result: true}})
				return nil
			}
		}
	}
	h.send(ctx, s, Outbound{Kind: MsgFenced, JobID: f.JobID, Generation: f.Generation})
	return nil
}

// refreshHeld extends leases of live held jobs and fences unknown ones.
func (h *Hub) refreshHeld(ctx context.Context, s *Session, held []HeldJobInfo, now time.Time) error {
	for _, hj := range held {
		f := Fence{JobID: hj.JobID, RunnerID: s.RunnerID, Generation: hj.Generation}
		j := h.liveJob(f)
		if j == nil {
			if hj.State == HeldTerminal && h.hasTerminalReceipt(ctx, f) {
				continue
			}
			if h.adoptable(ctx, hj, f) {
				if err := h.ledger.ExtendLease(ctx, f, now.Add(h.cfg.LostAfter)); err == nil {
					continue
				}
			}
			h.send(ctx, s, Outbound{Kind: MsgFenced, JobID: f.JobID, Generation: f.Generation})
			continue
		}
		if err := h.ledger.ExtendLease(ctx, f, now.Add(h.cfg.LostAfter)); err != nil {
			if ferr := h.fenceLive(ctx, s, j, err); ferr != nil {
				return ferr
			}
		}
	}
	return nil
}
