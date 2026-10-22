package renderreview

import (
	"context"
	"fmt"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

func TestReadabilityReviewer_GroupsRequireCompleteActualEvidence(t *testing.T) {
	for _, name := range []string{"branches", "workflows", "nested", "loop"} {
		t.Run(name, func(t *testing.T) {
			fx := newGroupReviewFixture(t, name)
			rv, err := testReviewer().Review(context.Background(), fx.req)
			require.NoError(t, err)
			require.Zero(t, layout.BlockingCount(rv.Findings), "%+v", rv.Findings)
			require.True(t, layout.HasRenderedEvidence(fx.geo, &rv))
			require.Equal(t, fx.req.Rendering.Artifacts, rv.Evidence.Artifacts)
			require.Equal(t, layout.GeometryDigest(fx.geo), rv.Evidence.GeometryDigest)
			require.Equal(t, fx.geo.Contract, rv.Evidence.Contract)
			if n := layout.CrossingCount(fx.geo); n > 0 {
				fs := withCriterion(rv.Findings, layout.CriterionCrossings)
				require.Len(t, fs, 1)
				require.Equal(t, fmt.Sprintf("%d crossing(s) remain in the rendered layout", n), fs[0].Evidence)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		mutate func(*reviewFixture)
	}{
		{"missing ownership", func(f *reviewFixture) { f.req.WorkflowGroups = nil }},
		{"wrong owner", func(f *reviewFixture) {
			f.req.WorkflowGroups.Owner["a"] = workflow.BodyOwner{GroupNodeID: "fan", TaskKey: "first"}
		}},
		{"changed config", func(f *reviewFixture) {
			for _, s := range f.req.WorkflowGroups.Groups {
				s.Config.CompletionRule = workflow.GroupRuleFirstSuccess
			}
		}},
		{"missing scope", func(f *reviewFixture) { f.req.WorkflowGroups.Groups["fan"] = nil }},
		{"wrong parent", func(f *reviewFixture) { f.req.WorkflowGroups.Groups["a"].Parent = nil }},
		{"missing nested ownership", func(f *reviewFixture) { f.req.WorkflowGroups.Groups["fan"].Nested = nil }},
		{"wrong continuation", func(f *reviewFixture) { f.req.WorkflowGroups.Groups["fan"].SuccessTarget = "oops" }},
		{"wrong digest", func(f *reviewFixture) { f.req.Rendering.GeometryDigest = "wrong" }},
		{"missing element", func(f *reviewFixture) {
			f.req.Rendering.Report.GroupElements = f.req.Rendering.Report.GroupElements[1:]
		}},
		{"duplicate element", func(f *reviewFixture) {
			r := &f.req.Rendering.Report
			r.GroupElements = append(r.GroupElements, r.GroupElements[0])
		}},
		{"missing text", func(f *reviewFixture) { r := &f.req.Rendering.Report; r.GroupElements[0].Texts = nil }},
		{"wrong text key", func(f *reviewFixture) { f.req.Rendering.Report.GroupElements[0].Texts[0].Key = "wrong" }},
		{"wrong text", func(f *reviewFixture) { f.req.Rendering.Report.GroupElements[0].Texts[0].Text.Content = "wrong" }},
		{"displaced text", func(f *reviewFixture) { f.req.Rendering.Report.GroupElements[0].Texts[0].Text.Box.Y += 10 }},
		{"clipped text", func(f *reviewFixture) { f.req.Rendering.Report.GroupElements[0].Texts[0].Text.Clipped = true }},
		{"invisible element", func(f *reviewFixture) { f.req.Rendering.Report.GroupElements[0].Visible = false }},
		{"zero border", func(f *reviewFixture) { f.req.Rendering.Report.GroupElements[0].BorderPx = 0 }},
		{"wrong outline", func(f *reviewFixture) { f.req.Rendering.Report.GroupElements[0].BorderColor = "#ffffff" }},
		{"transparent fill", func(f *reviewFixture) {
			for i := range f.req.Rendering.Report.GroupElements {
				f.req.Rendering.Report.GroupElements[i].BackgroundColor = "rgba(0, 0, 0, 0)"
			}
		}},
		{"missing connector", func(f *reviewFixture) { f.req.Rendering.Report.GroupConnectors = nil }},
		{"wrong connector", func(f *reviewFixture) { f.req.Rendering.Report.GroupConnectors[0].ID = "wrong" }},
		{"middle segment", func(f *reviewFixture) { f.req.Rendering.Report.GroupConnectors[0].Segments[0].End.X += 5 }},
		{"attachment", func(f *reviewFixture) { f.req.Rendering.Report.GroupConnectors[0].Start.X += 10 }},
		{"arrow", func(f *reviewFixture) { f.req.Rendering.Report.GroupConnectors[0].HasArrow = false }},
		{"dash", func(f *reviewFixture) { f.req.Rendering.Report.GroupConnectors[0].DashPattern = "none" }},
		{"missing crossing", func(f *reviewFixture) { f.req.Rendering.Report.GroupCrossings = nil }},
		{"crossing separation", func(f *reviewFixture) { f.req.Rendering.Report.GroupCrossings[0].UnderGapVisible = false }},
		{"missing real header", func(f *reviewFixture) { f.req.Rendering.Report.Nodes = f.req.Rendering.Report.Nodes[1:] }},
		{"incomplete tiles", func(f *reviewFixture) { f.req.Rendering.Artifacts = f.req.Rendering.Artifacts[:2] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := "branches"
			if tc.name == "missing crossing" || tc.name == "crossing separation" {
				name = "loop"
			}
			if tc.name == "wrong parent" || tc.name == "missing nested ownership" {
				name = "nested"
			}
			fx := newGroupReviewFixture(t, name)
			tc.mutate(fx)
			rv, err := testReviewer().Review(context.Background(), fx.req)
			require.NoError(t, err)
			require.Positive(t, layout.BlockingCount(rv.Findings), "%+v", rv)
		})
	}
}
