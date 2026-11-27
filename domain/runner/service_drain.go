// domain/runner/service_drain.go
package runner

import (
	"context"
	"fmt"

	"github.com/arbitron/arbitron/domain/audit"
)

// SetDrainController enables drain (P3 T13 wires it). Call before serving.
func (s *Service) SetDrainController(c *DrainController) { s.drain = c }

// Drain starts (or continues) draining runner id and returns its progress
// (admin only; every call audited runner.drain).
func (s *Service) Drain(ctx context.Context, id RunnerID) (DrainProgress, error) {
	who, err := svcAdmin(ctx)
	if err != nil {
		return DrainProgress{}, err
	}
	if s.drain == nil {
		return DrainProgress{}, ErrDrainUnavailable
	}
	r, _, err := s.d.Registry.GetRunner(ctx, id)
	if err != nil {
		return DrainProgress{}, err
	}
	started := !r.Draining
	if started {
		if err := s.d.Registry.SetDraining(ctx, id, true); err != nil {
			return DrainProgress{}, err
		}
		if err := s.drain.Begin(ctx, id); err != nil {
			return DrainProgress{}, err
		}
		s.operatorEvent(ctx, EventRunnerDraining, id, who, map[string]any{"draining": true})
		s.d.Notifier.Notify()
	}
	s.d.Audit.Record(ctx, audit.ActionRunnerDrain, string(id), svcAuditDetail(map[string]any{"started": started}))
	progress, err := s.drain.Step(ctx, id)
	if err != nil {
		return DrainProgress{}, fmt.Errorf("drain %s: %w", id, err)
	}
	progress = s.visibleDrain(ctx, progress)
	return progress, nil
}

// visibleDrain hides run IDs the caller cannot view; the group stays named.
func (s *Service) visibleDrain(ctx context.Context, p DrainProgress) DrainProgress {
	out := p
	out.Blocked = make([]DrainBlocker, 0, len(p.Blocked))
	for _, b := range p.Blocked {
		if b.RunID != nil && !s.d.Visibility.CanView(ctx, *b.RunID) {
			b.RunID = nil
		}
		out.Blocked = append(out.Blocked, b)
	}
	return out
}

func (s *Service) attachDrain(ctx context.Context, v *RunnerView) error {
	if s.drain == nil || !v.Runner.Draining {
		return nil
	}
	p, err := s.drain.Progress(ctx, v.Runner.ID)
	if err != nil {
		return fmt.Errorf("runner: drain progress of %s: %w", v.Runner.ID, err)
	}
	p = s.visibleDrain(ctx, p)
	v.Drain = &p
	return nil
}
