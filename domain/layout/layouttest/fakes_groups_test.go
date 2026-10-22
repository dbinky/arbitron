package layouttest_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

func TestFakeRendererAndReviewerGroupArguments(t *testing.T) {
	for _, name := range []string{"branches", "workflows", "nested", "loop"} {
		t.Run(name, func(t *testing.T) {
			wf, geo, _ := layouttest.CapturedGroup(t, name)
			g, err := layout.BuildGraph(wf)
			require.NoError(t, err)
			types := map[string]string{}
			for _, n := range wf.Nodes {
				types[n.ID] = string(n.Type)
			}
			req := layout.NewRenderRequest("saved-group", 2, geo, types)
			req.WorkflowGroups = g.Groups
			renderer := layouttest.NewFakeRenderer()
			reviewer := layouttest.NewFakeReviewer()
			out, err := renderer.Render(context.Background(), req)
			require.NoError(t, err)
			require.Len(t, out.Report.GroupElements, len(geo.GroupElements))
			require.NotEmpty(t, out.Report.GroupElements)
			for i, e := range geo.GroupElements {
				require.Equal(t, e.ID, out.Report.GroupElements[i].ID)
				require.Len(t, out.Report.GroupElements[i].Texts, len(e.Texts))
				for j, tx := range e.Texts {
					require.Equal(t, tx.Text, out.Report.GroupElements[i].Texts[j].Text.Content)
				}
			}
			for i, e := range geo.GroupConnectors {
				require.Equal(t, e.ID, out.Report.GroupConnectors[i].ID)
				require.Len(t, out.Report.GroupConnectors[i].ArrowPoints, 3)
			}
			require.Len(t, out.Report.GroupCrossings, len(geo.GroupCrossings))
			var names []string
			for _, s := range out.Report.States {
				names = append(names, s.State)
			}
			require.Equal(t, append(append([]string(nil), layout.RuntimeStates...), layout.GroupProbeNames(g.Groups)...), names)
			rv, err := reviewer.Review(context.Background(), layout.ReviewRequest{Revision: req.Revision, Attempt: req.Attempt, Geometry: geo, WorkflowGroups: g.Groups, Rendering: out})
			require.NoError(t, err)
			require.True(t, layout.HasRenderedEvidence(geo, &rv))
			for _, mutate := range []func(*layout.ReviewRequest){
				func(q *layout.ReviewRequest) { q.WorkflowGroups = nil },
				func(q *layout.ReviewRequest) { q.WorkflowGroups = &workflow.GroupGraph{} },
				func(q *layout.ReviewRequest) { q.Rendering.Report.GroupElements = nil },
				func(q *layout.ReviewRequest) { q.Rendering.Report.GroupConnectors[0].ID = "wrong" },
				func(q *layout.ReviewRequest) {
					q.Rendering.Report.States = q.Rendering.Report.States[:len(layout.RuntimeStates)]
				},
			} {
				out, err = renderer.Render(context.Background(), req)
				require.NoError(t, err)
				q := layout.ReviewRequest{Revision: req.Revision, Attempt: req.Attempt, Geometry: geo, WorkflowGroups: g.Groups, Rendering: out}
				mutate(&q)
				_, err = reviewer.Review(context.Background(), q)
				require.Error(t, err)
			}
			req.WorkflowGroups = nil
			_, err = renderer.Render(context.Background(), req)
			require.Error(t, err)
			req.WorkflowGroups = g.Groups
			renderer.OnRender = func(q layout.RenderRequest) {
				q.WorkflowGroups.Groups[g.Groups.Order[0]].Config.Tasks[0].Key = "mutated hook"
			}
			_, err = renderer.Render(context.Background(), req)
			require.NoError(t, err)
			require.NotEqual(t, "mutated hook", g.Groups.Groups[g.Groups.Order[0]].Config.Tasks[0].Key, "hooks cannot mutate captured input")
		})
	}
}
