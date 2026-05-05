package engine

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// instanceIDKey is the context key for passing instance ID to executors.
type instanceIDKey struct{}

// ContextWithInstanceID returns a context with the instance ID embedded.
func ContextWithInstanceID(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, instanceIDKey{}, id)
}

// InstanceIDFromContext extracts the instance ID from context.
func InstanceIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(instanceIDKey{}).(uuid.UUID)
	return id, ok
}

// TimerDelayExecutor handles timer_delay nodes by transitioning instances to
// the waiting state and scheduling a timer for re-queueing.
type TimerDelayExecutor struct {
	instances  instance.InstanceRepository
	nodeExecs  instance.NodeExecutionRepository
	scheduler  TimerScheduler
	queueMgr   QueueManager
}

// NewTimerDelayExecutor constructs a new TimerDelayExecutor.
func NewTimerDelayExecutor(
	instances instance.InstanceRepository,
	nodeExecs instance.NodeExecutionRepository,
	scheduler TimerScheduler,
	queueMgr QueueManager,
) *TimerDelayExecutor {
	return &TimerDelayExecutor{
		instances: instances,
		nodeExecs: nodeExecs,
		scheduler: scheduler,
		queueMgr:  queueMgr,
	}
}

// Execute resolves the duration, transitions the instance to waiting, schedules
// the timer, and returns a wait result.
func (e *TimerDelayExecutor) Execute(ctx context.Context, node workflow.Node, vars *variable.VariableStore, _ string) (*NodeResult, error) {
	instanceID, ok := InstanceIDFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("timer_delay executor: instance ID not found in context")
	}

	startTime := time.Now()

	// 1. Resolve {{substitution}} on DurationSeconds.
	durationStr := variable.Resolve(node.Config.DurationSeconds, vars)

	// 2. Parse to float64.
	duration, err := strconv.ParseFloat(durationStr, 64)
	if err != nil {
		return nil, fmt.Errorf("durationSeconds is not numeric: %q", durationStr)
	}

	// 3. Calculate waitingUntil.
	var waitingUntil time.Time
	if duration <= 0 {
		waitingUntil = time.Now() // immediately eligible
	} else {
		waitingUntil = time.Now().Add(time.Duration(duration * float64(time.Second)))
	}

	// 4. Transition instance to waiting.
	if err := e.instances.SetWaiting(ctx, instanceID, &waitingUntil); err != nil {
		return nil, fmt.Errorf("failed to set waiting state: %w", err)
	}

	// 5. Write node execution record with succeeded status.
	now := time.Now()
	nodeExec := &instance.NodeExecution{
		ID:         uuid.New(),
		InstanceID: instanceID,
		NodeID:     node.ID,
		Attempt:    1,
		Status:     instance.NodeStatusSucceeded,
		Output:     map[string]any{"waitingUntil": waitingUntil.Format(time.RFC3339)},
		StartedAt:  startTime,
		CompletedAt: &now,
	}
	_ = e.nodeExecs.Create(ctx, nodeExec)

	// 6. Register with scheduler.
	e.scheduler.Schedule(instanceID, waitingUntil, "timer_delay")

	// 7. Notify queue manager (capacity freed).
	e.queueMgr.NotifyCapacityFreed(ctx)

	// 8. Return result with IsWait=true.
	return &NodeResult{
		IsWait: true,
		Output: map[string]any{"waitingUntil": waitingUntil.Format(time.RFC3339)},
	}, nil
}
