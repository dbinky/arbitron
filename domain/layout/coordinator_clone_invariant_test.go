package layout

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

// cloneGroupGraph can only return an error from ParseGroupConfig. BuildGraph
// already checks those configs, and Run never exposes its captured map. Pin
// that invariant at BOTH request boundaries: even invalidating every parser
// input in the manifest and previous requests cannot make a later clone fail.
// Removing either request clone must fail this test at the next stage/attempt.
func TestRun_GroupCloneErrorsUnreachableAfterCapture(t *testing.T) {
	for _, tc := range []struct {
		name, form string
		nested     bool
	}{
		{"branches", workflow.GroupFormBranches, false},
		{"workflows", workflow.GroupFormWorkflows, false},
		{"nested", workflow.GroupFormBranches, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := capturedGroupWorkflow(tc.form, tc.nested)
			for i := range original.Nodes {
				if original.Nodes[i].Type == "" {
					original.Nodes[i].Type = workflow.NodeTypeTransform
				}
			}
			// Exercise the JSON-shaped saved input Worker actually decodes.
			raw, err := json.Marshal(original)
			require.NoError(t, err)
			var saved workflow.Workflow
			require.NoError(t, json.Unmarshal(raw, &saved))
			want, err := BuildGraph(original)
			require.NoError(t, err)
			check := func(groups *workflow.GroupGraph, geo Geometry) {
				t.Helper()
				require.NotNil(t, groups)
				require.Equal(t, want.Groups, groups, "captured input stays valid and unchanged")
				for _, scope := range groups.Groups {
					_, err := workflow.ParseGroupConfig(workflow.NodeConfig{Group: &scope.Config})
					require.NoError(t, err, "the only possible cloneGroupGraph error is unreachable")
				}
				require.NoError(t, (ReviewRequest{Geometry: geo, WorkflowGroups: groups}).ValidateGeometry())
			}
			poison := func(groups *workflow.GroupGraph) {
				for _, scope := range groups.Groups {
					invalidateCloneConfig(&scope.Config)
				}
				// Also invalidate ownership; request validation must observe the
				// original complete graph rather than accepting nil/empty input.
				clear(groups.Owner)
				clear(groups.Groups)
				groups.Order[0] = "invalid"
			}
			base := newScriptedRenderer()
			renders, reviews := 0, 0
			renderer := hookedRenderer{scriptedRenderer: base, render: func(ctx context.Context, req RenderRequest) (Rendering, error) {
				check(req.WorkflowGroups, req.Geometry)
				renders++
				out, err := base.Render(ctx, req)
				poison(req.WorkflowGroups)
				return out, err
			}}
			stop := errors.New("later review unavailable")
			firstFinding := reviewFinding(CriterionTextClipped)
			rv := &scriptedReviewer{steps: map[int]reviewStep{1: {findings: []Finding{firstFinding}}, 2: {err: stop}}}
			reviewer := reviewFunc(func(ctx context.Context, req ReviewRequest) (Review, error) {
				check(req.WorkflowGroups, req.Geometry)
				reviews++
				out, err := rv.Review(ctx, req)
				poison(req.WorkflowGroups)
				return out, err
			})
			result := newCoord(t, renderer, reviewer).Run(context.Background(), Job{Revision: "saved-group", Manifest: &saved, MaxAttempts: 2}, func(p Progress) {
				if p.Stage == StageArranging && p.Attempt == 1 {
					for i := range saved.Nodes {
						if saved.Nodes[i].Config.Group != nil {
							invalidateCloneConfig(saved.Nodes[i].Config.Group)
						}
					}
				}
			})
			require.Equal(t, 2, renders)
			require.Equal(t, 2, reviews)
			require.Equal(t, VerdictFailed, result.Verdict)
			require.Equal(t, 2, result.AttemptsUsed)
			require.Equal(t, &Failure{Stage: StageReviewing, Reason: "rendered review failed"}, result.Failure)
			require.NotNil(t, result.Best)
			require.Equal(t, 1, result.Best.Attempt)
			require.NoError(t, result.Best.Geometry.Validate(want))
			require.True(t, HasRenderedEvidence(result.Best.Geometry, result.Best.Review))
			require.Contains(t, result.Findings, firstFinding)
		})
	}
}

func invalidateCloneConfig(cfg *workflow.GroupConfig) {
	cfg.Form = "invalid"
	cfg.CompletionRule = "invalid"
	cfg.UnfinishedBehavior = "invalid"
	cfg.GatherMode = workflow.GroupGatherResultsAndFiles
	cfg.ResolverProfile = ""
	for i := range cfg.Tasks {
		task := &cfg.Tasks[i]
		task.Key, task.Entry, task.Workflow = "", "", ""
		if task.Inputs != nil {
			task.Inputs["workspace"] = "reserved"
			task.Inputs["nested"].(map[string]any)["values"].([]any)[0] = "invalid"
		}
	}
	cfg.Tasks = nil
}
