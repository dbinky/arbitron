package runner

import (
	"context"

	"github.com/google/uuid"
)

type assignmentCtxKey struct{}

// ContextWithAssignment stamps the exact dispatcher assignment onto ctx. The
// built-in StepRunner (adapter/runner/builtin.Run) stamps its actual
// assignment before invoking any inner harness or process adapter. The
// supervised launch wrapper (ProcessSupervisor) reads it to register the durable
// launch permit against that exact job fence.
func ContextWithAssignment(ctx context.Context, a Assignment) context.Context {
	return context.WithValue(ctx, assignmentCtxKey{}, a)
}

// AssignmentFromContext returns the stamped assignment. An incomplete fence
// (nil job, empty runner, generation < 1), or a Job that names a different job
// than the Fence, is reported absent. A supervised launch then fails closed and
// never invents a job identity, reads a global "current job", or rebuilds a
// fence from trace history.
func AssignmentFromContext(ctx context.Context) (Assignment, bool) {
	a, ok := ctx.Value(assignmentCtxKey{}).(Assignment)
	if !ok || a.Fence.JobID == uuid.Nil || a.Fence.RunnerID == "" || a.Fence.Generation < 1 {
		return Assignment{}, false
	}
	if a.Job.ID != uuid.Nil && a.Job.ID != a.Fence.JobID {
		return Assignment{}, false
	}
	return a, true
}
