package probe

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/workflow"
)

// HarnessConfirmationExecutor runs a subscription source's native exact-model
// check through the inner (undecorated) agent selector. It builds an agent-kind
// clone of the binding and calls the adapter's ProbeExecutedModel, which keeps
// the adapter's own tool restrictions, bounded execution and local
// account/model evidence. It never calls RunAgent.
type HarnessConfirmationExecutor struct{ inner engine.AgentProviderSelector }

var _ runner.ConfirmationExecutor = (*HarnessConfirmationExecutor)(nil)

// NewConfirmationExecutor panics on a nil or typed-nil selector: a half-wired
// verifier would silently refuse every recovery.
func NewConfirmationExecutor(inner engine.AgentProviderSelector) *HarnessConfirmationExecutor {
	if isNilDep(inner) {
		panic("probe: NewConfirmationExecutor requires the inner agent selector")
	}
	return &HarnessConfirmationExecutor{inner: inner}
}

func isNilDep(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Interface, reflect.Chan:
		return rv.IsNil()
	}
	return false
}

func (e *HarnessConfirmationExecutor) Confirm(ctx context.Context, b provider.ResolvedBinding, workDir string) (runner.ConfirmationResult, error) {
	switch {
	case workDir == "":
		return runner.ConfirmationResult{}, errors.New("probe: confirmation needs a scratch work dir")
	case b.Model == "":
		return runner.ConfirmationResult{}, fmt.Errorf("%w: no requested model", runner.ErrConfirmationModelMismatch)
	case b.ConnectionRef != "" || provider.IsLunaRoute(b.Provider):
		return runner.ConfirmationResult{}, fmt.Errorf("%w: only subscription sources confirm natively", runner.ErrConfirmationUnsupported)
	case b.Backend != workflow.BackendClaudeCode && b.Backend != workflow.BackendCodex:
		return runner.ConfirmationResult{}, fmt.Errorf("%w: backend %q", runner.ErrConfirmationUnsupported, b.Backend)
	}
	agent := b
	agent.Kind = provider.ProfileKindAgent
	agent.AllowedTools = nil
	agent.Pinned, agent.OpaquePinRoute = false, ""
	p, err := e.inner.AgentProviderFor(agent)
	if err != nil {
		return runner.ConfirmationResult{}, err
	}
	prober, ok := p.(engine.ExecutedModelProber)
	if !ok {
		return runner.ConfirmationResult{}, fmt.Errorf("%w: backend %q has no exact-model probe", runner.ErrConfirmationUnsupported, b.Backend)
	}
	executed, session, err := prober.ProbeExecutedModel(ctx, runner.ConfirmationPrompt, workDir)
	if err != nil {
		return runner.ConfirmationResult{}, err // WithOutcome attachments pass through unchanged
	}
	res := runner.ConfirmationResult{RequestedModel: b.Model, ExecutedModel: executed, SessionID: session}
	if err := runner.VerifyConfirmation(b.Model, &res); err != nil {
		return runner.ConfirmationResult{}, err
	}
	return res, nil
}
