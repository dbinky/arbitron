package provider_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/provider"
)

// A candidate source must return proof for the exact supplied start, expose
// actual route requests, and support read-only revalidation of that proof.
var _ interface {
	provider.VersionRequestSource
	Newest(context.Context, provider.Profile) (provider.EligibleCandidate, error)
	ValidateStart(context.Context, provider.Profile) (provider.EligibleCandidate, error)
	RevalidateStart(context.Context, provider.Profile, provider.EligibleCandidate) error
} = (provider.EligibleCandidateSource)(nil)

func selectionProof() provider.EligibleCandidate {
	return provider.EligibleCandidate{
		Model: "claude-sonnet-4-6", CanonicalID: "anthropic/claude-sonnet-4-6", PathRevision: "prospective-path-revision",
		ExecutionRevision: provider.ExecutionRevision{
			ID: "prospective-path-revision", PolicyRevision: 7,
			Connections:    map[string]int64{"gateway": 3},
			WorkflowHashes: map[string]string{"review": "workflow-hash"},
		},
	}
}

func TestEligibleCandidateCloneOwnsExecutionProof(t *testing.T) {
	original := selectionProof()
	cloned := original.Clone()
	require.Equal(t, original, cloned, "model, canonical identity and complete prospective proof survive copying")

	cloned.ExecutionRevision.Connections["gateway"] = 99
	cloned.ExecutionRevision.WorkflowHashes["review"] = "changed"
	assert.Equal(t, int64(3), original.ExecutionRevision.Connections["gateway"])
	assert.Equal(t, "workflow-hash", original.ExecutionRevision.WorkflowHashes["review"])

	var empty provider.EligibleCandidate
	assert.Equal(t, empty, empty.Clone(), "copying must not manufacture missing evidence")
}

func TestPreparedSelectionCloneOwnsProfileAndCandidate(t *testing.T) {
	candidate := selectionProof()
	original := provider.PreparedSelection{
		Profile: provider.Profile{
			Alias: "reviewer", Kind: provider.ProfileKindAgent, Backend: "claude-code",
			ConnectionRef: "gateway", Model: candidate.Model, Revision: 4,
			SelectionMode: provider.SelectionFollowing,
			FollowTarget:  &provider.FollowTarget{Provider: "anthropic", Tier: "sonnet"},
			AllowedTools:  []string{"Read", "Glob"}, ContextWindow: provider.Context1M,
		},
		Candidate: &candidate,
	}
	cloned := original.Clone()
	require.Equal(t, original, cloned)
	require.NotSame(t, original.Candidate, cloned.Candidate)
	cloned.Profile.FollowTarget.Tier = "opus"
	cloned.Profile.AllowedTools[0] = "Bash"
	cloned.Candidate.Model = "claude-sonnet-4-7"
	cloned.Candidate.ExecutionRevision.Connections["gateway"] = 99
	cloned.Candidate.ExecutionRevision.WorkflowHashes["review"] = "changed"
	assert.Equal(t, "sonnet", original.Profile.FollowTarget.Tier)
	assert.Equal(t, []string{"Read", "Glob"}, original.Profile.AllowedTools)
	assert.Equal(t, original.Profile.Model, original.Candidate.Model)
	assert.Equal(t, int64(3), original.Candidate.ExecutionRevision.Connections["gateway"])
	assert.Equal(t, "workflow-hash", original.Candidate.ExecutionRevision.WorkflowHashes["review"])
}

func TestPreparedSelectionClonePreservesPinnedNilCandidate(t *testing.T) {
	original := provider.PreparedSelection{Profile: provider.Profile{
		Alias: "pinned", SelectionMode: provider.SelectionPinned, Model: "gpt-5.2",
		AllowedTools: []string{"Read"},
	}}
	cloned := original.Clone()
	require.Equal(t, original, cloned)
	assert.Nil(t, cloned.Candidate, "pinned version validation does not acquire following proof")
	cloned.Profile.AllowedTools[0] = "Bash"
	assert.Equal(t, []string{"Read"}, original.Profile.AllowedTools)

	var empty provider.PreparedSelection
	assert.Equal(t, empty, empty.Clone())
}
