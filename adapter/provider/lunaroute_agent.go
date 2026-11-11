package provider

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/arbitron/arbitron/adapter/opencode"
	"github.com/arbitron/arbitron/adapter/pi"
	"github.com/arbitron/arbitron/domain/engine"
	domprovider "github.com/arbitron/arbitron/domain/provider"
)

const lunaRouteDiagnosisTimeout = 20 * time.Second

// lunaRouteAgent checks launch policy before dispatch and makes at most one
// bounded gateway observation after an opaque failure. It never retries a run.
type lunaRouteAgent struct {
	inner     engine.AgentProvider
	binding   domprovider.ResolvedBinding
	probes    domprovider.ProbeSelector
	preflight func(domprovider.ResolvedBinding) error
}

var _ engine.AgentProvider = (*lunaRouteAgent)(nil)

func wrapLunaRouteAgent(inner engine.AgentProvider, b domprovider.ResolvedBinding, probes domprovider.ProbeSelector, caps domprovider.LunaRouteCapabilities) engine.AgentProvider {
	return &lunaRouteAgent{inner: inner, binding: b, probes: probes,
		preflight: func(binding domprovider.ResolvedBinding) error {
			return domprovider.CheckLunaRouteLaunch(caps, binding, 0)
		}}
}

var lunaRouteLocalErrors = []error{
	context.Canceled, context.DeadlineExceeded,
	domprovider.ErrLunaRouteStoredCredential, domprovider.ErrLunaRouteEndpoint, domprovider.ErrLunaRouteLocalFailure,
	domprovider.ErrInvalidSelection, domprovider.ErrSpecificVersionUnavailable, domprovider.ErrSelectionPortsMissing,
	ErrUnknownProvider, ErrUnknownBackend, opencode.ErrUnknownProvider, pi.ErrUnknownProvider,
}

func lunaRouteDiagnosable(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	for _, local := range lunaRouteLocalErrors {
		if errors.Is(err, local) {
			return false
		}
	}
	var classified *domprovider.LunaRouteFailure
	return !errors.As(err, &classified)
}

func (a *lunaRouteAgent) RunAgent(ctx context.Context, m engine.AgentMission) (*engine.AgentResult, error) {
	if a.binding.Kind != domprovider.ProfileKindAgent {
		return nil, engine.ErrBindingKindMismatch
	}
	if err := a.preflight(a.binding); err != nil {
		return nil, err
	}
	result, err := a.inner.RunAgent(ctx, m)
	if err == nil || !lunaRouteDiagnosable(ctx, err) {
		return result, err
	}
	return result, a.diagnose(ctx, err)
}

func (a *lunaRouteAgent) diagnose(ctx context.Context, original error) error {
	d := &domprovider.LunaRouteDiagnosis{Original: original}
	if a.probes == nil {
		return d
	}
	probe, err := a.probes.ProbeFor(a.binding)
	if err != nil || probe == nil {
		return d
	}
	pctx, cancel := context.WithTimeout(ctx, lunaRouteDiagnosisTimeout)
	defer cancel()
	result, err := probe.Probe(pctx, a.binding)
	var failure *domprovider.LunaRouteFailure
	switch {
	case err != nil && errors.As(err, &failure):
		d.Checked, d.Observation = true, failure
	case err != nil:
		// Local probe errors are not gateway verdicts.
	case !result.Reachable || result.ModelsUnsupported:
		// No authoritative listing was obtained.
	case slices.Contains(result.Models, a.binding.Model):
		d.Checked, d.ModelListed = true, true
	default:
		d.Checked, d.Observation = true, domprovider.ErrLunaRouteModelUnavailable
	}
	return d
}
