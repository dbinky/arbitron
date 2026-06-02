package provider_test

import (
	"testing"

	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubKinder is a tiny ProfileKinder for exercising ResolverChecker in
// isolation: it reports a fixed kind/ok for any alias.
type stubKinder struct {
	kind provider.ProfileKind
	ok   bool
}

func (s stubKinder) Kind(string) (provider.ProfileKind, bool) {
	return s.kind, s.ok
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

// Edge: a kinder reporting ok=false collapses to an empty kind / ok=false.
func TestResolverChecker_KinderReportsUnknown(t *testing.T) {
	c := adapterprovider.NewResolverChecker(stubKinder{ok: false})

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

// CORRECTNESS regression: a VALID profile whose connection is ABSENT must fall
// through to UNKNOWN_CONNECTION, NOT be mislabeled UNKNOWN_PROFILE. Before the
// zero-I/O Kind(alias) fix, ProfileKind ran the full Resolve chain, which failed
// on the missing connection and reported ok=false -> UNKNOWN_PROFILE, hiding the
// real cause. With Kind(alias) the profile is recognised (kind known) and the
// missing connection is surfaced by the connection-existence check instead.
func TestResolverChecker_ValidProfileAbsentConnection_IsUnknownConnection(t *testing.T) {
	profiles := []provider.Profile{
		{Alias: "az", Kind: provider.ProfileKindChat, ConnectionRef: "azure-prod", Model: "m"},
	}
	// Store has NO "azure-prod" connection: the profile is valid but its
	// connection is absent. The ProfileKind path is zero-I/O (Kind over the
	// in-memory registry), so the resolver needs no store here; the absent
	// connection is exercised through the ConnChecker.
	store := &existsStore{present: map[string]bool{}}

	resolver, err := adapterprovider.NewStaticProfileResolver(profiles, nil)
	require.NoError(t, err)
	profileChecker := adapterprovider.NewResolverChecker(resolver)
	connChecker := adapterprovider.NewConnChecker(profiles, store)

	// The capability check alone must NOT report the profile as unknown.
	kind, ok := profileChecker.ProfileKind("az")
	require.True(t, ok, "a valid profile must be recognised even when its connection is absent")
	assert.Equal(t, "chat", kind)

	wf := &workflow.Workflow{
		Name: "absent-conn-wf",
		Nodes: []workflow.Node{
			{
				ID:         "llm",
				Type:       workflow.NodeTypeLLMCall,
				Entrypoint: true,
				Config: workflow.NodeConfig{
					TemplateFile: "prompt.md",
					ModelProfile: "az",
				},
			},
		},
	}
	errs := workflow.Validate(wf, nil, profileChecker, connChecker)

	var codes []string
	for _, e := range errs {
		codes = append(codes, e.Code)
	}
	assert.Contains(t, codes, "UNKNOWN_CONNECTION",
		"a valid profile with an absent connection must produce UNKNOWN_CONNECTION")
	assert.NotContains(t, codes, "UNKNOWN_PROFILE",
		"a valid profile must NOT be mislabeled UNKNOWN_PROFILE when only its connection is absent")
}
