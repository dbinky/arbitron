package layout

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func crossingGroupWorkflow() *workflow.Workflow {
	w := capturedGroupWorkflow("branches", false)
	w.Nodes = append(w.Nodes, workflow.Node{ID: "a2", Type: "script"}, workflow.Node{ID: "a3", Type: "script"}, workflow.Node{ID: "sink", Type: "script"})
	w.Edges = append(w.Edges, workflow.Edge{Source: "a", Target: "a2"}, workflow.Edge{Source: "a2", Target: "a3"}, workflow.Edge{Source: "a3", Target: "a"}, workflow.Edge{Source: "a", Target: "sink"})
	for i := range w.Nodes {
		if w.Nodes[i].Type == "" {
			w.Nodes[i].Type = "script"
		}
	}
	return w
}

func TestCoordinator_GroupCrossingsAffectBestScore(t *testing.T) {
	for _, renderFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(renderFailure), func(t *testing.T) {
			renderer := newScriptedRenderer()
			if renderFailure {
				renderer.renderErr[1] = errors.New("known render failure")
			}
			result := newCoord(t, renderer, &scriptedReviewer{}).Run(context.Background(), Job{Revision: "mixed", Manifest: crossingGroupWorkflow(), MaxAttempts: 1}, nil)
			require.NotNil(t, result.Best)
			geo := result.Best.Geometry
			require.NotEmpty(t, geo.GroupCrossings, "exercise real produced mixed crossings, not an empty score")
			assert.Equal(t, len(geo.Crossings)+len(geo.GroupCrossings), result.Best.Score.Crossings)
			if renderFailure {
				require.NotNil(t, result.Failure)
				assert.Nil(t, result.Best.Review)
			} else {
				require.NotNil(t, result.Best.Review)
			}
		})
	}
}

func TestCoordinator_GroupCancellation(t *testing.T) {
	for _, stage := range []Stage{"entry", StageArranging, StageRendering, StageReviewing} {
		t.Run(string(stage), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if stage == "entry" {
				cancel()
			}
			result := newCoord(t, newScriptedRenderer(), &scriptedReviewer{}).Run(ctx, Job{Revision: "cancelled", Manifest: crossingGroupWorkflow(), MaxAttempts: 5}, func(p Progress) {
				if p.Stage == stage {
					cancel()
				}
			})
			require.True(t, result.Cancelled)
			require.Equal(t, VerdictFailed, result.Verdict)
			require.NotNil(t, result.Failure)
			want := stage
			if stage == "entry" {
				want = StageArranging
			}
			assert.Equal(t, want, result.Failure.Stage)
			if stage == "entry" {
				assert.Equal(t, "context canceled", result.Failure.Reason)
				assert.Zero(t, result.AttemptsUsed)
			} else {
				assert.Equal(t, "cancelled: context canceled", result.Failure.Reason)
				assert.Equal(t, 1, result.AttemptsUsed)
			}
			if stage == "entry" || stage == StageArranging {
				assert.Nil(t, result.Best)
			} else {
				require.NotNil(t, result.Best)
				require.Equal(t, FormatV2, result.Best.Geometry.Format)
				require.NotEmpty(t, result.Best.Geometry.GroupElements)
				require.True(t, result.Unreviewed)
			}
		})
	}
}

func TestGroupCrossings_CandidateRanking(t *testing.T) {
	a := &Candidate{Attempt: 1, Geometry: Geometry{Crossings: []Crossing{{}}, GroupCrossings: []GroupCrossing{{}, {}}}}
	b := &Candidate{Attempt: 2, Geometry: Geometry{Crossings: []Crossing{{}}, GroupCrossings: []GroupCrossing{{}}}}
	a.Score = unreviewedScore(a)
	b.Score = unreviewedScore(b)
	assert.Same(t, b, better(a, b), "later candidate with fewer mixed crossings must win")
}

// Paired row 5: retention across generations ranks the persisted total, so a
// retry differing only in mixed crossings must win or lose on them alone.
func TestGroupCrossings_StoredRanking(t *testing.T) {
	fewer := Geometry{Crossings: []Crossing{{}}, GroupCrossings: []GroupCrossing{{}}}
	more := Geometry{Crossings: []Crossing{{}}, GroupCrossings: []GroupCrossing{{}, {}}}
	stored := func(attempt int, geo Geometry) StoredResult {
		return StoredResult{Geometry: geo, Verdict: VerdictNeedsAttention, Score: ScoreOf(attempt, nil, CrossingCount(geo))}
	}
	// Attempt order favours the opposite outcome, so only crossings decide.
	assert.True(t, storedBetter(stored(1, more), stored(2, fewer)), "fewer mixed crossings replace the best")
	assert.False(t, storedBetter(stored(2, fewer), stored(1, more)), "more mixed crossings never replace the best")
	assert.False(t, storedBetter(stored(1, fewer), stored(1, fewer)), "an equal retry keeps the earlier best")
}

type groupRequestRenderer struct {
	*scriptedRenderer
	t    *testing.T
	want *workflow.GroupGraph
}

func (r groupRequestRenderer) Render(ctx context.Context, req RenderRequest) (Rendering, error) {
	r.t.Helper()
	require.Equal(r.t, r.want, req.WorkflowGroups, "render receives the captured config and immediate ownership")
	if req.WorkflowGroups != nil {
		mutateRequestGroups(req.WorkflowGroups)
	}
	return r.scriptedRenderer.Render(ctx, req)
}

type groupRequestReviewer struct {
	t    *testing.T
	want *workflow.GroupGraph
	err  error
}

func (r groupRequestReviewer) Review(_ context.Context, req ReviewRequest) (Review, error) {
	r.t.Helper()
	require.Equal(r.t, r.want, req.WorkflowGroups, "review receives an independent captured map, not the renderer's mutated input")
	if req.WorkflowGroups != nil {
		mutateRequestGroups(req.WorkflowGroups)
	}
	if req.Attempt == 1 {
		// Demand a refinement without manufacturing any rendered evidence.
		return Review{Findings: []Finding{reviewFinding(CriterionTextClipped)}}, nil
	}
	return Review{}, r.err
}

func mutateRequestGroups(g *workflow.GroupGraph) {
	scope := g.Groups[g.Order[0]]
	scope.Config.Tasks[0].Inputs["nested"].(map[string]any)["values"].([]any)[0] = "mutated"
	scope.Config.Tasks[0].Key = "mutated"
	for key := range scope.Bodies {
		scope.Bodies[key][0] = "mutated"
	}
	if len(scope.Nested) > 0 {
		nested := g.Groups[scope.Nested[0]]
		nested.Parent.TaskKey = "mutated"
		nested.Config.Tasks[0].Inputs["nested"].(map[string]any)["values"].([]any)[0] = "mutated"
		scope.Nested[0] = "mutated"
	}
	g.Order[0] = "mutated"
	g.Owner["done"] = workflow.BodyOwner{GroupNodeID: "mutated"}
	delete(g.Groups, scope.NodeID)
}

func TestRun_WorkflowGroupsAreCapturedAndIndependentlyFed(t *testing.T) {
	for _, tc := range []struct {
		name, form string
		nested     bool
	}{
		{"branches", workflow.GroupFormBranches, false},
		{"workflows", workflow.GroupFormWorkflows, false},
		{"nested", workflow.GroupFormBranches, true},
		{"ordinary", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wf := capturedGroupWorkflow(tc.form, tc.nested)
			if tc.form == "" {
				wf = &workflow.Workflow{Nodes: []workflow.Node{{ID: "ordinary", Type: workflow.NodeTypeTransform}}}
			}
			for i := range wf.Nodes {
				if wf.Nodes[i].Type == "" {
					wf.Nodes[i].Type = workflow.NodeTypeTransform
				}
			}
			var want *workflow.GroupGraph
			if tc.form != "" {
				var findings []workflow.ValidationError
				want, findings = workflow.AnalyzeGroups(wf)
				require.Empty(t, findings)
			}
			stop := errors.New("protocol test stops at the reviewer")
			renderer := groupRequestRenderer{scriptedRenderer: newScriptedRenderer(), t: t, want: want}
			reviewer := groupRequestReviewer{t: t, want: want, err: stop}
			result := newCoord(t, renderer, reviewer).Run(context.Background(), Job{Revision: "saved-group", Manifest: wf, MaxAttempts: 2}, func(p Progress) {
				if p.Stage == StageArranging && want != nil {
					// A later authoring edit must not replace already captured input.
					wf.Nodes[1].Config.Group.Tasks[0].Key = "later-save"
				}
			})
			require.NotNil(t, result.Failure)
			assert.Equal(t, StageReviewing, result.Failure.Stage)
			assert.Equal(t, "rendered review failed", result.Failure.Reason)
			assert.Equal(t, VerdictFailed, result.Verdict)
			assert.Equal(t, 2, result.AttemptsUsed, "reviewer mutations must not contaminate the next attempt's captured input")
		})
	}
}

// WIRING — a blocking finding on a grouped candidate reaches Refine through
// Coordinator.Run and yields a different grouped geometry on the next attempt,
// not only different options. Each Refine-handled criterion that group
// preflight or the reviewer emits must move a measured group dimension.
func TestCoordinator_GroupBlockingFindingRefinesGroupGeometry(t *testing.T) {
	for _, form := range []string{workflow.GroupFormBranches, workflow.GroupFormWorkflows} {
		for _, criterion := range []Criterion{CriterionTextClipped, CriterionTextCollision, CriterionNodeOverlap, CriterionEdgeThroughNode} {
			t.Run(form+"/"+string(criterion), func(t *testing.T) {
				w := capturedGroupWorkflow(form, false)
				for i := range w.Nodes {
					if w.Nodes[i].Type == "" {
						w.Nodes[i].Type = "script"
					}
				}
				r := newScriptedRenderer()
				rv := &scriptedReviewer{steps: map[int]reviewStep{1: {findings: []Finding{reviewFinding(criterion)}}}}
				res := newCoord(t, r, rv).Run(context.Background(), Job{Revision: "refine-" + form, Manifest: w, MaxAttempts: 3}, nil)

				require.Equal(t, VerdictPassed, res.Verdict, "%+v", res.Failure)
				require.Equal(t, 2, res.AttemptsUsed)
				require.Len(t, rv.seen, 2)
				first, second := rv.seen[0].Geometry, rv.seen[1].Geometry
				require.NotEqual(t, r.options[0], r.options[1], "refinement changed the options")
				require.NotEmpty(t, first.GroupElements)
				assert.NotEqual(t, first.GroupElements, second.GroupElements, "group element boxes must move, not only options")
				assert.NotEqual(t, first.GroupLanes, second.GroupLanes)
				assert.NotEqual(t, GeometryDigest(first), GeometryDigest(second))
				assert.Equal(t, second, res.Best.Geometry)
			})
		}
	}
}
