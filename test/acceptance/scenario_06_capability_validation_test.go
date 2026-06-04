package acceptance

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	providersadapter "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
)

// TestAcceptance_06_CapabilityValidation proves spec §8 #6: an `agent_call`
// (editor) bound to a chat-only profile fails `workflow_validate` with a clear
// PROFILE_KIND_MISMATCH error BEFORE any execution. The positive control (both
// nodes correctly bound) validates with zero errors.
//
// Requires: Phase 1 capability validation; the worked example's negative binding
// variant. Validation runs over the real filesystem file-checker, the real
// StaticProfileResolver→ResolverChecker, and the real ConnChecker — no engine
// run is started, so no node executes.
func TestAcceptance_06_CapabilityValidation(t *testing.T) {
	wf := loadWorkedExampleGraph(t)

	// --- Negative: editor (agent_call) bound to a CHAT-only profile ---------
	t.Run("agent_call bound to chat profile fails with PROFILE_KIND_MISMATCH", func(t *testing.T) {
		runner := newFakeRunner()
		// The binding miswires editor → a chat profile (wrong kind).
		b := binding{
			name: "miswired",
			profiles: []provider.Profile{
				{Alias: "editor", Kind: provider.ProfileKindChat, ConnectionRef: "", Model: "claude-sonnet-4"},   // WRONG kind
				{Alias: "assessor", Kind: provider.ProfileKindChat, ConnectionRef: "", Model: "claude-sonnet-4"}, // correct
			},
			runner: runner,
		}
		h := newHarness(t, b)

		errs := workflow.Validate(wf, h.fileChecker(),
			providersadapter.NewResolverChecker(h.resolver), h.connChecker)

		codes := codesOf(errs)
		assert.Contains(t, codes, "PROFILE_KIND_MISMATCH",
			"editor bound to a chat-only profile must fail capability validation")

		// The error names the editor node, and NO subprocess was launched (no run
		// happened — validation is a pre-run gate).
		var found bool
		for _, e := range errs {
			if e.Code == "PROFILE_KIND_MISMATCH" && e.NodeID == "editor" {
				found = true
			}
		}
		assert.True(t, found, "the mismatch must be reported on the editor node")
		assert.Empty(t, runner.recordedSpecs(), "no node may execute when validation fails")
	})

	// --- Positive control: correct binding validates cleanly ----------------
	t.Run("correct binding validates with zero errors", func(t *testing.T) {
		runner := newFakeRunner()
		h := newHarness(t, claudeBinding(runner))

		errs := workflow.Validate(wf, h.fileChecker(),
			providersadapter.NewResolverChecker(h.resolver), h.connChecker)

		require.Empty(t, errs, "the worked example validates cleanly under the correct (editor=agent, assessor=chat) binding")
	})
}

// codesOf collects the error codes from a slice of validation errors.
func codesOf(errs []workflow.ValidationError) []string {
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		out = append(out, e.Code)
	}
	return out
}
