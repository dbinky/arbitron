package engine

import (
	"context"
	"errors"
	"fmt"
)

// GroupAreaMeter measures the bytes a working area holds: every regular file
// at any depth, including ignored, untracked and author Git metadata (all of
// which every task fork copies). Implemented by adapter/filesystem.
type GroupAreaMeter interface {
	AreaBytes(ctx context.Context, dir string) (int64, error)
}

// ErrGroupLaunchBudget is wrapped (inside ErrGroupPreLaunch) when a launch's
// eager copies would exceed the server's byte budget.
var ErrGroupLaunchBudget = errors.New("parallel_group launch exceeds the server's working-area copy budget")

// SetLaunchByteBudget bounds the storage one launch may allocate before any
// task is queued: the parent area's size times (tasks + 1) — the snapshot plus
// one fork per task — must not exceed maxBytes. The budget is server-owned (no
// workflow setting raises it). Composition time only; maxBytes must be
// positive and meter non-nil. Unset, a launch is bounded only by
// workflow.MaxGroupTasks.
func (l *GroupLauncher) SetLaunchByteBudget(maxBytes int64, meter GroupAreaMeter) {
	switch {
	case maxBytes <= 0:
		panic("engine: SetLaunchByteBudget requires a positive byte budget")
	case meter == nil:
		panic("engine: SetLaunchByteBudget requires a non-nil GroupAreaMeter")
	}
	l.maxLaunchBytes = maxBytes
	l.meter = meter
}

// checkLaunchBudget refuses a launch whose snapshot plus forks of parentWS
// would exceed the byte budget. It runs BEFORE the snapshot, so a refused
// launch writes nothing.
func (l *GroupLauncher) checkLaunchBudget(ctx context.Context, parentWS string, tasks int) error {
	if l.meter == nil || parentWS == "" {
		return nil
	}
	size, err := l.meter.AreaBytes(ctx, parentWS)
	if err != nil {
		return fmt.Errorf("measure the parent's working area: %w", err)
	}
	copies := int64(tasks) + 1
	if size > l.maxLaunchBytes/copies {
		return fmt.Errorf("%w: %d bytes x %d copies (snapshot + %d tasks) exceeds %d bytes; reduce the tasks or the working area",
			ErrGroupLaunchBudget, size, copies, tasks, l.maxLaunchBytes)
	}
	return nil
}
