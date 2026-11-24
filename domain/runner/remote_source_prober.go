package runner

import (
	"context"
	"fmt"
)

type remoteProbeTransport interface {
	ProbeSource(context.Context, SourceID) (ProbeResult, error)
}

// RemoteSourceProber reads fresh observations from the owning runner session.
type RemoteSourceProber struct{ hub remoteProbeTransport }

var _ SourceProber = (*RemoteSourceProber)(nil)

func NewRemoteSourceProber(h *Hub) *RemoteSourceProber {
	if h == nil {
		panic("runner: NewRemoteSourceProber requires a hub")
	}
	return &RemoteSourceProber{hub: h}
}
func (p *RemoteSourceProber) Probe(ctx context.Context, s Source) (ProbeResult, error) {
	if s.RunnerID == BuiltInRunnerID {
		return ProbeResult{}, fmt.Errorf("%w: built-in source", ErrRemoteProbeUnavailable)
	}
	return p.hub.ProbeSource(ctx, s.ID)
}

// Wait joins the liveness loop and its pending-check teardown.
func (h *Hub) Wait() { h.loop.Wait() }
