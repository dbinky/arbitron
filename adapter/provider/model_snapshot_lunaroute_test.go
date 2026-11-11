package provider_test

import (
	"context"
	"testing"

	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type lrSnapshotRoutes struct {
	t        *testing.T
	profile  provider.Profile
	workflow *workflow.Workflow
}

func (r lrSnapshotRoutes) VersionRequestsForWorkflow(_ context.Context, p provider.Profile, wf *workflow.Workflow) ([]provider.VersionRequest, error) {
	require.Equal(r.t, r.profile, p)
	require.Same(r.t, r.workflow, wf)
	return []provider.VersionRequest{{Model: p.Model, RouteProvider: "lunaroute", ConnectionRef: p.ConnectionRef}}, nil
}

func TestSnapshotLunaRouteCapabilityPolicy(t *testing.T) {
	p := provider.Profile{Alias: "lr", Kind: provider.ProfileKindChat, ConnectionRef: "lr", Model: "glm-5.3", Effort: provider.EffortHigh}
	f := newInvFixture()
	f.addConn(provider.Connection{Name: "lr", Provider: "lunaroute"})
	wf := &workflow.Workflow{Nodes: []workflow.Node{{ID: "call", Type: workflow.NodeTypeLLMCall, Config: workflow.NodeConfig{ModelProfile: p.Alias}}}}
	routes := lrSnapshotRoutes{t: t, profile: p, workflow: wf}
	cache := adapterprovider.NewProfileCacheFromProfiles(p)
	supported := provider.NewCapabilityRules(nil).WithLunaRouteModels(map[string]provider.LunaRouteModel{p.Model: {
		ID: p.Model, Efforts: map[string]provider.EffortFact{"chat": {Known: true, Levels: []provider.Effort{provider.EffortHigh}, Evidence: "ev#high"}},
	}})
	for _, tc := range []struct {
		name   string
		caps   provider.LunaRouteCapabilities
		reason error
	}{
		{"loaded facts", supported, nil}, {"missing port", nil, provider.ErrSelectionPortsMissing},
		{"empty table", provider.NewCapabilityRules(nil), provider.ErrLunaRouteAxisUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap := adapterprovider.NewModelSnapshotter(cache, f.conns, modelidentity.NewVersionResolver(), routes, tc.caps)
			got, err := snap.SnapshotModels(context.Background(), []string{p.Alias}, wf)
			if tc.reason != nil {
				require.ErrorIs(t, err, tc.reason)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, map[string]string{p.Alias: p.Model}, got.Models)
			assert.Empty(t, got.OpaqueRoutes)
		})
	}
}
