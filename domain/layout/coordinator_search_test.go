package layout

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_SearchPlateauThenBetterPrefix(t *testing.T) {
	wf := &workflow.Workflow{}
	for i := 0; i < 12; i++ {
		wf.Nodes = append(wf.Nodes, workflow.Node{ID: fmt.Sprint(i), Type: workflow.NodeTypeScript})
	}
	for _, pair := range [][2]string{{"0", "4"}, {"0", "6"}, {"0", "9"}, {"0", "11"}, {"1", "3"}, {"2", "5"}, {"2", "6"}, {"2", "9"}, {"5", "6"}, {"6", "10"}, {"8", "10"}} {
		wf.Edges = append(wf.Edges, workflow.Edge{Source: pair[0], Target: pair[1]})
	}
	o := DefaultOptions()
	o.CrossingSweeps = 1
	o.LaneSeparation = 60
	o.PortSpacing = 120
	r := newScriptedRenderer()
	rv := &scriptedReviewer{fallback: reviewStep{findings: []Finding{reviewFinding(CriterionAmbiguousCrossing)}}}
	res := newCoord(t, r, rv, WithOptions(o)).Run(context.Background(), Job{Revision: "plateau", Manifest: wf, MaxAttempts: 3}, nil)
	require.Len(t, r.requests, 3)
	require.Len(t, rv.seen, 3)
	assert.Equal(t, 3, res.AttemptsUsed)
	first, second, third := r.requests[0].Geometry, r.requests[1].Geometry, r.requests[2].Geometry
	second.Options = first.Options
	third.Options = first.Options
	assert.Equal(t, first, second, "a repeated presentation is not proof that search is exhausted")
	assert.Less(t, len(third.Crossings), len(second.Crossings), "a later search prefix really improves routed crossings")
	assert.Equal(t, []int{1, 2, 4}, []int{r.options[0].CrossingSweeps, r.options[1].CrossingSweeps, r.options[2].CrossingSweeps})
}

func TestRun_VisualOnlyBranchClippingGrowsSinkBoxes(t *testing.T) {
	job := Job{Revision: "branch", MaxAttempts: 3, Manifest: &workflow.Workflow{Nodes: []workflow.Node{{ID: "a", Type: workflow.NodeTypeScript, Label: "检查\nfull primary text"}, {ID: "b", Type: workflow.NodeTypeScript}}, Edges: []workflow.Edge{{Source: "a", Target: "b", Label: "重试 Unicode branch"}}}}
	r := newScriptedRenderer()
	rv := &scriptedReviewer{steps: map[int]reviewStep{1: {findings: []Finding{reviewFinding(CriterionTextClipped)}}}}
	res := newCoord(t, r, rv).Run(context.Background(), job, nil)
	require.Equal(t, VerdictPassed, res.Verdict)
	require.Len(t, r.requests, 2)
	before, after := r.requests[0].Geometry, r.requests[1].Geometry
	require.NotNil(t, before.Edges[0].Text)
	require.NotNil(t, after.Edges[0].Text)
	assert.Equal(t, before.Edges[0].Text.Box.W+8, after.Edges[0].Text.Box.W)
	assert.Equal(t, before.Edges[0].Text.Box.H+8, after.Edges[0].Text.Box.H)
	assert.Equal(t, before.Nodes[0].Box.H+8, after.Nodes[0].Box.H)
	assert.Equal(t, GeometryDigest(after), res.Best.Review.Evidence.GeometryDigest)
}

func TestNewCoordinator_StageTimeoutDefaults(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second, 17 * time.Second} {
		c := newCoord(t, newScriptedRenderer(), &scriptedReviewer{}, WithStageTimeout(d))
		want := d
		if d <= 0 {
			want = 2 * time.Minute
		}
		assert.Equal(t, want, c.stageTimeout)
	}
}

func TestRun_InvalidOptionsAndMissingRevision(t *testing.T) {
	o := DefaultOptions()
	o.NodeSpacing = -1
	r := newScriptedRenderer()
	res := newCoord(t, r, &scriptedReviewer{}, WithOptions(o)).Run(context.Background(), prReviewJob(t, 3), nil)
	require.NotNil(t, res.Failure)
	assert.Equal(t, StageArranging, res.Failure.Stage)
	assert.Equal(t, 1, res.AttemptsUsed)
	assert.Empty(t, r.rendered)
	job := prReviewJob(t, 3)
	job.Revision = ""
	res = newCoord(t, r, &scriptedReviewer{}).Run(context.Background(), job, nil)
	require.NotNil(t, res.Failure)
	assert.Equal(t, StageArranging, res.Failure.Stage)
	assert.Equal(t, 0, res.AttemptsUsed)
}
