package executor

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
)

var (
	// ErrCapabilityUnavailable marks a request this runner does not advertise.
	ErrCapabilityUnavailable = errors.New("capability_unavailable")
	// ErrExecutedModelUnproven marks missing native proof of the requested model.
	ErrExecutedModelUnproven = errors.New("executed model not proven")
)

// MatchNativeAgent rechecks the advertised native predicate before acceptance.
// Unsupported axes and tool scopes are never silently floored.
func MatchNativeAgent(sources []runneragent.Source, a *runneragent.AgentJob, rules provider.CapabilityRules) (runneragent.Source, runner.ExecutionCapability, error) {
	fail := func(why string) (runneragent.Source, runner.ExecutionCapability, error) {
		return runneragent.Source{}, runner.ExecutionCapability{}, fmt.Errorf("%w: %s", ErrCapabilityUnavailable, why)
	}
	if a == nil {
		return fail("agent dispatch without payload")
	}
	if rules.Empty() {
		return fail("capability rules not loaded")
	}
	reqCtx, reqEffort := provider.ContextWindow(a.ContextWindow), provider.Effort(a.Effort)
	if reqCtx == "" {
		reqCtx = provider.ContextDefault
	}
	if reqEffort == "" {
		reqEffort = provider.EffortDefault
	}
	effCtx, effEffort, err := rules.Validate(a.Backend, a.Model, reqCtx, reqEffort)
	if err != nil || effCtx != reqCtx || effEffort != reqEffort {
		return fail("requested context/effort is not supported exactly")
	}
	for _, s := range sources {
		if string(s.Kind) != a.CredentialKind || s.UnavailableReason != "" || s.Health.Auth == runner.AuthLoggedOut {
			continue
		}
		for _, c := range s.Capabilities {
			if c.Op != runner.OpAgent || c.Backend != a.Backend || provider.Canonicalize(c.Provider) != provider.Canonicalize(a.Provider) ||
				!slices.Contains(c.Models, a.Model) || (a.RequireExecutedModel && !c.ExecutedModel) {
				continue
			}
			if !slices.Contains(c.ContextWindows, reqCtx) {
				continue
			}
			// EffortNone is universal and validated locally, not advertised.
			if reqEffort != provider.EffortNone && !slices.Contains(c.Efforts, reqEffort) {
				continue
			}
			if c.ToolPolicy == "" || !runner.ToolScopeSatisfied(c, a.ToolScope) {
				continue
			}
			return s, c, nil
		}
	}
	return fail(fmt.Sprintf("no available %s source advertises %s/%s for model %q", a.CredentialKind, a.Provider, a.Backend, a.Model))
}

// VerifyExecutedModel requires native reported-execution evidence; the result's
// model label is not proof. Claude's context suffix does not change the model.
func VerifyExecutedModel(want string, r *engine.AgentResult) error {
	if r != nil {
		for _, e := range r.ModelEvidence {
			if e.ReportsExecution() && strings.TrimSuffix(strings.TrimSpace(e.Model), "[1m]") == want {
				return nil
			}
		}
	}
	return fmt.Errorf("%w: requested %q", ErrExecutedModelUnproven, want)
}

// SetInventory supplies the live advertised source evidence. Without it agent
// dispatches fail closed.
func (w *WorkspaceJobs) SetInventory(inv runneragent.LocalSources) { w.inv = inv }

func (w *WorkspaceJobs) nativeSources(ctx context.Context) []runneragent.Source {
	if w.inv == nil {
		return nil
	}
	return w.inv.Sources(ctx)
}
