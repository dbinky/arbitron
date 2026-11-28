package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
)

// Move reasons. A session moves ONLY for these (design P4 §3.4): never for
// load, cordon, suspect status, a full slot limit or transient congestion.
const (
	MoveReasonExhausted  = "exhausted"
	MoveReasonLoggedOut  = "logged_out"
	MoveReasonDrain      = "drain"
	MoveReasonRunnerLost = "runner_lost"
)

// AllowedMoveReason reports whether reason authorizes a session move.
func AllowedMoveReason(reason string) bool {
	switch reason {
	case MoveReasonExhausted, MoveReasonLoggedOut, MoveReasonDrain, MoveReasonRunnerLost:
		return true
	}
	return false
}

// SessionLossNotice is the persisted, user-visible notice for every lossy move.
const SessionLossNotice = "tool details and reasoning may be lost"

// ConversationKeyPendingRebuild marks a session whose native bundle could not be
// restored: the NEXT turn starts a fresh provider session whose prompt is
// BuildRebuildPrompt(conversation, message). Cleared by the next successful turn.
const ConversationKeyPendingRebuild = "pendingRebuild"

// ConversationKeyProvisionalResume marks a native import until its first
// actual resume succeeds or the turn's fenced fallback rebuilds it.
const ConversationKeyProvisionalResume = "provisionalResume"

// SessionMoveRequest mirrors runner.MoveRequest so domain/engine never imports
// domain/runner. Binding carries the exact accepted model/route/backend/tools.
type SessionMoveRequest struct {
	RunID, InvocationID, ExecutionID uuid.UUID
	NodeID                           string
	TurnIndex                        int
	Reason                           string
	From                             string
	Binding                          provider.ResolvedBinding
	WorkDir                          string
	ResourceID                       uuid.UUID
	RestoreWorkspace                 bool
	FirstTurn                        bool

	RequestID uuid.UUID
}

// SessionMoveResult mirrors runner.MoveResult. Generation is the session
// generation reserved by the move; the caller adopts it into its held lease.
type SessionMoveResult struct {
	To             string
	SessionID      string
	Lossy          bool
	Notice         string
	Generation     int64
	PendingRebuild bool
}

// SessionMover moves a pinned session while the caller HOLDS lease. It never
// acquires the lease itself (a mover called within a turn reuses the token).
type SessionMover interface {
	MoveSession(ctx context.Context, lease TurnLease, req SessionMoveRequest) (SessionMoveResult, error)
}

var (
	ErrSessionMoveNotAllowed = errors.New("engine: a session moves only for exhaustion, logout, drain or runner loss")
	ErrNoSessionTarget       = errors.New("engine: no eligible runner for the session's model group")
	ErrSessionRestoreFailed  = errors.New("engine: session restoration failed")
	ErrRebuildTooLarge       = errors.New("engine: recorded conversation exceeds the target context window; refusing to truncate")
)

// SessionWaitError is the visible wait a move reports when no same-group target
// is eligible (Kind "capacity") or every eligible target is full (Kind "slot").
type SessionWaitError struct {
	Kind      string
	Group     string
	Model     string
	NextReset *time.Time
}

func (e *SessionWaitError) Error() string {
	msg := fmt.Sprintf("session waiting for %s: group %s model %s", e.Kind, e.Group, e.Model)
	if e.NextReset != nil {
		msg += " next reset " + e.NextReset.UTC().Format(time.RFC3339)
	}
	return msg
}

// Is makes errors.Is(err, ErrNoSessionTarget) true.
func (e *SessionWaitError) Is(target error) bool { return target == ErrNoSessionTarget }

// RebuildLimitBytes bounds a transcript rebuild prompt for the binding's context
// window: tokens x 3 bytes/token x 0.8 (room for the reply). Never truncates.
func RebuildLimitBytes(cw provider.ContextWindow) int {
	if cw == provider.Context1M {
		return 2_400_000
	}
	return 480_000
}

const (
	rebuildTranscriptHeader = "The conversation so far, as recorded by Arbitron (earlier tool details and reasoning are not included):"
	rebuildContinueFooter   = "Continue this conversation from here. The user's next message follows."
)

// BuildRebuildPrompt renders the fresh-session primer: the ORIGINAL first prompt
// verbatim (it embeds the completion sentinel and its nonce), every later
// recorded message in order, then nextMessage. limitBytes <= 0 disables the
// bound. An oversized result is ErrRebuildTooLarge — never truncated.
func BuildRebuildPrompt(conv map[string]any, nextMessage string, limitBytes int) (string, error) {
	msgs := parseConversation(conv)
	if len(msgs) == 0 {
		return "", fmt.Errorf("%w: no recorded first prompt", ErrSessionRestoreFailed)
	}
	first, _ := msgs[0]["content"].(string)
	if strings.TrimSpace(first) == "" {
		return "", fmt.Errorf("%w: recorded first prompt is empty", ErrSessionRestoreFailed)
	}
	var b strings.Builder
	b.WriteString(first)
	if len(msgs) > 1 {
		b.WriteString("\n\n")
		b.WriteString(rebuildTranscriptHeader)
		b.WriteString("\n")
		for _, m := range msgs[1:] {
			role, _ := m["role"].(string)
			content, _ := m["content"].(string)
			fmt.Fprintf(&b, "\n[%s]\n%s\n", role, content)
		}
	}
	b.WriteString("\n")
	b.WriteString(rebuildContinueFooter)
	b.WriteString("\n\n")
	b.WriteString(nextMessage)
	if limitBytes > 0 && b.Len() > limitBytes {
		return "", fmt.Errorf("%w: %d bytes exceeds %d", ErrRebuildTooLarge, b.Len(), limitBytes)
	}
	return b.String(), nil
}
