package runner

import (
	"context"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/engine"
)

// ProbeCompletion is the evidence of one synthetic OperationProbe (quota
// confirmation): the model it asked for, the model the provider reports having
// executed, and whether that evidence was validated.
type ProbeCompletion struct {
	RequestedModel, ExecutedModel string
	Validated                     bool
}

// ErrInvalidProbeCompletion: not validated exact-model evidence, or not a
// synthetic probe job. The caller takes the ordinary failed-job branch.
var ErrInvalidProbeCompletion = errors.New("runner: probe completion is not validated exact-model evidence")

// Validate requires validated evidence that the requested model executed.
func (p ProbeCompletion) Validate() error {
	if !p.Validated || p.RequestedModel == "" || p.ExecutedModel != p.RequestedModel {
		return fmt.Errorf("%w: requested %q, executed %q, validated %t", ErrInvalidProbeCompletion, p.RequestedModel, p.ExecutedModel, p.Validated)
	}
	return nil
}

// ProbeCommitter is the explicit completion owner of OperationProbe jobs. A
// probe has no node continuation and no workspace; routing may bypass the node
// completion sink ONLY for this operation kind and this owner (P3 T6/T13).
type ProbeCommitter interface {
	CommitProbe(ctx context.Context, f engine.JobFenceRef, result ProbeCompletion) error
}
