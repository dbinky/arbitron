// Package remote is the server-side StepRunner for remote runners. It hands
// hermetic work to the hub, which offers it over the runner channel, and
// guarantees that no server-only binding field or server path reaches that
// path.
package remote

import (
	"context"
	"fmt"

	"github.com/arbitron/arbitron/domain/runner"
)

// hubRunner is the slice of *runner.Hub this adapter drives.
type hubRunner interface {
	Run(ctx context.Context, a runner.Assignment, job runner.StepJob) (runner.StepOutput, error)
}

// Runner executes assignments placed on remote runners.
type Runner struct {
	hub hubRunner
}

// New returns the remote StepRunner over h. It panics on a nil hub.
func New(h *runner.Hub) *Runner {
	if h == nil {
		panic("remote: New requires a hub")
	}
	return &Runner{hub: h}
}

// Run implements runner.StepRunner. Built-in or unassigned assignments are a
// placement bug and fail as OutcomeOther without touching the hub.
func (r *Runner) Run(ctx context.Context, a runner.Assignment, job runner.StepJob) (runner.StepOutput, error) {
	if a.Fence.RunnerID == "" || a.Fence.RunnerID == runner.BuiltInRunnerID {
		return runner.StepOutput{}, runner.WithOutcome(
			fmt.Errorf("remote: assignment %s is for runner %q, not a remote runner", a.Job.ID, a.Fence.RunnerID),
			runner.SourceOutcome{Class: runner.OutcomeOther})
	}
	return r.hub.Run(ctx, a, hermetic(job))
}

// hermetic returns a copy of job without the server-only fields: the key
// closure, gateway URL and extras (the runner uses its own credentials) and
// the server work dir (the runner uses a per-job scratch dir). The caller's
// job is untouched because a re-placed built-in attempt still needs them.
func hermetic(job runner.StepJob) runner.StepJob {
	out := job
	out.Binding.RevealKey = nil
	out.Binding.BaseURL = ""
	out.Binding.Extras = nil
	if job.Chat != nil {
		c := *job.Chat
		c.WorkDir = ""
		out.Chat = &c
	}
	return out
}
