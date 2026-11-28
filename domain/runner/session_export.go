package runner

import (
	"context"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
)

// RemoteSessionExporter returns the bundle uploaded inside the originating
// stream assignment. Export sends no extra job and reserves no slot.
type RemoteSessionExporter interface {
	ExportSession(ctx context.Context, runner RunnerID, req engine.SessionExportRequest) (engine.SessionBundle, error)
}

var (
	ErrNoSessionPortability           = errors.New("runner: no session portability adapter for backend")
	ErrRemoteSessionExportUnavailable = errors.New("runner: remote session export is not available")
)

var _ engine.SessionExporter = (*SessionExportRouter)(nil)

// SessionExportRouter routes exports to the runner named by the stream receipt.
type SessionExportRouter struct {
	builtin *SessionPortabilityRegistry
	remote  RemoteSessionExporter
}

// NewSessionExportRouter builds the router; remote may be nil before hub wiring.
func NewSessionExportRouter(builtin *SessionPortabilityRegistry, remote RemoteSessionExporter) *SessionExportRouter {
	return &SessionExportRouter{builtin: builtin, remote: remote}
}

func (r *SessionExportRouter) ExportSession(ctx context.Context, req engine.SessionExportRequest) (engine.SessionBundle, error) {
	f := req.JobFence
	if f.JobID == uuid.Nil || f.Generation < 1 || f.RunnerID == "" || f.RunnerID != req.RunnerID {
		return engine.SessionBundle{}, fmt.Errorf("%w: export of %s needs the holding runner's stream fence", engine.ErrInvalidStepCompletion, req.SessionID)
	}
	var b engine.SessionBundle
	var err error
	if RunnerID(req.RunnerID) == BuiltInRunnerID {
		p, ok := r.builtin.For(req.Backend)
		if !ok {
			return engine.SessionBundle{}, fmt.Errorf("%w: %q", ErrNoSessionPortability, req.Backend)
		}
		b, err = p.Export(ctx, req.SessionID, req.WorkDir)
	} else {
		if r.remote == nil {
			return engine.SessionBundle{}, fmt.Errorf("%w (runner %s)", ErrRemoteSessionExportUnavailable, req.RunnerID)
		}
		b, err = r.remote.ExportSession(ctx, RunnerID(req.RunnerID), req)
	}
	if err != nil {
		return engine.SessionBundle{}, err
	}
	if b.Backend != req.Backend || b.SessionID != req.SessionID {
		return engine.SessionBundle{}, fmt.Errorf("runner: exported bundle is %s/%s, want %s/%s", b.Backend, b.SessionID, req.Backend, req.SessionID)
	}
	if err := b.Verify(); err != nil {
		return engine.SessionBundle{}, err
	}
	return b, nil
}
