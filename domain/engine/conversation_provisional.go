package engine

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// CommittedWorkspaceRestorer restores the run's last committed workspace into
// dir. *runner.WorkspaceSync (P3 T6, RestoreBuiltIn) satisfies it.
type CommittedWorkspaceRestorer interface {
	RestoreBuiltIn(ctx context.Context, runID uuid.UUID, dir string) error
}

// SetConversationFencer overrides the generation-fenced conversation writer.
// Default: the node-execution repo when it implements the port.
func (h *conversationHandler) SetConversationFencer(f instance.SessionConversationFencer) {
	h.fencer = f
}

// SetWorkspaceRestorer installs the committed-workspace restorer that agent
// turns use in the provisional-resume fallback.
func (h *conversationHandler) SetWorkspaceRestorer(r CommittedWorkspaceRestorer) { h.restorer = r }

func (h *conversationHandler) conversationFencer() (instance.SessionConversationFencer, bool) {
	if h.fencer != nil {
		return h.fencer, true
	}
	f, ok := h.nodeExecs.(instance.SessionConversationFencer)
	return f, ok
}

// isProvisionalResumeRefusal: the attempt that just failed was the first
// resume of a provisionally imported native session (P4 T8 marker), and the
// failure is the provider/adapter refusing it, not an interruption, a
// cancellation or a fence.
func isProvisionalResumeRefusal(ctx context.Context, conv map[string]any, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	if p, _ := conv[ConversationKeyProvisionalResume].(bool); !p {
		return false
	}
	switch {
	case errors.Is(err, ErrSessionMoveRequired), errors.Is(err, ErrTurnLeaseLost), errors.Is(err, ErrTurnFenced),
		errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return false
	}
	return true
}

// provisionalResumeFallback is this task's OWN fenced fallback (decision
// unstick 2026-11-24; the mover stays move-only). It runs at most once per
// turn: patch at the held generation, restore the committed workspace for
// agent turns, rebuild prompt + transcript, and return the resend request.
func (h *conversationHandler) provisionalResumeFallback(ctx context.Context, run *turnRun, turn InteractiveTurnIdentity, cause error) (ChatRequest, error) {
	fencer, ok := h.conversationFencer()
	if !ok {
		return ChatRequest{}, fmt.Errorf("%w: no generation-fenced conversation writer: %w", ErrSessionRestoreFailed, cause)
	}
	patch := map[string]any{ConversationKeyProvisionalResume: nil, ConversationKeySessionID: nil, ConversationKeyPendingRebuild: true}
	if err := fencer.PatchSessionFieldsAtGeneration(ctx, run.exec.ID, turn.Generation, patch); err != nil {
		if errors.Is(err, instance.ErrStaleSessionGeneration) || errors.Is(err, instance.ErrSessionClosed) {
			return ChatRequest{}, fmt.Errorf("%w: provisional fallback at generation %d: %w", ErrTurnFenced, turn.Generation, err)
		}
		return ChatRequest{}, fmt.Errorf("%w: provisional fallback: %w", ErrSessionRestoreFailed, err)
	}
	// Mirror the committed patch in memory so the turn's final write cannot
	// resurrect the refused session.
	conv := maps.Clone(run.exec.LLMConversation)
	delete(conv, ConversationKeyProvisionalResume)
	delete(conv, ConversationKeySessionID)
	conv[ConversationKeyPendingRebuild] = true
	run.exec.LLMConversation = conv
	if run.restoreWorkspace {
		if h.restorer == nil {
			return ChatRequest{}, fmt.Errorf("%w: no committed-workspace restorer: %w", ErrSessionRestoreFailed, cause)
		}
		if err := h.restorer.RestoreBuiltIn(ctx, run.instanceID, run.workDir); err != nil {
			return ChatRequest{}, fmt.Errorf("%w: restore committed workspace: %w", ErrSessionRestoreFailed, err)
		}
	}
	primer, err := BuildRebuildPrompt(conv, run.message, RebuildLimitBytes(run.binding.ContextWindow))
	if err != nil {
		return ChatRequest{}, fmt.Errorf("%w: %w", ErrSessionRestoreFailed, err)
	}
	req := run.req
	req.Prompt, req.SessionID = primer, ""
	PublishContextNotice(run.bus, turn, SessionLossNotice)
	return req, nil
}
