package runner

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingDispatcher records every (req, job) and answers from a per-op table,
// so the routed provider must hand over the right payload to get the right answer.
type recordingDispatcher struct {
	mu   sync.Mutex
	reqs []ExecRequirements
	jobs []StepJob
	out  map[OpType]StepOutput
	err  error
}

func (d *recordingDispatcher) Dispatch(_ context.Context, req ExecRequirements, job StepJob) (StepOutput, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.reqs = append(d.reqs, req)
	d.jobs = append(d.jobs, job)
	return d.out[req.Op], d.err
}

// innerChats is a validating inner selector: it refuses bindings whose Model is
// "refused" and records every binding it saw. Its providers must NEVER be called
// by the routed provider (execution belongs to the runner).
type innerChats struct {
	seen  []provider.ResolvedBinding
	calls int
}

func (s *innerChats) ChatProviderFor(b provider.ResolvedBinding) (engine.ChatProvider, error) {
	s.seen = append(s.seen, b)
	if b.Model == "refused" {
		return nil, errInnerRefused
	}
	return countingChat{&s.calls}, nil
}

func (s *innerChats) AgentProviderFor(b provider.ResolvedBinding) (engine.AgentProvider, error) {
	s.seen = append(s.seen, b)
	if b.Model == "refused" {
		return nil, errInnerRefused
	}
	return countingAgent{&s.calls}, nil
}

func (s *innerChats) StreamProviderFor(b provider.ResolvedBinding) (engine.ChatProvider, error) {
	s.seen = append(s.seen, b)
	if b.Model == "refused" {
		return nil, errInnerRefused
	}
	return countingChat{&s.calls}, nil
}

var errInnerRefused = errors.New("inner: binding refused")

type countingChat struct{ n *int }

func (c countingChat) Chat(context.Context, engine.ChatRequest) (*engine.ChatResponse, error) {
	*c.n++
	return &engine.ChatResponse{Output: "inner-direct"}, nil
}
func (c countingChat) StreamChat(context.Context, engine.ChatRequest) (<-chan engine.ChatChunk, error) {
	*c.n++
	return nil, nil
}

type countingAgent struct{ n *int }

func (c countingAgent) RunAgent(context.Context, engine.AgentMission) (*engine.AgentResult, error) {
	*c.n++
	return &engine.AgentResult{Summary: "inner-direct"}, nil
}

func stepCtx(runID uuid.UUID) context.Context {
	return engine.ContextWithStepIdentity(context.Background(), engine.StepIdentity{
		RunID: runID, NodeID: "n1", InvocationID: uuid.New(), OperationID: "node", Kind: engine.OperationNode, Attempt: 1,
	})
}

func subscriptionBinding(model string) provider.ResolvedBinding {
	return provider.ResolvedBinding{Kind: provider.ProfileKindChat, Provider: "claude-cli", Model: model}
}

// Happy path: a chat call is dispatched with exact requirements and the response
// comes from the dispatcher, not from the inner provider.
func TestRoutingChatSelector_DispatchesChatWithRequirements(t *testing.T) {
	inner := &innerChats{}
	runID := uuid.New()
	d := &recordingDispatcher{out: map[OpType]StepOutput{"chat": {Chat: &engine.ChatResponse{Output: "routed"}}}}
	sel := newRoutingChatSelector(inner, d, BuiltInAffinity)

	b := subscriptionBinding("claude-sonnet-4-5")
	b.AllowedTools = []string{"Read"}
	p, err := sel.ChatProviderFor(b)
	require.NoError(t, err)
	resp, err := p.Chat(stepCtx(runID), engine.ChatRequest{Model: "claude-sonnet-4-5", Prompt: "hi", RequireExecutedModel: true})
	require.NoError(t, err)
	assert.Equal(t, "routed", resp.Output)
	assert.Equal(t, 0, inner.calls, "inner provider must not execute; the runner does")

	require.Len(t, d.reqs, 1)
	req := d.reqs[0]
	wantGroup, gErr := GroupFor(b, OpType("chat"))
	require.NoError(t, gErr)
	assert.Equal(t, wantGroup, req.Group)
	assert.Equal(t, CredAnthropicSubscription, req.Group.Kind)
	assert.Equal(t, modelidentity.Family("anthropic"), req.Group.Family)
	assert.Equal(t, "claude-sonnet-4-5", req.Model)
	assert.Equal(t, "", req.Backend)
	assert.Equal(t, OpType("chat"), req.Op)
	assert.Equal(t, AffinityBuiltIn, req.Affinity)
	assert.Equal(t, runID, req.RunID)
	assert.True(t, req.RequireExecutedModel)
	assert.Equal(t, []string{"Read"}, req.Tools)
	assert.Equal(t, int64(0), req.WorkspaceBytes)

	job := d.jobs[0]
	assert.Equal(t, OpType("chat"), job.Op)
	assert.Equal(t, b.Model, job.Binding.Model)
	require.NotNil(t, job.Chat)
	assert.Equal(t, "hi", job.Chat.Prompt)
}

// Validation unchanged: an inner refusal is returned verbatim and nothing dispatches.
func TestRoutingSelectors_InnerValidationErrorSurfacesUnchanged(t *testing.T) {
	inner := &innerChats{}
	d := &recordingDispatcher{}
	_, err := newRoutingChatSelector(inner, d, BuiltInAffinity).ChatProviderFor(subscriptionBinding("refused"))
	assert.Same(t, errInnerRefused, err)
	_, err = newRoutingAgentSelector(inner, d).AgentProviderFor(subscriptionBinding("refused"))
	assert.Same(t, errInnerRefused, err)
	_, err = newRoutingStreamSelector(inner, d).StreamProviderFor(subscriptionBinding("refused"))
	assert.Same(t, errInnerRefused, err)
	assert.Empty(t, d.reqs)
	assert.Len(t, inner.seen, 3, "inner selector must be consulted first for every kind")
}

// Pinned bindings require model evidence even when the request does not ask.
func TestRoutingChatSelector_PinnedBindingRequiresExecutedModel(t *testing.T) {
	d := &recordingDispatcher{out: map[OpType]StepOutput{"chat": {Chat: &engine.ChatResponse{}}}}
	b := subscriptionBinding("claude-sonnet-4-5-20250929")
	b.Pinned = true
	p, err := newRoutingChatSelector(&innerChats{}, d, BuiltInAffinity).ChatProviderFor(b)
	require.NoError(t, err)
	_, err = p.Chat(stepCtx(uuid.New()), engine.ChatRequest{Prompt: "x"})
	require.NoError(t, err)
	assert.True(t, d.reqs[0].RequireExecutedModel)
}

// The routed stream path dispatches Op "stream" and returns the dispatcher's channel.
func TestRoutingChatSelector_StreamChatDispatchesStream(t *testing.T) {
	ch := make(chan engine.ChatChunk, 1)
	ch <- engine.ChatChunk{Token: "t", Done: true}
	close(ch)
	d := &recordingDispatcher{out: map[OpType]StepOutput{"stream": {Stream: ch}}}
	p, err := newRoutingChatSelector(&innerChats{}, d, BuiltInAffinity).ChatProviderFor(subscriptionBinding("claude-sonnet-4-5"))
	require.NoError(t, err)
	got, err := p.StreamChat(stepCtx(uuid.New()), engine.ChatRequest{Prompt: "x"})
	require.NoError(t, err)
	assert.Equal(t, "t", (<-got).Token)
	assert.Equal(t, OpType("stream"), d.reqs[0].Op)
}

// Agents: the effective backend set by the agent executor flows into requirements.
func TestRoutingAgentSelector_DispatchesAgentWithBackendAndTools(t *testing.T) {
	d := &recordingDispatcher{out: map[OpType]StepOutput{"agent": {Agent: &engine.AgentResult{Summary: "routed"}}}}
	b := provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Provider: "claude-cli", Model: "gpt-5.2-codex", Backend: "codex"}
	p, err := newRoutingAgentSelector(&innerChats{}, d).AgentProviderFor(b)
	require.NoError(t, err)
	res, err := p.RunAgent(stepCtx(uuid.New()), engine.AgentMission{Model: "gpt-5.2-codex", Prompt: "do", ToolScope: []string{"Bash"}})
	require.NoError(t, err)
	assert.Equal(t, "routed", res.Summary)
	req := d.reqs[0]
	assert.Equal(t, OpType("agent"), req.Op)
	assert.Equal(t, "codex", req.Backend)
	assert.Equal(t, CredOpenAISubscription, req.Group.Kind)
	assert.Equal(t, []string{"Bash"}, req.Tools)
	require.NotNil(t, d.jobs[0].Agent)
	assert.Equal(t, "do", d.jobs[0].Agent.Prompt)
}

// Interactive agent streams: an empty backend is defaulted BEFORE routing so the
// group is the deployment default's group, not the claude fallback.
func TestWithDefaultAgentBackend_FillsBackendBeforeRouting(t *testing.T) {
	inner := &innerChats{}
	d := &recordingDispatcher{out: map[OpType]StepOutput{"stream": {Stream: make(chan engine.ChatChunk)}}}
	sel := WithDefaultAgentBackend(newRoutingStreamSelector(inner, d), "codex")
	agentB := provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Provider: "claude-cli", Model: "gpt-5.2-codex"}
	p, err := sel.StreamProviderFor(agentB)
	require.NoError(t, err)
	_, err = p.StreamChat(stepCtx(uuid.New()), engine.ChatRequest{Prompt: "x"})
	require.NoError(t, err)
	assert.Equal(t, "codex", d.reqs[0].Backend)
	assert.Equal(t, CredOpenAISubscription, d.reqs[0].Group.Kind)
	assert.Equal(t, "codex", inner.seen[0].Backend, "inner sees the filled backend too")
	assert.Equal(t, "codex", d.jobs[0].Binding.Backend)

	// Chat-kind bindings and explicit backends are never rewritten.
	chatB := subscriptionBinding("claude-sonnet-4-5")
	_, err = sel.StreamProviderFor(chatB)
	require.NoError(t, err)
	assert.Equal(t, "", inner.seen[1].Backend)
	explicit := agentB
	explicit.Backend = "claude-code"
	_, err = sel.StreamProviderFor(explicit)
	require.NoError(t, err)
	assert.Equal(t, "claude-code", inner.seen[2].Backend)
}

// Process: a slot-only CredNone requirement, spec forwarded verbatim, result returned.
func TestRoutedProcessRunner_DispatchesProcess(t *testing.T) {
	d := &recordingDispatcher{out: map[OpType]StepOutput{"process": {Process: &engine.ProcessResult{Stdout: "{}", ExitCode: 0}}}}
	pr := newRoutedProcessRunner(d)
	runID := uuid.New()
	spec := engine.ProcessSpec{Kind: engine.ProcessScript, Command: "/bin/sh", Args: []string{"/s.sh"}, WorkDir: "/ws/r"}
	res, err := pr.RunProcess(stepCtx(runID), spec)
	require.NoError(t, err)
	assert.Equal(t, "{}", res.Stdout)
	req := d.reqs[0]
	assert.Equal(t, OpType("process"), req.Op)
	assert.Equal(t, ModelGroup{Kind: CredNone}, req.Group)
	assert.Equal(t, AffinityBuiltIn, req.Affinity)
	assert.Equal(t, runID, req.RunID)
	require.NotNil(t, d.jobs[0].Process)
	assert.Equal(t, spec, *d.jobs[0].Process)
}

// Error: dispatcher errors (with their outcome) pass through untouched; a missing
// payload in a "successful" output is an error, never a nil dereference.
func TestRoutedProviders_ErrorsAndMissingPayload(t *testing.T) {
	exhausted := WithOutcome(errors.New("quota"), SourceOutcome{Class: OutcomeExhausted})
	d := &recordingDispatcher{err: exhausted, out: map[OpType]StepOutput{}}
	p, err := newRoutingChatSelector(&innerChats{}, d, BuiltInAffinity).ChatProviderFor(subscriptionBinding("claude-sonnet-4-5"))
	require.NoError(t, err)
	_, err = p.Chat(stepCtx(uuid.New()), engine.ChatRequest{})
	require.Error(t, err)
	assert.Equal(t, OutcomeExhausted, ClassifyError(err).Class)

	empty := &recordingDispatcher{out: map[OpType]StepOutput{}}
	p, _ = newRoutingChatSelector(&innerChats{}, empty, BuiltInAffinity).ChatProviderFor(subscriptionBinding("claude-sonnet-4-5"))
	_, err = p.Chat(stepCtx(uuid.New()), engine.ChatRequest{})
	assert.ErrorIs(t, err, errMissingStepOutput)
	_, err = newRoutedProcessRunner(empty).RunProcess(stepCtx(uuid.New()), engine.ProcessSpec{})
	assert.ErrorIs(t, err, errMissingStepOutput)
}

// Edge: an unresolved family on a subscription binding refuses before dispatch.
func TestRoutingChatSelector_RouteAxesAndForbiddenEndpoint(t *testing.T) {
	d := &recordingDispatcher{out: map[OpType]StepOutput{OpChat: {Chat: &engine.ChatResponse{Output: "routed"}}}}
	b := provider.ResolvedBinding{Kind: provider.ProfileKindChat, Provider: "openai", Model: "gpt-5.2", ConnectionRef: "production", BaseURL: "https://models.example/v1", ContextWindow: provider.Context1M, Effort: provider.EffortHigh, Pinned: true, AllowedTools: []string{"Read"}}
	p, err := newRoutingChatSelector(&innerChats{}, d, BuiltInAffinity).ChatProviderFor(b)
	require.NoError(t, err)
	resp, err := p.Chat(stepCtx(uuid.New()), engine.ChatRequest{Prompt: "route me", ToolScope: []string{"Bash"}})
	require.NoError(t, err)
	assert.Equal(t, "routed", resp.Output)
	require.Len(t, d.reqs, 1)
	fingerprint, err := EndpointFingerprint(b)
	require.NoError(t, err)
	assert.Equal(t, fingerprint, d.reqs[0].EndpointFingerprint)
	assert.Equal(t, b.Provider, d.reqs[0].Provider)
	assert.Equal(t, b.ContextWindow, d.reqs[0].ContextWindow)
	assert.Equal(t, b.Effort, d.reqs[0].Effort)
	assert.True(t, d.reqs[0].RequireExecutedModel)
	assert.Equal(t, b.AllowedTools, d.reqs[0].Tools)
	assert.Equal(t, b, d.jobs[0].Binding)

	blocked := &recordingDispatcher{}
	b.Provider, b.BaseURL = "lunaroute", "https://forbidden.example"
	p, err = newRoutingChatSelector(&innerChats{}, blocked, BuiltInAffinity).ChatProviderFor(b)
	require.NoError(t, err)
	_, err = p.Chat(stepCtx(uuid.New()), engine.ChatRequest{})
	require.Error(t, err)
	assert.Empty(t, blocked.jobs)
}

func TestRoutingChatSelector_UnresolvedFamilyFailsClosed(t *testing.T) {
	d := &recordingDispatcher{}
	p, err := newRoutingChatSelector(&innerChats{}, d, BuiltInAffinity).ChatProviderFor(subscriptionBinding("mystery-model-xyz"))
	require.NoError(t, err, "selection still succeeds: validation belongs to the inner selector")
	_, err = p.Chat(stepCtx(uuid.New()), engine.ChatRequest{})
	assert.ErrorIs(t, err, ErrUnresolvedFamily)
	assert.Empty(t, d.reqs)
}

// Edge: nil dispatcher or nil inner panics at construction (fail closed at boot).
func TestAgentToolPolicy_Declaration(t *testing.T) {
	var policy AgentToolPolicy = func(_ provider.ResolvedBinding, scope []string) ([]string, error) { return scope, nil }
	d := &StepDispatcher{}
	d.SetAgentToolPolicy(policy)
	got, err := d.agentToolPolicy(subscriptionBinding("claude-sonnet-4-5"), []string{"Read"})
	require.NoError(t, err)
	assert.Equal(t, []string{"Read"}, got)
}

func TestRoutingConstructors_PanicOnNil(t *testing.T) {
	assert.Panics(t, func() { NewRoutingChatSelector(&innerChats{}, nil, BuiltInAffinity) })
	assert.Panics(t, func() { NewRoutingAgentSelector(&innerChats{}, nil) })
	assert.Panics(t, func() { NewRoutingStreamSelector(&innerChats{}, nil) })
	assert.Panics(t, func() { NewRoutedProcessRunner(nil) })
	assert.Panics(t, func() { newRoutingChatSelector(nil, &recordingDispatcher{}, BuiltInAffinity) })
	assert.Panics(t, func() { WithDefaultAgentBackend(nil, "claude-code") })
}

// A nil affinity function falls back to BuiltInAffinity (the only Phase 1 value).
func TestRoutingChatSelector_NilAffinityIsBuiltIn(t *testing.T) {
	d := &recordingDispatcher{out: map[OpType]StepOutput{"chat": {Chat: &engine.ChatResponse{}}}}
	p, err := newRoutingChatSelector(&innerChats{}, d, nil).ChatProviderFor(subscriptionBinding("claude-sonnet-4-5"))
	require.NoError(t, err)
	_, err = p.Chat(stepCtx(uuid.New()), engine.ChatRequest{})
	require.NoError(t, err)
	assert.Equal(t, AffinityBuiltIn, d.reqs[0].Affinity)
}
