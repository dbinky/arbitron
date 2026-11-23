package runner

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// HomeView is one workspace homed on a runner, as the runner detail shows
// it. When the caller cannot view the run, RunID is nil and Hidden is true;
// the generation and dirty flag remain, so the runner's load stays
// explainable without naming the run.
type HomeView struct {
	RunID      *uuid.UUID
	Hidden     bool
	Generation int64
	Dirty      bool
}

// homeViews lists the workspaces homed on runner id. A nil home store
// (Phase 1/2 wiring) yields an empty, non-nil list. A store error fails the
// read: an unreadable store is never reported as "no homes".
func (s *Service) homeViews(ctx context.Context, id RunnerID) ([]HomeView, error) {
	out := []HomeView{}
	if s.d.Homes == nil {
		return out, nil
	}
	homes, err := s.d.Homes.ListByRunner(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("runner: list homes of %s: %w", id, err)
	}
	for _, h := range homes {
		v := HomeView{Generation: h.Generation, Dirty: h.Dirty}
		if s.d.Visibility.CanView(ctx, h.RunID) {
			run := h.RunID
			v.RunID = &run
		} else {
			v.Hidden = true
		}
		out = append(out, v)
	}
	return out, nil
}
