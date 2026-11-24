package runner

import (
	"context"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
)

// AgentToolPolicy resolves effective agent tools. Phase 3 supplies and consumes
// this policy at startup; Phase 1 keeps inner-factory tool behavior.
type AgentToolPolicy func(provider.ResolvedBinding, []string) ([]string, error)

// SetAgentToolPolicy installs the policy before execution starts.
func (d *StepDispatcher) SetAgentToolPolicy(p AgentToolPolicy) { d.agentToolPolicy = p }

// dispatcher is the slice of *StepDispatcher the routed providers need. The
// exported constructors take the concrete *StepDispatcher (contracts §11); the
// interface lets unit tests record dispatches.
type dispatcher interface {
	Dispatch(ctx context.Context, req ExecRequirements, job StepJob) (StepOutput, error)
}

// errMissingStepOutput reports a dispatcher success that carried no payload for
// the requested op. It is a programming error surfaced as a step failure.
var errMissingStepOutput = errors.New("runner: dispatch returned no output for the operation")

// BuiltInAffinity is the Phase 1 affinity: every operation stays on the
// built-in runner.
func BuiltInAffinity(provider.ResolvedBinding) Affinity { return AffinityBuiltIn }

// NewRoutingChatSelector wraps a chat selector so every Chat/StreamChat call is
// placed through the dispatcher. inner is consulted first, so validation errors
// surface unchanged. A nil affinity uses BuiltInAffinity.
func NewRoutingChatSelector(inner engine.ChatProviderSelector, d *StepDispatcher, affinity func(provider.ResolvedBinding) Affinity) engine.ChatProviderSelector {
	if d == nil {
		panic("runner: NewRoutingChatSelector requires a dispatcher")
	}
	return newRoutingChatSelector(inner, d, affinity)
}

// NewRoutingAgentSelector wraps an agent selector so RunAgent is placed.
func NewRoutingAgentSelector(inner engine.AgentProviderSelector, d *StepDispatcher) engine.AgentProviderSelector {
	if d == nil {
		panic("runner: NewRoutingAgentSelector requires a dispatcher")
	}
	return newRoutingAgentSelector(inner, d)
}

// NewRoutingStreamSelector wraps the interactive stream selector so every
// interactive turn's StreamChat is placed.
func NewRoutingStreamSelector(inner engine.InteractiveStreamSelector, d *StepDispatcher) engine.InteractiveStreamSelector {
	if d == nil {
		panic("runner: NewRoutingStreamSelector requires a dispatcher")
	}
	return streamSelectorFor(inner, d, d.d.SessionPins)
}

// NewRoutedProcessRunner places script/tool_call processes as slot-only jobs.
func NewRoutedProcessRunner(d *StepDispatcher) engine.ProcessRunner {
	if d == nil {
		panic("runner: NewRoutedProcessRunner requires a dispatcher")
	}
	return newRoutedProcessRunner(d)
}

func newRoutingChatSelector(inner engine.ChatProviderSelector, d dispatcher, affinity func(provider.ResolvedBinding) Affinity) engine.ChatProviderSelector {
	if inner == nil || d == nil {
		panic("runner: routing chat selector requires inner selector and dispatcher")
	}
	if affinity == nil {
		affinity = BuiltInAffinity
	}
	return &routingChatSelector{inner: inner, d: d, affinity: affinity}
}

func newRoutingAgentSelector(inner engine.AgentProviderSelector, d dispatcher) engine.AgentProviderSelector {
	if inner == nil || d == nil {
		panic("runner: routing agent selector requires inner selector and dispatcher")
	}
	return &routingAgentSelector{inner: inner, d: d}
}

func newRoutingStreamSelector(inner engine.InteractiveStreamSelector, d dispatcher) engine.InteractiveStreamSelector {
	if inner == nil || d == nil {
		panic("runner: routing stream selector requires inner selector and dispatcher")
	}
	return &routingStreamSelector{inner: inner, d: d}
}

func newRoutedProcessRunner(d dispatcher) engine.ProcessRunner {
	if d == nil {
		panic("runner: routed process runner requires a dispatcher")
	}
	return &routedProcessRunner{d: d}
}

type routingChatSelector struct {
	inner    engine.ChatProviderSelector
	d        dispatcher
	affinity func(provider.ResolvedBinding) Affinity
}

func (s *routingChatSelector) ChatProviderFor(b provider.ResolvedBinding) (engine.ChatProvider, error) {
	if _, err := s.inner.ChatProviderFor(b); err != nil {
		return nil, err
	}
	return &routedChat{d: s.d, b: b, affinity: s.affinity(b)}, nil
}

type routingStreamSelector struct {
	inner engine.InteractiveStreamSelector
	d     dispatcher
}

func (s *routingStreamSelector) StreamProviderFor(b provider.ResolvedBinding) (engine.ChatProvider, error) {
	if _, err := s.inner.StreamProviderFor(b); err != nil {
		return nil, err
	}
	return &routedChat{d: s.d, b: b, affinity: AffinityBuiltIn}, nil
}

type routingAgentSelector struct {
	inner engine.AgentProviderSelector
	d     dispatcher
}

func (s *routingAgentSelector) AgentProviderFor(b provider.ResolvedBinding) (engine.AgentProvider, error) {
	if _, err := s.inner.AgentProviderFor(b); err != nil {
		return nil, err
	}
	return &routedAgent{d: s.d, b: b}, nil
}

// routedChat is the chat/stream provider handed to executors. It never executes
// anything itself; the placed runner re-selects the inner provider.
type routedChat struct {
	d        dispatcher
	b        provider.ResolvedBinding
	affinity Affinity
}

func (p *routedChat) Chat(ctx context.Context, req engine.ChatRequest) (*engine.ChatResponse, error) {
	er, err := requirementsFor(ctx, p.b, OpType("chat"), hermeticCallAffinity(ctx, p.affinity, p.b, req), req.RequireExecutedModel, req.ToolScope)
	if err != nil {
		return nil, err
	}
	r := req
	out, err := p.d.Dispatch(ctx, er, StepJob{Op: OpType("chat"), Binding: p.b, Chat: &r})
	if err != nil {
		return out.Chat, err
	}
	if out.Chat == nil {
		return nil, errMissingStepOutput
	}
	return out.Chat, nil
}

func (p *routedChat) StreamChat(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
	er, err := requirementsFor(ctx, p.b, OpType("stream"), hermeticCallAffinity(ctx, p.affinity, p.b, req), req.RequireExecutedModel, req.ToolScope)
	if err != nil {
		return nil, err
	}
	r := req
	out, err := p.d.Dispatch(ctx, er, StepJob{Op: OpType("stream"), Binding: p.b, Chat: &r})
	if err != nil {
		return nil, err
	}
	if out.Stream == nil {
		return nil, errMissingStepOutput
	}
	return out.Stream, nil
}

// AgentAffinity keeps missions without a workspace built-in and places
// missions with a working directory through the workspace protocol.
func AgentAffinity(m engine.AgentMission) Affinity {
	if m.WorkDir != "" {
		return AffinityWorkspace
	}
	return AffinityBuiltIn
}

type routedAgent struct {
	d dispatcher
	b provider.ResolvedBinding
}

func (p *routedAgent) RunAgent(ctx context.Context, m engine.AgentMission) (*engine.AgentResult, error) {
	er, err := requirementsFor(ctx, p.b, OpType("agent"), AgentAffinity(m), false, m.ToolScope)
	if err != nil {
		return nil, err
	}
	mm := m
	out, err := p.d.Dispatch(ctx, er, StepJob{Op: OpType("agent"), Binding: p.b, Agent: &mm})
	if err != nil {
		return out.Agent, err
	}
	if out.Agent == nil {
		return nil, errMissingStepOutput
	}
	return out.Agent, nil
}

type routedProcessRunner struct{ d dispatcher }

func (p *routedProcessRunner) RunProcess(ctx context.Context, spec engine.ProcessSpec) (engine.ProcessResult, error) {
	s := spec
	er := ExecRequirements{
		Group:    ModelGroup{Kind: CredNone},
		Op:       OpType("process"),
		Affinity: ProcessAffinity(spec),
		RunID:    runIDFrom(ctx),
	}
	out, err := p.d.Dispatch(ctx, er, StepJob{Op: OpType("process"), Process: &s})
	if out.Process != nil {
		return *out.Process, err
	}
	if err != nil {
		return engine.ProcessResult{}, err
	}
	return engine.ProcessResult{}, errMissingStepOutput
}

// requirementsFor builds the placement requirements for a model operation.
// The binding's own tool allowlist wins over the request scope (same precedence
// the adapters apply); pinned bindings always require executed-model evidence.
func requirementsFor(ctx context.Context, b provider.ResolvedBinding, op OpType, aff Affinity, requireModel bool, scope []string) (ExecRequirements, error) {
	g, err := GroupFor(b, op)
	if err != nil {
		return ExecRequirements{}, fmt.Errorf("route %s for model %q: %w", op, b.Model, err)
	}
	fingerprint, err := EndpointFingerprint(b)
	if err != nil {
		return ExecRequirements{}, err
	}
	tools := scope
	if len(b.AllowedTools) > 0 {
		tools = b.AllowedTools
	}
	return ExecRequirements{
		Provider:             b.Provider,
		EndpointFingerprint:  fingerprint,
		ContextWindow:        b.ContextWindow,
		Effort:               b.Effort,
		Group:                g,
		Model:                b.Model,
		Backend:              b.Backend,
		Op:                   op,
		Affinity:             aff,
		RunID:                runIDFrom(ctx),
		RequireExecutedModel: requireModel || b.RequiresModelEvidence(),
		Tools:                append([]string(nil), tools...),
	}, nil
}

// runIDFrom reads the run from the step identity. A missing identity yields
// uuid.Nil here; the dispatcher refuses it with engine.ErrMissingStepIdentity.
func runIDFrom(ctx context.Context) uuid.UUID {
	if si, ok := engine.StepIdentityFromContext(ctx); ok {
		return si.RunID
	}
	return uuid.Nil
}

// WithDefaultAgentBackend fills an empty Backend on agent-kind bindings with the
// deployment default before the binding reaches the wrapped selector. It mirrors
// the agent executor (executor_agent.go sets binding.Backend before
// AgentProviderFor) so GroupFor sees the effective backend on interactive turns.
func WithDefaultAgentBackend(sel engine.InteractiveStreamSelector, backend string) engine.InteractiveStreamSelector {
	if sel == nil {
		panic("runner: WithDefaultAgentBackend requires a selector")
	}
	return defaultBackendSelector{inner: sel, backend: backend}
}

type defaultBackendSelector struct {
	inner   engine.InteractiveStreamSelector
	backend string
}

func (s defaultBackendSelector) StreamProviderFor(b provider.ResolvedBinding) (engine.ChatProvider, error) {
	if b.Kind == provider.ProfileKindAgent && b.Backend == "" {
		b.Backend = s.backend
	}
	return s.inner.StreamProviderFor(b)
}
