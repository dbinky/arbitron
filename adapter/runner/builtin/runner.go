// Package builtin is the in-process StepRunner for runner.BuiltInRunnerID. It
// executes placed jobs with the UNDECORATED selectors (so it never re-enters
// the dispatcher) and the local process runner.
package builtin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
)

// ErrMalformedJob is returned for a StepJob whose payload does not match its op.
var ErrMalformedJob = errors.New("builtin runner: malformed step job")

// Deps are the inner (undecorated) execution paths.
type Deps struct {
	Chats   engine.ChatProviderSelector
	Agents  engine.AgentProviderSelector
	Streams engine.InteractiveStreamSelector
	Process engine.ProcessRunner
	// Sessions holds this machine's session adapters for session import (P4 T8).
	// Export runs inside the originating stream assignment (contracts §24).
	Sessions      *runner.SessionPortabilityRegistry
	Confirmations runner.ConfirmationExecutor
}

// Runner executes jobs placed on the built-in runner.
type Runner struct{ d Deps }

var _ runner.StepRunner = (*Runner)(nil)

// New builds the built-in runner. Any nil dependency panics: a half-wired
// built-in runner would silently fail every placed step.
func New(d Deps) *Runner {
	if d.Chats == nil || d.Agents == nil || d.Streams == nil || d.Process == nil || isNilDep(d.Confirmations) {
		panic("builtin: New requires Chats, Agents, Streams, Process and Confirmations")
	}
	return &Runner{d: d}
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

// Run executes one placed job. Adapter errors are returned unchanged so the
// outcome attached with runner.WithOutcome reaches the dispatcher's classifier.
func (r *Runner) Run(ctx context.Context, _ runner.Assignment, job runner.StepJob) (runner.StepOutput, error) {
	switch job.Op {
	case runner.OpChat:
		if job.Chat == nil {
			return runner.StepOutput{}, fmt.Errorf("%w: chat op without request", ErrMalformedJob)
		}
		p, err := r.d.Chats.ChatProviderFor(job.Binding)
		if err != nil {
			return runner.StepOutput{}, err
		}
		resp, err := p.Chat(ctx, *job.Chat)
		return runner.StepOutput{Chat: resp}, err
	case runner.OpStream:
		if job.Chat == nil {
			return runner.StepOutput{}, fmt.Errorf("%w: stream op without request", ErrMalformedJob)
		}
		p, err := r.streamProvider(job.Binding)
		if err != nil {
			return runner.StepOutput{}, err
		}
		ch, err := p.StreamChat(ctx, *job.Chat)
		return runner.StepOutput{Stream: ch}, err
	case runner.OpAgent:
		if job.Agent == nil {
			return runner.StepOutput{}, fmt.Errorf("%w: agent op without mission", ErrMalformedJob)
		}
		p, err := r.d.Agents.AgentProviderFor(job.Binding)
		if err != nil {
			return runner.StepOutput{}, err
		}
		res, err := p.RunAgent(ctx, *job.Agent)
		return runner.StepOutput{Agent: res}, err
	case runner.OpProcess:
		if job.Process == nil {
			return runner.StepOutput{}, fmt.Errorf("%w: process op without spec", ErrMalformedJob)
		}
		res, err := r.d.Process.RunProcess(ctx, *job.Process)
		return runner.StepOutput{Process: &res}, err
	case runner.OpConfirm:
		if job.Confirmation == nil || job.Confirmation.WorkDir == "" {
			return runner.StepOutput{}, fmt.Errorf("%w: confirm op without a scratch request", ErrMalformedJob)
		}
		if err := os.MkdirAll(job.Confirmation.WorkDir, 0o700); err != nil {
			return runner.StepOutput{}, fmt.Errorf("builtin runner: confirmation scratch: %w", err)
		}
		dir, err := os.MkdirTemp(job.Confirmation.WorkDir, "confirm-")
		if err != nil {
			return runner.StepOutput{}, fmt.Errorf("builtin runner: confirmation scratch: %w", err)
		}
		defer func() { _ = os.RemoveAll(dir) }()
		res, err := r.d.Confirmations.Confirm(ctx, job.Binding, dir)
		if err != nil {
			return runner.StepOutput{}, err
		}
		return runner.StepOutput{Confirmation: &res}, nil
	default:
		return runner.StepOutput{}, fmt.Errorf("%w: unknown op %q", ErrMalformedJob, job.Op)
	}
}

// streamProvider mirrors the interactive selector's split: agent-kind bindings
// use the tool-enabled interactive stream; every other kind uses the chat
// selector's tool-suppressed stream.
func (r *Runner) streamProvider(b provider.ResolvedBinding) (engine.ChatProvider, error) {
	if b.Kind == provider.ProfileKindAgent {
		return r.d.Streams.StreamProviderFor(b)
	}
	return r.d.Chats.ChatProviderFor(b)
}
