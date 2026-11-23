package runner

import (
	"errors"
	"fmt"
	"strings"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
)

// ModelGroup is a capacity aggregation key: credential kind (+ connection for
// connection-backed kinds) and model family. It is never the complete
// eligibility key — ExecRequirements carries the exact request.
type ModelGroup struct {
	Kind          CredentialKind
	ConnectionRef string
	Family        modelidentity.Family
}

// String renders "kind:family", "kind:ref:family", or "none" for slot-only work.
func (g ModelGroup) String() string {
	if g.Kind == CredNone {
		return "none"
	}
	if g.ConnectionRef != "" {
		return string(g.Kind) + ":" + g.ConnectionRef + ":" + string(g.Family)
	}
	return string(g.Kind) + ":" + string(g.Family)
}

// ErrUnresolvedFamily refuses placement of a model whose vendor cannot be proved.
var ErrUnresolvedFamily = errors.New("runner: model family unresolved; refusing remote placement")

// ErrUnresolvedBackend refuses an agent/no-connection binding whose effective
// backend was not floored (or is unknown). Routing never guesses a subscription.
var ErrUnresolvedBackend = errors.New("runner: agent backend unresolved; the executor must floor it before routing")

// GroupFor derives the model group from a resolved binding (design P1 §3.6).
// The order matters: LunaRoute is recognised by canonical provider BEFORE any
// backend rule, so LunaRoute on codex/pi is never mistaken for a subscription.
func GroupFor(b provider.ResolvedBinding, op OpType) (ModelGroup, error) {
	if op == OpProcess {
		return ModelGroup{Kind: CredNone}, nil
	}
	family := modelidentity.ClassifyFamily(b.Provider, b.Model)
	ref := strings.TrimSpace(b.ConnectionRef)
	backend := strings.TrimSpace(b.Backend)

	var g ModelGroup
	switch {
	case provider.IsLunaRoute(b.Provider):
		g = ModelGroup{Kind: CredLunaRoute, ConnectionRef: ref, Family: family}
	case ref != "":
		// Built-in only; an opaque deployment name keeps FamilyUnresolved and
		// matches only its own connection source.
		return ModelGroup{Kind: CredServerConnection, ConnectionRef: ref, Family: family}, nil
	case backend == workflow.BackendClaudeCode,
		backend == workflow.BackendUnset && (op == OpChat || op == OpStream):
		g = ModelGroup{Kind: CredAnthropicSubscription, Family: family}
	case backend == workflow.BackendCodex:
		g = ModelGroup{Kind: CredOpenAISubscription, Family: family}
	case backend == workflow.BackendPi, backend == workflow.BackendOpencode:
		return ModelGroup{Kind: CredLocalHarness, ConnectionRef: backend, Family: family}, nil
	default:
		return ModelGroup{}, fmt.Errorf("%w: op %s backend %q", ErrUnresolvedBackend, op, backend)
	}
	if family == modelidentity.FamilyUnresolved {
		return ModelGroup{}, fmt.Errorf("%w: model %q on %s", ErrUnresolvedFamily, b.Model, g.Kind)
	}
	return g, nil
}
