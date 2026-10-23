package provider_test

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// unknownProbe never confirms: only stored proof can authorize advancement.
type unknownProbe struct{}

func (unknownProbe) Check(_ context.Context, path modelcatalog.ExecutionPath, e modelidentity.Entry) (modelcatalog.UsabilityEvidence, error) {
	return modelcatalog.UsabilityEvidence{CanonicalID: e.CanonicalID, PathKey: path.Key, ConfigRevision: path.ConfigRevision, State: modelcatalog.UsabilityUnknown}, nil
}

func groupResolverWorkflow() *workflow.Workflow {
	return &workflow.Workflow{Name: "g", Nodes: []workflow.Node{{ID: "grp", Type: workflow.NodeTypeParallelGroup, Config: workflow.NodeConfig{
		Group: &workflow.GroupConfig{GatherMode: workflow.GroupGatherResultsAndFiles, ResolverProfile: "resolver"}}}}}
}

// A parallel_group's resolverProfile is consumed by an interactive agent session
// (GroupResolutionBridge, then human turns). The inventory must give a
// group-only alias that interactive route, so batch-only proof cannot advance
// it; proof on every consuming path does. Covers saved manifests and legacy
// active-run snapshots, which both reach addConsumers.
func TestInventoryGroupResolverIsInteractiveConsumer(t *testing.T) {
	sonnet := func(native, order string) modelidentity.Entry {
		return modelidentity.Entry{CanonicalID: "anthropic/" + native, Provider: "anthropic", NativeID: native, SpecificModel: native,
			Tier: "sonnet", OrderKey: order, Immutable: true, PolicyRevision: 3}
	}
	current, next := sonnet("claude-sonnet-4-5", "4.5"), sonnet("claude-sonnet-4-6", "4.6")
	resolver := provider.Profile{Alias: "resolver", Kind: provider.ProfileKindAgent, Model: current.SpecificModel, Revision: 1,
		SelectionMode: provider.SelectionFollowing, FollowTarget: &provider.FollowTarget{Provider: "anthropic", Tier: "sonnet"}}

	for _, source := range []string{"manifest", "active run"} {
		t.Run(source, func(t *testing.T) {
			f := newInvFixture()
			f.profiles.profiles = []provider.Profile{resolver}
			if source == "manifest" {
				f.wfs = &invWorkflows{scopes: []workflow.Scope{workflow.Shared()}, byScope: map[workflow.Scope][]modelcatalog.WorkflowManifest{
					workflow.Shared(): {{Scope: workflow.Shared(), Name: "g", Content: []byte("g"), Workflow: groupResolverWorkflow()}}}}
			} else {
				snap := runSnapshot()
				snap.WorkflowSnapshot["nodes"] = []any{map[string]any{"id": "grp", "type": "parallel_group",
					"config": map[string]any{"group": map[string]any{"gatherMode": "results_and_files", "resolverProfile": "resolver"}}}}
				f.runs.snaps = append(f.runs.snaps, snap)
			}
			snap, err := f.build(t).Snapshot(context.Background())
			require.NoError(t, err)

			modes := map[string]modelcatalog.ExecutionPath{}
			for _, p := range snap.Paths {
				for _, a := range p.Aliases {
					if a == "resolver" {
						modes[p.Mode] = p
					}
				}
			}
			require.Contains(t, modes, modelcatalog.ExecutionModeInteractive, "the resolver's interactive route must be inventoried")
			require.Contains(t, modes, modelcatalog.ExecutionModeBatch)

			proof := func(p modelcatalog.ExecutionPath) modelcatalog.UsabilityEvidence {
				return modelcatalog.UsabilityEvidence{CanonicalID: next.CanonicalID, PathKey: p.Key, ConfigRevision: p.ConfigRevision,
					NativeModel: next.SpecificModel, ProofRef: "proof", CheckedAt: time.Unix(1, 0), State: modelcatalog.UsabilityConfirmed}
			}
			in := modelcatalog.SelectionInput{RefreshID: "r", Profiles: []provider.Profile{resolver},
				Catalog: modelcatalog.CatalogSnapshot{Slices: []modelcatalog.CatalogSlice{{Provider: "anthropic", Status: modelcatalog.SliceStatusOK,
					Entries: []modelidentity.Entry{current, next}}}},
				Known: []modelidentity.Entry{current, next}, Paths: snap,
				Stored: []modelcatalog.UsabilityEvidence{proof(modes[modelcatalog.ExecutionModeBatch])}}
			sel := (&modelcatalog.AdvanceSelector{Probe: unknownProbe{}}).Select(context.Background(), in)
			assert.Empty(t, sel.Advances, "batch-only proof must not advance a group-only resolver alias")

			in.Stored = append(in.Stored, proof(modes[modelcatalog.ExecutionModeInteractive]))
			sel = (&modelcatalog.AdvanceSelector{Probe: unknownProbe{}}).Select(context.Background(), in)
			require.Len(t, sel.Advances, 1, "proof on every consuming path reaches selection")
			assert.Equal(t, next.SpecificModel, sel.Advances[0].Model)
		})
	}
}
