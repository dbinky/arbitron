package runner

import (
	"context"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/provider"
)

// OpConfirm is the token-spending, source-bound native quota confirmation. It
// is ordinary slot-counted execution, never a read-only probe.
const OpConfirm OpType = "confirm"

// ConfirmationPrompt is the fixed bounded prompt every native verifier gets.
const ConfirmationPrompt = "Reply with OK."

// ConfirmationRequest is the server-side execution descriptor. WorkDir is the
// built-in runner's scratch parent; remote runners replace it with their own.
type ConfirmationRequest struct{ WorkDir string }

// ConfirmationResult is the native harness's own evidence for one turn.
type ConfirmationResult struct{ RequestedModel, ExecutedModel, SessionID string }

// ConfirmationExecutor runs one native exact-model check for binding in workDir.
type ConfirmationExecutor interface {
	Confirm(ctx context.Context, binding provider.ResolvedBinding, workDir string) (ConfirmationResult, error)
}

var (
	ErrConfirmationNoProfile     = errors.New("runner: no authorized profile for the confirmation model")
	ErrConfirmationModelMismatch = errors.New("runner: confirmation did not prove the exact model")
)

// VerifyConfirmation accepts only exact native evidence for want.
func VerifyConfirmation(want string, res *ConfirmationResult) error {
	switch {
	case res == nil:
		return fmt.Errorf("%w: no evidence", ErrConfirmationModelMismatch)
	case want == "" || res.RequestedModel != want || res.ExecutedModel != want:
		return fmt.Errorf("%w: requested %q executed %q", ErrConfirmationModelMismatch, res.RequestedModel, res.ExecutedModel)
	case res.SessionID == "":
		return fmt.Errorf("%w: no harness session", ErrConfirmationModelMismatch)
	}
	return nil
}
