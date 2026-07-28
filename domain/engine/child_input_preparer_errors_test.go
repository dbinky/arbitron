package engine_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// prepareWith runs Prepare against a provider that returns the given raw text.
func prepareWith(t *testing.T, raw string) (map[string]any, error) {
	t.Helper()
	prov := &mockChatProvider{
		callFn: func(_ context.Context, req engine.ChatRequest) (*engine.ChatResponse, error) {
			return &engine.ChatResponse{Output: raw, Model: req.Model}, nil
		},
	}
	p := engine.NewChildInputPreparer(chatSelector(prov), chatResolver("m"))
	return p.Prepare(context.Background(), engine.ChildInputRequest{
		NodeID: "spawn-1", ChildWorkflow: "child-pipeline", Inputs: childFields(),
		ParentContext: map[string]any{"repository": "arbitron/arbitron"},
		ProfileAlias:  "chat-default",
	})
}

// ---------- failure: contract not satisfied ----------

func TestChildInputPreparer_MissingRequiredInput_ErrorsAndReturnsNoPartial(t *testing.T) {
	values, err := prepareWith(t, `{"repo":"arbitron/arbitron"}`)
	require.Error(t, err)
	assert.Nil(t, values, "no PARTIAL map may be returned alongside the error: the caller must "+
		"not be able to spawn with half a payload")
	assert.ErrorIs(t, err, engine.ErrChildContractUnsatisfied)
	assert.Contains(t, err.Error(), "branch", "the error must name the missing required input")
	assert.Contains(t, err.Error(), "child-pipeline", "the error must name the child workflow")
	assert.Contains(t, err.Error(), "spawn-1", "the error must name the offending node")
}

func TestChildInputPreparer_NullRequiredInput_TreatedAsMissing(t *testing.T) {
	values, err := prepareWith(t, `{"repo":"r","branch":null}`)
	require.Error(t, err)
	assert.Nil(t, values)
	assert.ErrorIs(t, err, engine.ErrChildContractUnsatisfied)
	assert.Contains(t, err.Error(), "branch")
}

func TestChildInputPreparer_JSONArrayResponse_Errors(t *testing.T) {
	values, err := prepareWith(t, `["repo","branch"]`)
	require.Error(t, err)
	assert.Nil(t, values)
	assert.Contains(t, err.Error(), "not a JSON object")
}

func TestChildInputPreparer_BareStringResponse_Errors(t *testing.T) {
	values, err := prepareWith(t, `"just some prose with no braces at all"`)
	require.Error(t, err)
	assert.Nil(t, values)
}

func TestChildInputPreparer_EmptyResponse_Errors(t *testing.T) {
	values, err := prepareWith(t, ``)
	require.Error(t, err)
	assert.Nil(t, values)
	assert.Contains(t, err.Error(), "no JSON object found")
}

// The excerpt lets an operator diagnose the failure from the trace without
// re-running, and it is BOUNDED so a whole model response never bloats a
// node-execution record.
func TestChildInputPreparer_UnparseableResponse_IncludesBoundedExcerpt(t *testing.T) {
	long := strings.Repeat("x", 5000)
	_, err := prepareWith(t, long)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "truncated", "a long unparseable response must be truncated in the error")
	assert.Less(t, len(err.Error()), 1200, "the error message must stay bounded, got %d bytes", len(err.Error()))
}

// ---------- error: provider / profile / posture ----------

func TestChildInputPreparer_ProviderError_Wrapped(t *testing.T) {
	sentinel := errors.New("upstream exploded")
	prov := &mockChatProvider{
		callFn: func(context.Context, engine.ChatRequest) (*engine.ChatResponse, error) {
			return nil, sentinel
		},
	}
	p := engine.NewChildInputPreparer(chatSelector(prov), chatResolver("m"))

	values, err := p.Prepare(context.Background(), engine.ChildInputRequest{
		NodeID: "spawn-1", ChildWorkflow: "c", Inputs: childFields(),
		ParentContext: map[string]any{}, ProfileAlias: "chat-default",
	})
	require.Error(t, err)
	assert.Nil(t, values)
	assert.ErrorIs(t, err, sentinel, "the provider error must be wrapped, not replaced")
}

func TestChildInputPreparer_UnknownProfile_Errors(t *testing.T) {
	prov := &mockChatProvider{
		callFn: func(context.Context, engine.ChatRequest) (*engine.ChatResponse, error) {
			t.Fatal("the provider must not be called when the profile cannot be resolved")
			return nil, nil
		},
	}
	p := engine.NewChildInputPreparer(chatSelector(prov), chatResolver("m"))

	values, err := p.Prepare(context.Background(), engine.ChildInputRequest{
		NodeID: "spawn-1", ChildWorkflow: "c", Inputs: childFields(),
		ParentContext: map[string]any{}, ProfileAlias: "does-not-exist",
	})
	require.Error(t, err)
	assert.Nil(t, values)
	assert.ErrorIs(t, err, provider.ErrUnknownProfile)
}

// validateModelProfiles lets a spawn node omit model_profile entirely, on the
// theory that a child with zero declared inputs never reaches this call. If
// the node's child DOES declare inputs, req.ProfileAlias arrives empty and
// this must fail closed with a named error — never silently resolve some
// default binding, which would turn the author-time carve-out into an
// unguarded escalation path.
//
// The error is raised HERE, explicitly, rather than by letting the resolver
// reject the empty alias: `unknown profile: ""` reads like a registry outage,
// while this names the node, the child, and the missing config field.
func TestChildInputPreparer_EmptyProfileAliasWithDeclaredInputs_FailsClosed(t *testing.T) {
	prov := &mockChatProvider{
		callFn: func(context.Context, engine.ChatRequest) (*engine.ChatResponse, error) {
			t.Fatal("the provider must not be called when the profile alias is empty")
			return nil, nil
		},
	}
	p := engine.NewChildInputPreparer(chatSelector(prov), chatResolver("m"))

	values, err := p.Prepare(context.Background(), engine.ChildInputRequest{
		NodeID: "spawn-1", ChildWorkflow: "c", Inputs: childFields(),
		ParentContext: map[string]any{}, ProfileAlias: "",
	})
	require.Error(t, err)
	assert.Nil(t, values)
	assert.Contains(t, err.Error(), "spawn-1", "the error must name the offending node")
	assert.Contains(t, err.Error(), "c", "and the child whose contract went unsatisfied")
	assert.Contains(t, err.Error(), "model_profile", "and the field to set")
}

// The binding-kind guard must fire BEFORE any provider is selected, so a
// tool-enabled (agent) provider is never constructed for what must be a
// tool-suppressed reasoning call.
func TestChildInputPreparer_AgentBinding_FailsClosedBeforeSelection(t *testing.T) {
	selected := false
	sel := &spySelector{onSelect: func() { selected = true }}
	agentRes := newFakeResolver(provider.Profile{
		Alias: "agent-profile", Kind: provider.ProfileKindAgent, Model: "sonnet",
	})
	p := engine.NewChildInputPreparer(sel, agentRes)

	values, err := p.Prepare(context.Background(), engine.ChildInputRequest{
		NodeID: "spawn-1", ChildWorkflow: "c", Inputs: childFields(),
		ParentContext: map[string]any{}, ProfileAlias: "agent-profile",
	})
	require.Error(t, err)
	assert.Nil(t, values)
	assert.ErrorIs(t, err, engine.ErrBindingKindMismatch)
	assert.False(t, selected, "no provider may be selected once the posture guard has failed")
}

func TestChildInputPreparer_SelectorError_Wrapped(t *testing.T) {
	sentinel := errors.New("no backend for binding")
	p := engine.NewChildInputPreparer(staticChatSelector{err: sentinel}, chatResolver("m"))

	values, err := p.Prepare(context.Background(), engine.ChildInputRequest{
		NodeID: "spawn-1", ChildWorkflow: "c", Inputs: childFields(),
		ParentContext: map[string]any{}, ProfileAlias: "chat-default",
	})
	require.Error(t, err)
	assert.Nil(t, values)
	assert.ErrorIs(t, err, sentinel)
}

// ---------- edge cases ----------

func TestChildInputPreparer_CodeFencedResponseParses(t *testing.T) {
	values, err := prepareWith(t, "```json\n{\"repo\":\"r\",\"branch\":\"b\"}\n```")
	require.NoError(t, err, "a fenced response is the documented real-world failure mode and must parse")
	assert.Equal(t, "r", values["repo"])
}

func TestChildInputPreparer_LeadingProseResponseParses(t *testing.T) {
	values, err := prepareWith(t,
		"Here are the startup values you asked for:\n\n{\"repo\":\"r\",\"branch\":\"b\"}\n\nHope that helps!")
	require.NoError(t, err)
	assert.Equal(t, "b", values["branch"])
}

func TestChildInputPreparer_LeadingWhitespaceResponseParses(t *testing.T) {
	values, err := prepareWith(t, "\n\n   {\"repo\":\"r\",\"branch\":\"b\"}")
	require.NoError(t, err)
	assert.Equal(t, "r", values["repo"])
}

// An empty parent context with a child that requires inputs fails CLEANLY
// rather than producing fabricated-looking empty strings.
func TestChildInputPreparer_EmptyParentContext_FailsCleanly(t *testing.T) {
	prov := &mockChatProvider{
		callFn: func(_ context.Context, req engine.ChatRequest) (*engine.ChatResponse, error) {
			assert.Contains(t, req.Prompt, "(none)", "an empty parent context must be stated explicitly")
			return &engine.ChatResponse{Output: `{}`}, nil
		},
	}
	p := engine.NewChildInputPreparer(chatSelector(prov), chatResolver("m"))

	values, err := p.Prepare(context.Background(), engine.ChildInputRequest{
		NodeID: "spawn-1", ChildWorkflow: "c", Inputs: childFields(),
		ParentContext: map[string]any{}, ProfileAlias: "chat-default",
	})
	require.Error(t, err)
	assert.Nil(t, values)
	assert.ErrorIs(t, err, engine.ErrChildContractUnsatisfied)
}

// A very large parent context must not panic and must still be rendered.
func TestChildInputPreparer_LargeParentContext_DoesNotPanic(t *testing.T) {
	big := map[string]any{}
	for i := 0; i < 2000; i++ {
		big[string(rune('a'+i%26))+strings.Repeat("k", i%50)] = strings.Repeat("v", 200)
	}
	var got engine.ChatRequest
	prov := &mockChatProvider{
		callFn: func(_ context.Context, req engine.ChatRequest) (*engine.ChatResponse, error) {
			got = req
			return &engine.ChatResponse{Output: `{"repo":"r","branch":"b"}`}, nil
		},
	}
	p := engine.NewChildInputPreparer(chatSelector(prov), chatResolver("m"))

	values, err := p.Prepare(context.Background(), engine.ChildInputRequest{
		NodeID: "spawn-1", ChildWorkflow: "c", Inputs: childFields(),
		ParentContext: big, ProfileAlias: "chat-default",
	})
	require.NoError(t, err)
	assert.Equal(t, "r", values["repo"])
	assert.NotEmpty(t, got.Prompt)
}

// A non-string Output under a schema is an adapter contract violation and is
// reported, never silently passed through.
func TestChildInputPreparer_NonStringProviderOutput_Errors(t *testing.T) {
	prov := &mockChatProvider{
		callFn: func(context.Context, engine.ChatRequest) (*engine.ChatResponse, error) {
			return &engine.ChatResponse{Output: map[string]any{"repo": "r"}}, nil
		},
	}
	p := engine.NewChildInputPreparer(chatSelector(prov), chatResolver("m"))

	values, err := p.Prepare(context.Background(), engine.ChildInputRequest{
		NodeID: "spawn-1", ChildWorkflow: "c", Inputs: childFields(),
		ParentContext: map[string]any{}, ProfileAlias: "chat-default",
	})
	require.Error(t, err)
	assert.Nil(t, values)
	assert.Contains(t, err.Error(), "expected raw string from provider")
}

// spySelector records whether ChatProviderFor was reached. It is used to prove
// the posture guard fires BEFORE provider selection.
type spySelector struct {
	onSelect func()
}

func (s *spySelector) ChatProviderFor(provider.ResolvedBinding) (engine.ChatProvider, error) {
	if s.onSelect != nil {
		s.onSelect()
	}
	return &mockChatProvider{
		callFn: func(context.Context, engine.ChatRequest) (*engine.ChatResponse, error) {
			return &engine.ChatResponse{Output: `{"repo":"r","branch":"b"}`}, nil
		},
	}, nil
}

var _ engine.ChatProviderSelector = (*spySelector)(nil)

// A child declaring ZERO inputs never makes a preparation call, so a missing
// model_profile is not an error at all on that path. This is why the check
// cannot live in author-time validation.
func TestChildInputPreparer_NoInputsNeedsNoModelProfile(t *testing.T) {
	calls := 0
	var got engine.ChatRequest
	prov := respondWith(`{}`, &got, &calls)
	p := engine.NewChildInputPreparer(chatSelector(prov), chatResolver("gpt-4o"))

	values, err := p.Prepare(context.Background(), engine.ChildInputRequest{
		NodeID:        "spawn-1",
		ChildWorkflow: "child-pipeline",
		Inputs:        nil,
		ParentContext: map[string]any{},
		ProfileAlias:  "",
	})

	require.NoError(t, err)
	assert.Empty(t, values)
	assert.Zero(t, calls, "a child with no declared inputs must cost no provider call")
}
