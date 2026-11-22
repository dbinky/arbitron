package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// OperationKind classifies the durable operation a dispatched provider or
// process call belongs to (Remote Runners RR-01).
type OperationKind string

const (
	OperationNode       OperationKind = "node"
	OperationChildInput OperationKind = "child_input"
	OperationTurn       OperationKind = "turn"
	OperationResolution OperationKind = "resolution"
	OperationProbe      OperationKind = "probe"
)

// StepIdentity is the durable identity of one dispatched operation. The
// runner ledger keys a job on (RunID, InvocationID, OperationID, Attempt).
type StepIdentity struct {
	RunID        uuid.UUID
	NodeID       string
	InvocationID uuid.UUID // one per node VISIT (back-edge => new); reused across restart
	OperationID  string    // e.g. "node", "node/child-input", "turn/<n>", "resolution/<attempt id>"
	Kind         OperationKind
	Attempt      int // workflow retry attempt (1-based); resets every visit
}

// Valid reports whether every key component is present. The dispatcher
// refuses an invalid identity exactly like a missing one.
func (s StepIdentity) Valid() bool {
	return s.RunID != uuid.Nil && s.NodeID != "" && s.InvocationID != uuid.Nil && s.OperationID != "" && s.Attempt >= 1
}

// ErrMissingStepIdentity is returned by the dispatcher when a routed call has
// no (valid) identity on its context: there is never a silent unplaced run.
var ErrMissingStepIdentity = errors.New("engine: dispatched operation has no step identity")

type stepIdentityKey struct{}

// ContextWithStepIdentity attaches id to ctx (precedent: ContextWithInstanceID).
func ContextWithStepIdentity(ctx context.Context, id StepIdentity) context.Context {
	return context.WithValue(ctx, stepIdentityKey{}, id)
}

// StepIdentityFromContext returns the identity stamped on ctx.
func StepIdentityFromContext(ctx context.Context) (StepIdentity, bool) {
	id, ok := ctx.Value(stepIdentityKey{}).(StepIdentity)
	return id, ok
}

// WithSubOperation derives a child operation under the same invocation:
// OperationID = parent.OperationID + "/" + suffix. Without a parent identity
// ctx is returned unchanged, so a dispatch from it still fails closed.
func WithSubOperation(ctx context.Context, suffix string, kind OperationKind) context.Context {
	parent, ok := StepIdentityFromContext(ctx)
	if !ok {
		return ctx
	}
	child := parent
	child.OperationID = parent.OperationID + "/" + suffix
	child.Kind = kind
	return ContextWithStepIdentity(ctx, child)
}

// nodeOperationID is the operation id of a node's own execution attempt.
const nodeOperationID = "node"

// turnOperationID names the n-th user turn of an interactive session.
func turnOperationID(n int) string { return fmt.Sprintf("turn/%d", n) }

// resolutionOperationID names one group conflict-resolution attempt.
func resolutionOperationID(groupRunID uuid.UUID, mergeIndex, attempt int) string {
	return fmt.Sprintf("resolution/%s:%d:%d", groupRunID, mergeIndex, attempt)
}
