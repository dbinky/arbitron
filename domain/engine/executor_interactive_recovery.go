package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// firstTurnResult is the successful first turn's stream outcome.
type firstTurnResult struct {
	response, sessionID string
	tokensIn, tokensOut int
}

// streamFirstTurnWithRecovery streams the first turn. A classified
// interruption has the mover fence the old owner and restore the workspace
// (FirstTurn: no session record, no import), adopts the reserved generation,
// and re-runs the WHOLE step with a fresh session and the same prompt/nonce,
// at most e.maxMoves times in this pass.
func (e *InteractiveExecutor) streamFirstTurnWithRecovery(ctx context.Context, node workflow.Node, instanceID uuid.UUID,
	p ChatProvider, req ChatRequest, binding provider.ResolvedBinding, workDir string, hold *turnLeaseHold, requestID uuid.UUID) (firstTurnResult, error) {
	identity := func() InteractiveTurnIdentity {
		inv := uuid.Nil
		if id, ok := StepIdentityFromContext(ctx); ok {
			inv = id.InvocationID
		}
		return InteractiveTurnIdentity{RunID: instanceID, NodeID: node.ID, InvocationID: inv, RequestID: requestID,
			TurnIndex: 1, Generation: hold.Lease().Generation}
	}
	moves := 0
	for {
		turn := identity()
		attemptCtx := ContextWithTurnIdentity(ContextWithTurnLease(ctx, hold.Lease()), turn)
		if moves > 0 {
			attemptCtx = WithSubOperation(attemptCtx, fmt.Sprintf("g%d", turn.Generation), OperationTurn)
		}
		attemptSink := NewStepCompletionSink() // fresh per dispatch
		if _, ok := StepCompletionSinkFromContext(ctx); ok {
			attemptCtx = ContextWithStepCompletionSink(attemptCtx, attemptSink)
		}

		res, err := e.streamFirstTurn(attemptCtx, node, instanceID, p, req, turn.Generation)
		if err == nil {
			if outer, ok := StepCompletionSinkFromContext(ctx); ok {
				for _, c := range attemptSink.Completions() {
					if rerr := outer.Record(c); rerr != nil {
						return firstTurnResult{}, rerr
					}
				}
			}
			return res, nil
		}
		var interrupted *SessionMoveRequiredError
		if !errors.As(err, &interrupted) || e.mover == nil || ctx.Err() != nil {
			return firstTurnResult{}, err
		}
		if moves >= e.maxMoves {
			return firstTurnResult{}, fmt.Errorf("interactive %q: %w: %d moves in this pass: %w",
				node.ID, ErrRecoveryBudgetExhausted, moves, err)
		}
		moves++
		moveCtx := ContextWithStepCompletionSink(ctx, NewStepCompletionSink())
		moved, merr := e.mover.MoveSession(moveCtx, hold.Lease(), SessionMoveRequest{
			RunID: instanceID, InvocationID: turn.InvocationID, NodeID: node.ID, TurnIndex: 1,
			Reason: interrupted.Reason, From: interrupted.From, Binding: binding, WorkDir: workDir,
			RestoreWorkspace: binding.Kind == provider.ProfileKindAgent, FirstTurn: true, RequestID: requestID,
		})
		if merr != nil {
			return firstTurnResult{}, merr // a SessionWaitError keeps the first-turn intent pending, see (c)
		}
		lease := hold.Lease()
		lease.Generation = moved.Generation // the ACTUAL reserved generation
		hold.Adopt(lease)
		if hold.Lease().Generation != moved.Generation {
			return firstTurnResult{}, fmt.Errorf("interactive %q: reserved generation %d not adopted: %w", node.ID, moved.Generation, ErrTurnFenced)
		}
		req.SessionID = "" // fresh session: no bundle exists before the first success; Prompt (and nonce) unchanged
		PublishTurnRestarted(e.eventBus, identity())
	}
}

// streamFirstTurn is the original stream call and consume loop (steps 7-8),
// unchanged except that every token carries the producing attempt's generation.
func (e *InteractiveExecutor) streamFirstTurn(ctx context.Context, node workflow.Node, instanceID uuid.UUID,
	p ChatProvider, req ChatRequest, generation int64) (firstTurnResult, error) {
	stream, err := p.StreamChat(ctx, req)
	if err != nil {
		return firstTurnResult{}, fmt.Errorf("interactive %q: stream call: %w", node.ID, err)
	}
	if stream == nil {
		return firstTurnResult{}, fmt.Errorf("interactive %q: stream closed without completion (nil stream)", node.ID)
	}
	var out firstTurnResult
	var responseBuilder strings.Builder
	sawDone := false
	for !sawDone {
		var chunk ChatChunk
		var open bool
		select {
		case <-ctx.Done():
			return firstTurnResult{}, fmt.Errorf("interactive %q: stream cancelled: %w", node.ID, ctx.Err())
		case chunk, open = <-stream:
		}
		// A buffered chunk or channel closure can win the select even after
		// cancellation. Never treat that as a successfully completed turn.
		if err := ctx.Err(); err != nil {
			return firstTurnResult{}, fmt.Errorf("interactive %q: stream cancelled: %w", node.ID, err)
		}
		if !open {
			break
		}
		if chunk.Error != nil {
			return firstTurnResult{}, fmt.Errorf("interactive %q: stream error: %w", node.ID, chunk.Error)
		}
		if chunk.SessionID != "" {
			out.sessionID = chunk.SessionID
		}
		if chunk.Token != "" {
			responseBuilder.WriteString(chunk.Token)
			turn, _ := TurnIdentityFromContext(ctx)
			if turn.Validate() == nil {
				publishSessionEvent(e.eventBus, turn, EventTypeInteractiveToken, map[string]any{"token": chunk.Token})
			} else {
				e.eventBus.Publish(instanceID, Event{Type: EventTypeInteractiveToken, Timestamp: time.Now(), Data: map[string]any{"token": chunk.Token, "generation": generation}})
			}
		}
		if chunk.Done {
			out.tokensIn, out.tokensOut, sawDone = chunk.TokensIn, chunk.TokensOut, true
		}
	}
	// Publishing the last token can itself race cancellation. Check again
	// before reporting success.
	if err := ctx.Err(); err != nil {
		return firstTurnResult{}, fmt.Errorf("interactive %q: stream cancelled: %w", node.ID, err)
	}
	// DATA INTEGRITY (PR #18): a stream that closed without a terminal Done
	// chunk was truncated; it is never a completed success.
	if !sawDone {
		return firstTurnResult{}, fmt.Errorf("interactive %q: stream closed without completion", node.ID)
	}
	out.response = responseBuilder.String()
	return out, nil
}
