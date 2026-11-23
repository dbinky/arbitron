package probe

import (
	"context"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
)

// LunaRouteProber reuses the existing connection probe (adapter/probe factory)
// against the source's own connection. Busy or any failure is auth UNKNOWN:
// LunaRoute pressure is never logged-out and never exhaustion.
type LunaRouteProber struct {
	store  provider.ConnectionStore
	probes provider.ProbeSelector
}

func NewLunaRouteProber(store provider.ConnectionStore, probes provider.ProbeSelector) *LunaRouteProber {
	if store == nil || probes == nil {
		panic("probe: NewLunaRouteProber requires a connection store and a probe selector")
	}
	return &LunaRouteProber{store: store, probes: probes}
}

func (p *LunaRouteProber) Probe(ctx context.Context, s runner.Source) (runner.ProbeResult, error) {
	if s.Kind != runner.CredLunaRoute || s.CredentialRef == "" {
		return runner.ProbeResult{Auth: runner.AuthUnknown}, nil
	}
	conn, err := p.store.Get(ctx, s.CredentialRef)
	if err != nil {
		return runner.ProbeResult{Auth: runner.AuthUnknown}, nil
	}
	b := provider.ProbeBindingFromConnection(p.store, s.CredentialRef, conn)
	probe, err := p.probes.ProbeFor(b)
	if err != nil {
		return runner.ProbeResult{Auth: runner.AuthUnknown}, nil
	}
	if _, err := probe.Probe(ctx, b); err != nil {
		return runner.ProbeResult{Auth: runner.AuthUnknown}, nil
	}
	return runner.ProbeResult{Auth: runner.AuthOK}, nil
}
