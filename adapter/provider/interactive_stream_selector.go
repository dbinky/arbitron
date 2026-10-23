package provider

import (
	"errors"
	"fmt"
	"strings"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
)

// ErrUnsupportedAgentBackend is returned when an agent-kind interactive binding
// names a backend the stream selector cannot route — an unknown/typo backend, or
// opencode (which has no streaming adapter and is explicitly out of scope for
// interactive streaming). It is a package-level sentinel wrapped with %w so
// callers can distinguish it from an unknown-kind error via errors.Is.
//
// Renamed from ErrUnsupportedAgentProvider (Phase 2): dispatch moved from the
// PROVIDER axis (claude-cli only) to the BACKEND axis (claude-code / codex /
// opencode), matching the batch path's AgentProviderFor vocabulary.
var ErrUnsupportedAgentBackend = errors.New("interactive_agent: unsupported agent stream backend")

// AgentStreamFactory constructs a streaming provider from the current turn's
// resolved binding, including its pinned model and live credentials/axes.
type AgentStreamFactory func(provider.ResolvedBinding) engine.ChatProvider

// InteractiveStreamSelector implements engine.InteractiveStreamSelector: it
// routes a resolved binding to the appropriate streaming ChatProvider based on
// the binding's Kind, dispatching agent-kind bindings on the BACKEND axis (the
// SAME vocabulary and default-backend floor as the batch AgentProviderFor):
//
//   - ProfileKindChat  → delegates to the injected ChatProviderSelector
//     (tool-suppressed stream); unchanged by the backend generalization.
//   - ProfileKindAgent → the per-backend streaming provider. The dispatch contract
//     is "backend id → its interactive streaming provider"; the TOOL/SANDBOX
//     POSTURE is each provider's own implementation detail, NOT a property of this
//     map. It is backend-defined: claude-code is the tool-enabled (autonomous)
//     stream adapter, codex is the read-only + docs/-scoped approval-gated
//     interactive provider. An empty backend is floored to defaultBackend first;
//     opencode and any unknown/typo backend fail closed with
//     ErrUnsupportedAgentBackend (never a silent fallback to claude).
type InteractiveStreamSelector struct {
	chat engine.ChatProviderSelector
	// agentStreams maps backend ids to per-binding streaming constructors. A
	// backend absent from the map fails closed; opencode has no stream adapter.
	agentStreams map[string]AgentStreamFactory
	// defaultBackend is the deployment floor applied to an empty agent backend —
	// the SAME floor the batch path applies before reaching the factory, so an
	// interactive node and a batch node inherit the same default.
	defaultBackend string
}

var _ engine.InteractiveStreamSelector = (*InteractiveStreamSelector)(nil)

// NewInteractiveStreamSelector constructs an InteractiveStreamSelector.
// chat is the ChatProviderSelector used for chat-kind bindings. agentStreams
// maps each supported agent backend to its per-binding streaming constructor.
// The provider's posture is backend-defined, not assumed tool-enabled.
// defaultBackend is the floor applied to an empty agent-kind backend.
func NewInteractiveStreamSelector(chat engine.ChatProviderSelector, agentStreams map[string]AgentStreamFactory, defaultBackend string) *InteractiveStreamSelector {
	return &InteractiveStreamSelector{
		chat:           chat,
		agentStreams:   agentStreams,
		defaultBackend: defaultBackend,
	}
}

// StreamProviderFor selects the streaming ChatProvider for the given binding.
func (s *InteractiveStreamSelector) StreamProviderFor(b provider.ResolvedBinding) (engine.ChatProvider, error) {
	switch b.Kind {
	case provider.ProfileKindChat:
		return s.chat.ChatProviderFor(b)
	case provider.ProfileKindAgent:
		return s.agentStreamFor(b)
	default:
		return nil, fmt.Errorf("interactive stream: unknown profile kind %q", b.Kind)
	}
}

// agentStreamFor dispatches an agent-kind binding on the backend axis. It trims
// whitespace only (NOT lowercase) so the vocabulary stays exact-match identical
// to the workflow validator and the factory — a near-miss case (e.g. "Codex")
// stays unknown and fails closed, never silently routed. An empty backend is
// floored to defaultBackend first (matching the batch path). opencode is rejected
// with a clear, explicit message; any backend absent from the stream map (incl.
// opencode and unknown values) returns ErrUnsupportedAgentBackend.
func (s *InteractiveStreamSelector) agentStreamFor(b provider.ResolvedBinding) (engine.ChatProvider, error) {
	backend := strings.TrimSpace(b.Backend)
	if backend == "" {
		backend = s.defaultBackend
	}

	// opencode has no streaming adapter: surface an explicit, fail-closed message
	// (NOT a silent fallback to claude) so an operator who points an interactive
	// node at opencode learns it is out of scope rather than getting claude.
	if backend == workflow.BackendOpencode {
		return nil, fmt.Errorf("%w: opencode does not support interactive streaming (backend %q)", ErrUnsupportedAgentBackend, b.Backend)
	}

	if build, ok := s.agentStreams[backend]; ok && build != nil {
		b.Backend = backend
		if err := validatePinnedRoute(b, backend); err != nil {
			return nil, err
		}
		return build(b), nil
	}
	return nil, fmt.Errorf("%w: %q", ErrUnsupportedAgentBackend, b.Backend)
}
