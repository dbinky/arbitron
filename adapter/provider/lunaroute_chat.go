package provider

import (
	"github.com/arbitron/arbitron/adapter/openrouter"
	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/modelidentity"
	domprovider "github.com/arbitron/arbitron/domain/provider"
)

// lunaRouteChat reuses the OpenAI-compatible adapter with hosted-only routing,
// guarded credentials, exact request policy and classified failures. Inventory
// construction remains lazy: the model and key are checked only at call time.
func (f *Factory) lunaRouteChat(b domprovider.ResolvedBinding) engine.ChatProvider {
	launch := b
	guarded := domprovider.GuardLunaRouteReveal(b)
	// Leave a tampered URL intact so preflight refuses it before reveal or I/O.
	if base, err := domprovider.LunaRouteOpenAIBaseURL(b.BaseURL); err == nil {
		guarded.BaseURL = base
	}
	caps := f.deps.CapabilityRules
	return openrouter.New(openrouter.Config{
		Binding:          guarded,
		HTTPClient:       procenv.NoRedirectClient(f.deps.HTTPClient),
		Timeout:          f.deps.HTTPTimeout,
		MaxResponseBytes: f.deps.MaxResponseBytes,
		ClassifyError: func(status int, body string, transport error, retryAfter string, secrets ...string) error {
			return domprovider.ClassifyLunaRoute(domprovider.LunaRouteOutcome{
				Operation: domprovider.LunaRouteChat, Status: status, Body: body,
				Transport: transport, RetryAfter: retryAfter, Secrets: secrets,
				Code: domprovider.LunaRouteErrorCode(domprovider.LunaRouteChat, body),
			})
		},
		// The strict stream protocol is chosen here, explicitly, rather than
		// inferred from the presence of the error classifier above.
		StrictStream: true,
		// LunaRoute can route to a different model than the one selected, so
		// response identity is enforced for every call. Legacy (version-0) runs
		// resolve to an unpinned binding and never set RequireExecutedModel.
		AlwaysRequireExecutedModel: true,
		AcceptExecutedModel:        modelidentity.LunaRouteExecutedModelAccepted,
		Preflight: func(req engine.ChatRequest) error {
			var settings []string
			if req.Temperature != 0 {
				settings = append(settings, "temperature")
			}
			if req.OutputSchema != nil {
				settings = append(settings, "output_schema")
			}
			return domprovider.CheckLunaRouteRequest(caps, launch, req.MaxTokens, settings)
		},
	})
}
