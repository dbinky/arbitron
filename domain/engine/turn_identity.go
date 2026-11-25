package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// TurnOperationMatches reports whether a job is the accepted turn or one of its
// sub-operations. A turn/1 prefix never accepts turn/10.
func TurnOperationMatches(jobOp, intentOp string) bool {
	return intentOp != "" && (jobOp == intentOp || strings.HasPrefix(jobOp, intentOp+"/"))
}

// InteractiveTurnIdentity names one accepted interactive turn. It is stamped
// at acceptance from the persisted intent and held lease, committed in the
// conversation by P4 T6 and carried on every interactive event (P4 T8/T12).
type InteractiveTurnIdentity struct {
	RunID        uuid.UUID `json:"instanceId"`
	NodeID       string    `json:"nodeId"`
	InvocationID uuid.UUID `json:"invocationId"`
	RequestID    uuid.UUID `json:"requestId"`
	TurnIndex    int       `json:"turnIndex"`
	Generation   int64     `json:"generation"`
}

// ConversationKeyTurnIdentity is the LLMConversation key of the committed turn identity.
const ConversationKeyTurnIdentity = "turnIdentity"

// ErrInvalidTurnIdentity rejects an identity missing a required field.
var ErrInvalidTurnIdentity = errors.New("engine: interactive turn identity is incomplete")

// Validate requires every field a client needs to filter superseded frames.
func (t InteractiveTurnIdentity) Validate() error {
	switch {
	case t.RunID == uuid.Nil:
		return fmt.Errorf("%w: run", ErrInvalidTurnIdentity)
	case t.NodeID == "":
		return fmt.Errorf("%w: node", ErrInvalidTurnIdentity)
	case t.InvocationID == uuid.Nil:
		return fmt.Errorf("%w: invocation", ErrInvalidTurnIdentity)
	case t.RequestID == uuid.Nil:
		return fmt.Errorf("%w: request", ErrInvalidTurnIdentity)
	case t.TurnIndex < 0:
		return fmt.Errorf("%w: turn index %d", ErrInvalidTurnIdentity, t.TurnIndex)
	case t.Generation < 1:
		return fmt.Errorf("%w: generation %d", ErrInvalidTurnIdentity, t.Generation)
	}
	return nil
}

// TurnIdentityFromIntent stamps the identity from the exact persisted intent;
// its SessionGeneration is the held lease generation at acceptance.
func TurnIdentityFromIntent(in TurnIntent) InteractiveTurnIdentity {
	return InteractiveTurnIdentity{RunID: in.RunID, NodeID: in.NodeID, InvocationID: in.InvocationID,
		RequestID: in.RequestID, TurnIndex: in.TurnIndex, Generation: in.SessionGeneration}
}

type turnIdentityCtxKey struct{}

// ContextWithTurnIdentity carries the accepted turn identity to emitters and the commit.
func ContextWithTurnIdentity(ctx context.Context, t InteractiveTurnIdentity) context.Context {
	return context.WithValue(ctx, turnIdentityCtxKey{}, t)
}

// TurnIdentityFromContext returns the identity stamped at acceptance.
func TurnIdentityFromContext(ctx context.Context) (InteractiveTurnIdentity, bool) {
	t, ok := ctx.Value(turnIdentityCtxKey{}).(InteractiveTurnIdentity)
	return t, ok
}

// CommittedTurnIdentity reads the identity the turn commit wrote into conv.
// It accepts the in-memory struct or its decoded JSON map; incomplete or
// malformed values report false.
func CommittedTurnIdentity(conv map[string]any) (InteractiveTurnIdentity, bool) {
	v, ok := conv[ConversationKeyTurnIdentity]
	if !ok || v == nil {
		return InteractiveTurnIdentity{}, false
	}
	var t InteractiveTurnIdentity
	switch x := v.(type) {
	case InteractiveTurnIdentity:
		t = x
	default:
		raw, err := json.Marshal(x)
		if err != nil || json.Unmarshal(raw, &t) != nil {
			return InteractiveTurnIdentity{}, false
		}
	}
	if t.Validate() != nil {
		return InteractiveTurnIdentity{}, false
	}
	return t, true
}
