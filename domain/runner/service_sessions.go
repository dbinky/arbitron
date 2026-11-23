package runner

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// RunnerSession identifies a live session pinned to a runner.
type RunnerSession struct {
	RunID      uuid.UUID
	NodeID     string
	Generation int64
}

// RunnerSessionIndex lists live pinned sessions for one runner.
type RunnerSessionIndex interface {
	ListByRunner(context.Context, RunnerID) ([]RunnerSession, error)
}

// SessionPinView redacts the run and node when the caller cannot view them.
type SessionPinView struct {
	RunID      *uuid.UUID
	NodeID     string
	Hidden     bool
	Generation int64
}

func (s *Service) sessionViews(ctx context.Context, id RunnerID) ([]SessionPinView, error) {
	out := []SessionPinView{}
	if s.d.Sessions == nil {
		return out, nil
	}
	sessions, err := s.d.Sessions.ListByRunner(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("runner: list sessions pinned to %s: %w", id, err)
	}
	for _, ps := range sessions {
		v := SessionPinView{Generation: ps.Generation}
		if s.d.Visibility.CanView(ctx, ps.RunID) {
			run := ps.RunID
			v.RunID, v.NodeID = &run, ps.NodeID
		} else {
			v.Hidden = true
		}
		out = append(out, v)
	}
	return out, nil
}
