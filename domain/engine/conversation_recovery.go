package engine

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
)

// turnRun retains the accepted request and policy across attempts.
type turnRun struct {
	instanceID       uuid.UUID
	nodeID           string
	exec             *instance.NodeExecution
	binding          provider.ResolvedBinding
	provider         ChatProvider
	req              ChatRequest
	bus              EventBus
	hold             *turnLeaseHold
	message          string
	turnIndex        int
	requestID        uuid.UUID
	workDir          string
	resourceID       uuid.UUID
	restoreWorkspace bool
}

func (run *turnRun) identity(ctx context.Context) InteractiveTurnIdentity {
	id, _ := StepIdentityFromContext(ctx)
	return InteractiveTurnIdentity{RunID: run.instanceID, NodeID: run.nodeID, InvocationID: id.InvocationID,
		RequestID: run.requestID, TurnIndex: run.turnIndex, Generation: run.hold.Lease().Generation}
}

// runTurnWithRecovery only returns the successful attempt's output and receipt.
// The mover reuses the held token; capacity and budget waits leave the intent
// pending for the recovery worker.
func (h *conversationHandler) runTurnWithRecovery(ctx context.Context, run *turnRun) (string, string, error) {
	moves, fellBack := 0, false
	for {
		turn := run.identity(ctx)
		attemptCtx := ContextWithTurnIdentity(ContextWithTurnLease(ctx, run.hold.Lease()), turn)
		if moves > 0 || fellBack {
			attemptCtx = WithSubOperation(attemptCtx, fmt.Sprintf("g%d", turn.Generation), OperationTurn)
		}
		if fellBack {
			// The refused native attempt already owns g<generation> in the
			// ledger; a same-generation rebuild is a distinct stream job.
			attemptCtx = WithSubOperation(attemptCtx, "rebuild", OperationTurn)
		}
		attemptSink := NewStepCompletionSink()
		if _, ok := StepCompletionSinkFromContext(ctx); ok {
			attemptCtx = ContextWithStepCompletionSink(attemptCtx, attemptSink)
		}
		resp, sid, err := h.streamChatResponse(attemptCtx, run.instanceID, run.provider, run.req, run.bus)
		if err == nil {
			if outer, ok := StepCompletionSinkFromContext(ctx); ok {
				for _, c := range attemptSink.Completions() {
					if err := outer.Record(c); err != nil {
						return "", "", err
					}
				}
			}
			if sid == "" {
				sid = run.req.SessionID
			}
			return resp, sid, nil
		}
		if !fellBack && isProvisionalResumeRefusal(ctx, run.exec.LLMConversation, err) {
			fellBack = true
			if run.req, err = h.provisionalResumeFallback(ctx, run, turn, err); err != nil {
				return "", "", err
			}
			continue
		}
		if fellBack && !errors.Is(err, ErrSessionMoveRequired) && ctx.Err() == nil {
			return "", "", fmt.Errorf("%w: rebuilt session failed again: %w", ErrSessionRestoreFailed, err)
		}
		var interrupted *SessionMoveRequiredError
		if !errors.As(err, &interrupted) || h.mover == nil || ctx.Err() != nil {
			return "", "", err
		}
		if moves >= h.maxMoves {
			return "", "", fmt.Errorf("%w: %d moves in this pass: %w", ErrRecoveryBudgetExhausted, moves, err)
		}
		moves++
		// Import receipts belong to the move, not to the user's stream.
		moveCtx := ContextWithStepCompletionSink(ctx, NewStepCompletionSink())
		res, err := h.mover.MoveSession(moveCtx, run.hold.Lease(), SessionMoveRequest{
			RunID: run.instanceID, InvocationID: turn.InvocationID, ExecutionID: run.exec.ID, NodeID: run.nodeID,
			TurnIndex: run.turnIndex, Reason: interrupted.Reason, From: interrupted.From, Binding: run.binding,
			WorkDir: run.workDir, ResourceID: run.resourceID, RestoreWorkspace: run.restoreWorkspace, RequestID: run.requestID,
		})
		if err != nil {
			return "", "", err
		}
		lease := run.hold.Lease()
		lease.Generation = res.Generation
		run.hold.Adopt(lease)
		if run.hold.Lease().Generation != res.Generation {
			return "", "", fmt.Errorf("session move: reserved generation %d not adopted: %w", res.Generation, ErrTurnFenced)
		}
		// Mirror the mover's committed native-import marker for this turn's
		// first resume. The original pre-move read cannot contain that marker.
		conv := maps.Clone(run.exec.LLMConversation)
		conv[ConversationKeyProvisionalResume] = res.SessionID != "" && !res.PendingRebuild
		conv[ConversationKeyPendingRebuild] = res.PendingRebuild
		conv[ConversationKeySessionID] = res.SessionID
		run.exec.LLMConversation = conv
		run.req.Prompt, run.req.SessionID = run.message, res.SessionID
		if res.PendingRebuild {
			primer, err := BuildRebuildPrompt(run.exec.LLMConversation, run.message, RebuildLimitBytes(run.binding.ContextWindow))
			if err != nil {
				return "", "", fmt.Errorf("%w: %w", ErrSessionRestoreFailed, err)
			}
			run.req.Prompt, run.req.SessionID = primer, ""
		}
		restarted := run.identity(ctx)
		PublishTurnRestarted(run.bus, restarted)
		if res.Lossy {
			PublishContextNotice(run.bus, restarted, res.Notice)
		}
	}
}
