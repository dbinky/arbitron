package provider_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdvanceModelValidatesCandidateCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name        string
		context     provider.ContextWindow
		effort      provider.Effort
		backend     string
		modelEffort []provider.Effort
		want        error
	}{
		{name: "candidate context", context: provider.Context1M, effort: provider.EffortDefault, backend: "claude-code", want: provider.ErrAxisNotApplicable},
		{name: "candidate effort", context: provider.ContextDefault, effort: provider.EffortHigh, backend: "claude-code", want: provider.ErrAxisNotApplicable},
		{name: "invalid context", context: "invalid", effort: provider.EffortDefault, backend: "claude-code", want: provider.ErrInvalidContextWindow},
		{name: "invalid effort", context: provider.ContextDefault, effort: "invalid", backend: "claude-code", want: provider.ErrInvalidEffort},
		{name: "unknown backend", context: provider.ContextDefault, effort: provider.EffortDefault, backend: "unknown", want: provider.ErrUnknownBackend},
		{name: "compatible defaults", context: provider.ContextDefault, effort: provider.EffortDefault, backend: "claude-code"},
		{name: "compatible effort flooring", context: provider.ContextDefault, effort: provider.EffortHigh, backend: "claude-code", modelEffort: []provider.Effort{provider.EffortMedium}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, store, pub, req := advanceFixture(t, "rev-A")
			req.Expected.Kind = provider.ProfileKindAgent
			req.Expected.Backend = tc.backend
			req.Expected.ContextWindow, req.Expected.Effort = tc.context, tc.effort
			levels := tc.modelEffort
			if levels == nil {
				levels = []provider.Effort{} // Explicitly no graded effort, not inheritance.
			}
			rules := provider.NewCapabilityRules(map[string]provider.BackendCapability{
				"claude-code": {
					ContextTiers: []provider.ContextWindow{provider.ContextDefault, provider.Context1M},
					EffortLevels: []provider.Effort{provider.EffortHigh},
					ModelOverrides: map[string]provider.ModelCapability{
						req.Model: {ContextTiers: []provider.ContextWindow{provider.ContextDefault}, EffortLevels: levels},
					},
				},
			})
			gate := provider.NewExecutionGate()
			reads := 0
			gate.SetCurrentRevision(func(context.Context) (string, error) { reads++; return "rev-A", nil })
			svc := provider.NewProfileService(nil, rules, nil)
			next := req.Expected.Clone()
			next.Model = req.Model
			svc.SetModelSelection(modelidentity.NewVersionResolver(), &advanceRoutes{want: next, requests: []provider.VersionRequest{provider.VersionRequestFor(next, "")}})
			svc.SetAdvancementDependencies(store, pub, gate)
			svc.SetPublicationJournal(&checkingPublicationJournal{store: store, publisher: pub})
			adv, err := svc.AdvanceModel(context.Background(), req)
			if tc.want != nil {
				assert.ErrorIs(t, err, tc.want)
				assert.Empty(t, adv)
				assert.Empty(t, store.calls, "invalid replacement must not reach CAS")
				assert.Empty(t, pub.seen, "invalid replacement must not publish")
				assert.Zero(t, reads, "capability validation precedes the execution lease")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, req.Model, adv.ToModel)
			require.Len(t, store.calls, 1)
			assert.Equal(t, req, store.calls[0], "validation must not rewrite the requested axes/configuration")
			assert.Equal(t, [][]provider.Advancement{{adv}}, pub.seen)
		})
	}
}
