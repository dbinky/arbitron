package layout

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGroupGeometry_CapturedOwnershipIsRequiredForValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Graph)
	}{
		{"nil", func(g *Graph) { g.Groups = nil }},
		{"empty", func(g *Graph) { g.Groups = &workflow.GroupGraph{} }},
		{"wrong body owner", func(g *Graph) {
			g.Groups.Owner["a"] = workflow.BodyOwner{GroupNodeID: "fan", TaskKey: "first"}
		}},
		{"different saved config", func(g *Graph) { g.Groups.Groups["fan"].Config.CompletionRule = workflow.GroupRuleFirstSuccess }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, err := BuildGraph(capturedGroupWorkflow(workflow.GroupFormBranches, true))
			require.NoError(t, err)
			m := measured(t, g)
			geo, err := Arrange(context.Background(), g, m, DefaultContract(), DefaultOptions())
			require.NoError(t, err)
			tc.edit(&g)
			err = geo.Validate(g)
			require.ErrorIs(t, err, ErrInvalidGroups)
			assert.ErrorIs(t, err, ErrInvalidGeometry, "stored-value callers retain their existing typed geometry error")
			assert.False(t, Compatible(geo, g), "compatibility must not bless candidate geometry with missing/stale group input")
			findings := Preflight(g, geo, m)
			require.Len(t, findings, 1, "invalid ownership stops semantic checks before ordinary boxes can be mistaken for valid group data")
			assert.Equal(t, CriterionCoverage, findings[0].Criterion)
			assert.Equal(t, SeverityBlocking, findings[0].Severity)
			assert.Contains(t, findings[0].Evidence, ErrInvalidGroups.Error())
		})
	}
}
