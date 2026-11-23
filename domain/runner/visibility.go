package runner

import (
	"context"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// RunVisibility answers whether the context identity may see a run.
type RunVisibility interface {
	CanView(ctx context.Context, runID uuid.UUID) bool
}

// NewInstanceVisibility checks runs against the instance store with the
// engine's own read predicate. Unknown runs and read errors are hidden.
func NewInstanceVisibility(repo instance.InstanceRepository) RunVisibility {
	if repo == nil {
		panic("runner: NewInstanceVisibility requires an instance repository")
	}
	return instanceVisibility{repo: repo}
}

type instanceVisibility struct{ repo instance.InstanceRepository }

func (v instanceVisibility) CanView(ctx context.Context, runID uuid.UUID) bool {
	inst, err := v.repo.Get(ctx, runID)
	if err != nil || inst == nil {
		return false
	}
	return engine.CanViewInstance(ctx, inst)
}
