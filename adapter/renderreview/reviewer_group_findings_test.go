package renderreview

import (
	"slices"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/require"
)

// requireFindingsAcceptedByCoordinator mirrors domain/layout reviewFindingsProblem
// for the ids a finding may cite: Coordinator.Run replaces any review that names
// an id outside the candidate graph with a render mismatch and Rendered=false, so
// a group element id, task key or fabricated ordinal would hide the real defect.
func requireFindingsAcceptedByCoordinator(t *testing.T, geo layout.Geometry, findings []layout.Finding) {
	t.Helper()
	nodes := map[string]bool{}
	for _, n := range geo.Nodes {
		nodes[n.ID] = true
	}
	ordinals := map[int]bool{}
	for _, e := range geo.Edges {
		ordinals[e.Ordinal] = true
	}
	for _, f := range findings {
		for _, id := range f.NodeIDs {
			require.True(t, nodes[id], "finding %q cites %q, which is not a graph node: %+v", f.Evidence, id, f)
		}
		for _, o := range f.EdgeOrdinals {
			require.True(t, ordinals[o], "finding %q cites edge ordinal %d, which is not a manifest edge: %+v", f.Evidence, o, f)
		}
		require.True(t, slices.Contains([]layout.Severity{layout.SeverityBlocking, layout.SeverityAdvisory}, f.Severity))
		require.Equal(t, layout.SourceReview, f.Source)
		require.NotEmpty(t, f.Evidence)
	}
}
