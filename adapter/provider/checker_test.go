package provider_test

import (
	"errors"
	"testing"

	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubResolver is a tiny ProfileResolver for exercising ResolverChecker in
// isolation, including the non-ErrUnknownProfile error path.
type stubResolver struct {
	binding provider.ResolvedBinding
	err     error
}

func (s stubResolver) Resolve(string) (provider.ResolvedBinding, error) {
	return s.binding, s.err
}

// Happy path: a resolvable chat alias yields its kind as a plain string, ok=true.
func TestResolverChecker_ProfileKindChat(t *testing.T) {
	r, err := adapterprovider.NewStaticProfileResolver([]provider.Profile{
		{Alias: "default-chat", Kind: provider.ProfileKindChat, Model: "m-chat"},
		{Alias: "default-agent", Kind: provider.ProfileKindAgent, Model: "m-agent"},
	}, nil)
	require.NoError(t, err)

	c := adapterprovider.NewResolverChecker(r)

	kind, ok := c.ProfileKind("default-chat")
	assert.True(t, ok)
	assert.Equal(t, "chat", kind)
}

// Success: an agent alias maps to the "agent" kind string.
func TestResolverChecker_ProfileKindAgent(t *testing.T) {
	r, err := adapterprovider.NewStaticProfileResolver([]provider.Profile{
		{Alias: "default-agent", Kind: provider.ProfileKindAgent, Model: "m-agent"},
	}, nil)
	require.NoError(t, err)

	c := adapterprovider.NewResolverChecker(r)

	kind, ok := c.ProfileKind("default-agent")
	assert.True(t, ok)
	assert.Equal(t, "agent", kind)
}

// Error: an unknown alias (ErrUnknownProfile) reports ok=false, empty kind.
func TestResolverChecker_UnknownAlias(t *testing.T) {
	r, err := adapterprovider.NewStaticProfileResolver(nil, nil)
	require.NoError(t, err)

	c := adapterprovider.NewResolverChecker(r)

	kind, ok := c.ProfileKind("ghost")
	assert.False(t, ok)
	assert.Equal(t, "", kind)
}

// Edge: any resolver error (not just ErrUnknownProfile) collapses to ok=false.
func TestResolverChecker_GenericResolveError(t *testing.T) {
	c := adapterprovider.NewResolverChecker(stubResolver{err: errors.New("boom")})

	kind, ok := c.ProfileKind("whatever")
	assert.False(t, ok)
	assert.Equal(t, "", kind)
}

// Integration with workflow.Validate: a ResolverChecker backed by a real
// StaticProfileResolver drives the kind-mismatch check end-to-end — an
// agent_call node referencing a chat profile produces PROFILE_KIND_MISMATCH
// via workflow.Validate, and an unknown alias produces UNKNOWN_PROFILE.
// This proves the ResolverChecker satisfies the workflow.ProfileChecker port
// at the boundary that matters: capability validation before any run.
func TestResolverChecker_SatisfiesWorkflowPort(t *testing.T) {
	r, err := adapterprovider.NewStaticProfileResolver([]provider.Profile{
		{Alias: "chat-only", Kind: provider.ProfileKindChat, Model: "m-chat"},
	}, nil)
	require.NoError(t, err)
	c := adapterprovider.NewResolverChecker(r)

	// agent_call bound to a chat profile: kind mismatch must be caught by
	// workflow.Validate via the ResolverChecker — before any executor runs.
	wf := &workflow.Workflow{
		Name: "kind-mismatch-wf",
		Nodes: []workflow.Node{
			{
				ID:         "editor",
				Type:       workflow.NodeTypeAgentCall,
				Entrypoint: true,
				Config: workflow.NodeConfig{
					TemplateFile: "mission.md",
					ModelProfile: "chat-only", // agent_call needs agent kind
				},
			},
		},
	}
	errs := workflow.Validate(wf, nil, c, nil)

	var codes []string
	for _, e := range errs {
		codes = append(codes, e.Code)
	}
	assert.Contains(t, codes, "PROFILE_KIND_MISMATCH",
		"agent_call referencing a chat profile must produce PROFILE_KIND_MISMATCH via workflow.Validate")

	// Confirm the error names the node and describes the mismatch.
	for _, e := range errs {
		if e.Code == "PROFILE_KIND_MISMATCH" {
			assert.Equal(t, "editor", e.NodeID)
			assert.Contains(t, e.Message, "agent", "message must name the required kind")
			assert.Contains(t, e.Message, "chat", "message must name the actual kind")
		}
	}

	// Unknown alias also produces UNKNOWN_PROFILE through the same path.
	wf2 := &workflow.Workflow{
		Name: "unknown-alias-wf",
		Nodes: []workflow.Node{
			{
				ID:         "llm",
				Type:       workflow.NodeTypeLLMCall,
				Entrypoint: true,
				Config: workflow.NodeConfig{
					TemplateFile: "prompt.md",
					ModelProfile: "ghost-alias",
				},
			},
		},
	}
	errs2 := workflow.Validate(wf2, nil, c, nil)

	var codes2 []string
	for _, e := range errs2 {
		codes2 = append(codes2, e.Code)
	}
	assert.Contains(t, codes2, "UNKNOWN_PROFILE",
		"an unknown alias must produce UNKNOWN_PROFILE via workflow.Validate")
}
