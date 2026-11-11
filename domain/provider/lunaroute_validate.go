package provider

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/workflow"
)

// LunaRouteRoute is one prospective or actual LunaRoute execution route.
// Backend is the effective agent backend (unused for chat). MaxOutputTokens is
// a request-level setting; zero means it was not requested.
type LunaRouteRoute struct {
	Kind              ProfileKind
	Model             string
	Backend           string
	ContextWindow     ContextWindow
	Effort            Effort
	MaxOutputTokens   int
	RequestedSettings []string
}

// ErrLunaRouteModelSpelling refuses an unreviewed executable identity while
// retaining the existing selection and pin-preflight refusal classifications.
var ErrLunaRouteModelSpelling = fmt.Errorf("%w: %w: use a reviewed fixed LunaRoute id", ErrInvalidSelection, ErrSpecificVersionUnavailable)

// Backends are enabled with production emitter and composition-root sink proofs.
// Live contract verification and release readiness remain separately gated.
var lunaRouteEnabledBackends = map[string]bool{
	workflow.BackendClaudeCode: true,
	workflow.BackendOpencode:   true,
	workflow.BackendCodex:      true,
}

// LunaRouteBackendEnabled reports whether backend may carry LunaRoute routes.
func LunaRouteBackendEnabled(backend string) bool { return lunaRouteEnabledBackends[backend] }

// lunaRouteExactEffort describes emission, not model support. Both must hold:
// LunaRoute never uses native-provider flooring or drops a requested setting.
var lunaRouteExactEffort = map[string]map[Effort]bool{
	LunaRouteChatAxis:          {EffortNone: true, EffortLow: true, EffortMedium: true, EffortHigh: true, EffortMax: true},
	workflow.BackendClaudeCode: {EffortNone: true, EffortLow: true, EffortMedium: true, EffortHigh: true, EffortXHigh: true, EffortMax: true},
	workflow.BackendCodex:      {EffortLow: true, EffortMedium: true, EffortHigh: true, EffortXHigh: true},
	workflow.BackendPi:         {EffortNone: true, EffortLow: true, EffortMedium: true, EffortHigh: true, EffortXHigh: true, EffortMax: true},
	workflow.BackendOpencode:   {},
}

// These emitters register model limits and must never assume native defaults.
var lunaRouteNeedsLimits = map[string]bool{workflow.BackendClaudeCode: true, workflow.BackendOpencode: true}

func evidenceNote(ev string) string {
	if ev == "" {
		return ""
	}
	return " (evidence: " + ev + ")"
}

// checkLunaRouteIdentity proves spelling before consulting vendor evidence.
// The seams are private so production always uses the real grammar/classifier.
func checkLunaRouteIdentity(model string, versions VersionResolver, family func(string, string) modelidentity.Family) error {
	if _, err := versions.SpecificVersion(context.Background(), VersionRequest{
		Model: model, RouteProvider: CanonicalLunaRoute, ConnectionRef: "lunaroute",
	}); err != nil {
		return fmt.Errorf("%w: %s", ErrLunaRouteModelSpelling, err)
	}
	if family(CanonicalLunaRoute, model) == modelidentity.FamilyUnresolved {
		return fmt.Errorf("%w: establish vendor evidence before adding a reviewed alias mapping", ErrLunaRouteFamilyUnresolved)
	}
	return nil
}

// CheckLunaRouteRoute applies the save/launch rule in order: backend enablement,
// grammar, family, table compatibility, context, exact effort, required limits,
// and request conflicts. It never changes the selected model or axes.
func CheckLunaRouteRoute(caps LunaRouteCapabilities, r LunaRouteRoute) error {
	return checkLunaRouteRoute(caps, r, true)
}

func checkLunaRouteRoute(caps LunaRouteCapabilities, r LunaRouteRoute, requireEnabled bool) error {
	if caps == nil {
		return fmt.Errorf("%w: lunaroute capability table", ErrSelectionPortsMissing)
	}
	refuse := func(reason error, detail string) error {
		return &LunaRouteRefusal{Reason: reason, Model: r.Model, Backend: r.Backend, Detail: detail}
	}
	axis := LunaRouteChatAxis
	switch r.Kind {
	case ProfileKindAgent:
		if strings.TrimSpace(r.Backend) == "" {
			return refuse(ErrLunaRouteBackendRequired, "set backend to claude-code, opencode, codex or pi explicitly; deployment defaults never choose a LunaRoute backend")
		}
		if !slices.Contains(LunaRouteBackends, r.Backend) || requireEnabled && !LunaRouteBackendEnabled(r.Backend) {
			return refuse(ErrLunaRouteBackendUnsupported, fmt.Sprintf("LunaRoute on %s not yet supported in this release; select a supported explicit backend", r.Backend))
		}
		axis = r.Backend
	case ProfileKindChat:
	default:
		return refuse(ErrLunaRouteIncompatible, fmt.Sprintf("unknown profile kind %q; select chat or agent", r.Kind))
	}
	if err := checkLunaRouteIdentity(r.Model, modelidentity.NewVersionResolver(), modelidentity.ClassifyFamily); err != nil {
		return refuse(err, "")
	}
	m, _ := caps.LunaRouteModel(r.Model)
	if f := m.Task(r.Kind); f.Support == SupportUnsupported {
		return refuse(ErrLunaRouteIncompatible, fmt.Sprintf("the capability table marks %s tasks unsupported%s; choose another model", r.Kind, evidenceNote(f.Evidence)))
	}
	if r.Kind == ProfileKindAgent {
		if f := m.Backend(r.Backend); f.Support == SupportUnsupported {
			return refuse(ErrLunaRouteIncompatible, fmt.Sprintf("the capability table marks backend %s unsupported%s; choose another backend or model", r.Backend, evidenceNote(f.Evidence)))
		}
	}
	if r.ContextWindow != "" && r.ContextWindow != ContextDefault {
		return refuse(ErrLunaRouteAxisUnsupported, fmt.Sprintf("context %q is a Claude-native switch, not a LunaRoute model limit; set context to default", r.ContextWindow))
	}
	if r.Effort != "" && r.Effort != EffortDefault {
		if !r.Effort.Valid() {
			return refuse(fmt.Errorf("%w: %w", ErrLunaRouteAxisUnsupported, ErrInvalidEffort), fmt.Sprintf("unknown effort %q; set effort to default or a supported level", r.Effort))
		}
		if !lunaRouteExactEffort[axis][r.Effort] {
			return refuse(ErrLunaRouteAxisUnsupported, fmt.Sprintf("effort %q has no exact emission on %s; set effort to default or a level that path sends verbatim", r.Effort, axis))
		}
		if f := m.Effort(axis); !f.Known || !slices.Contains(f.Levels, r.Effort) {
			return refuse(ErrLunaRouteAxisUnsupported, fmt.Sprintf("effort %q is not known to be supported for this model on %s; set effort to default or record evidence in the capability table", r.Effort, axis))
		}
	}
	if r.Kind == ProfileKindAgent && lunaRouteNeedsLimits[r.Backend] && (!m.ContextTokens.Known() || !m.OutputTokens.Known()) {
		return refuse(ErrLunaRouteLimitsUnknown, r.Backend+" on LunaRoute needs known context and output limits for this model in the capability table (lunaroute_models)")
	}
	if r.MaxOutputTokens > 0 && m.OutputTokens.Known() && r.MaxOutputTokens > m.OutputTokens.Tokens {
		return refuse(ErrLunaRouteSettingConflict, fmt.Sprintf("max tokens %d exceeds the model's known output limit %d; lower max tokens", r.MaxOutputTokens, m.OutputTokens.Tokens))
	}
	for _, setting := range r.RequestedSettings {
		if f := m.RequestSettings[setting]; f.Support == SupportUnsupported {
			return refuse(ErrLunaRouteSettingConflict, fmt.Sprintf("request setting %s is unsupported%s; omit it or choose a compatible model", setting, evidenceNote(f.Evidence)))
		}
	}
	return nil
}

// CheckLunaRouteLaunch validates the actual binding after pins and backend
// precedence. Non-LunaRoute bindings are untouched.
func CheckLunaRouteLaunch(caps LunaRouteCapabilities, b ResolvedBinding, maxOutputTokens int) error {
	return CheckLunaRouteRequest(caps, b, maxOutputTokens, nil)
}

// CheckLunaRouteRequest additionally validates the settings emitted by a request.
func CheckLunaRouteRequest(caps LunaRouteCapabilities, b ResolvedBinding, maxOutputTokens int, requestedSettings []string) error {
	return checkLunaRouteBinding(caps, b, maxOutputTokens, requestedSettings, true)
}

// CheckLunaRouteAdapterLaunch is for direct backend constructors before their
// production enablement. It skips only the phased enablement check; factories,
// profile saves and triggers must use the full gate instead.
func CheckLunaRouteAdapterLaunch(caps LunaRouteCapabilities, b ResolvedBinding, maxOutputTokens int) error {
	return checkLunaRouteBinding(caps, b, maxOutputTokens, nil, false)
}

func checkLunaRouteBinding(caps LunaRouteCapabilities, b ResolvedBinding, maxOutputTokens int, requestedSettings []string, requireEnabled bool) error {
	if !IsLunaRoute(b.Provider) {
		return nil
	}
	if caps == nil {
		return fmt.Errorf("%w: lunaroute capability table", ErrSelectionPortsMissing)
	}
	if _, err := NormalizeLunaRouteBaseURL(b.BaseURL); err != nil {
		return err
	}
	route := LunaRouteRoute{Kind: b.Kind, Model: b.Model, ContextWindow: b.ContextWindow, Effort: b.Effort, MaxOutputTokens: maxOutputTokens, RequestedSettings: requestedSettings}
	if b.Kind == ProfileKindAgent {
		if !b.BackendExplicit {
			return &LunaRouteRefusal{Reason: ErrLunaRouteBackendRequired, Model: b.Model, Backend: b.Backend, Detail: "select a backend explicitly in the profile or node; deployment defaults do not choose LunaRoute backends"}
		}
		route.Backend = strings.TrimSpace(b.Backend)
	}
	return checkLunaRouteRoute(caps, route, requireEnabled)
}
