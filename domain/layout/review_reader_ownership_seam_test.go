package layout_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

// Paired row 1: the reviewer judges a candidate against the ownership captured
// with the render request; the Reader re-derives ownership from the authorized
// manifest. Both must accept the same real browser-reviewed candidate, and
// neither may accept it once ownership differs on only its own side.
func capturedReviewOwnership(t *testing.T, name string) *workflow.GroupGraph {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "adapter", "renderreview", "testdata", "readability", "group-"+name, "request.json"))
	require.NoError(t, err)
	var req layout.RenderRequest
	require.NoError(t, json.Unmarshal(raw, &req))
	require.NotNil(t, req.WorkflowGroups)
	return req.WorkflowGroups
}

// swapFirstTwoTasks exchanges the owning tasks of the first group's two lanes
// while keeping every node, edge and box identical.
func swapFirstTwoTasks(g *workflow.GroupGraph) {
	scope := g.Groups[g.Order[0]]
	a, b := scope.Config.Tasks[0].Key, scope.Config.Tasks[1].Key
	scope.Bodies[a], scope.Bodies[b] = scope.Bodies[b], scope.Bodies[a]
	for id, o := range g.Owner {
		if o.GroupNodeID == scope.NodeID {
			if o.TaskKey == a {
				o.TaskKey = b
			} else if o.TaskKey == b {
				o.TaskKey = a
			}
			g.Owner[id] = o
		}
	}
	for _, s := range g.Groups {
		if s.Parent != nil && s.Parent.GroupNodeID == scope.NodeID {
			if s.Parent.TaskKey == a {
				s.Parent.TaskKey = b
			} else if s.Parent.TaskKey == b {
				s.Parent.TaskKey = a
			}
		}
	}
}

func TestReviewedCapturedOwnershipAgreesWithReaderManifest(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"branches", "workflows", "nested", "loop"} {
		t.Run(name, func(t *testing.T) {
			store, wf, best := capturedGroupRow(t, name)
			captured := capturedReviewOwnership(t, name)
			graph, err := layout.BuildGraph(wf)
			require.NoError(t, err)
			// The production coordinator captures exactly BuildGraph(manifest).Groups.
			require.Equal(t, graph.Groups, captured)
			require.NoError(t, layout.ReviewRequest{Geometry: best.Geometry, WorkflowGroups: captured}.ValidateGeometry())
			view := layout.NewReader(readOnlyStore{store, t}, nil, nil).View(ctx, wf)
			require.Equal(t, layout.DisplayComplete, view.DisplayStatus)
			require.False(t, view.Unreviewed)
			require.Equal(t, captured, view.Groups)

			t.Run("captured ownership differs", func(t *testing.T) {
				changed := capturedReviewOwnership(t, name)
				if name == "workflows" {
					// Workflow-form lanes own no body nodes; their task order
					// is the ownership the reviewed lanes were drawn for.
					tasks := changed.Groups[changed.Order[0]].Config.Tasks
					tasks[0], tasks[1] = tasks[1], tasks[0]
				} else {
					swapFirstTwoTasks(changed)
				}
				err := layout.ReviewRequest{Geometry: best.Geometry, WorkflowGroups: changed}.ValidateGeometry()
				require.True(t, errors.Is(err, layout.ErrInvalidGroups) || errors.Is(err, layout.ErrInvalidGeometry), "%v", err)
			})
			t.Run("manifest ownership differs", func(t *testing.T) {
				var moved workflow.Workflow
				require.NoError(t, json.Unmarshal(layouttest.ManifestJSON(t, wf), &moved))
				for i := range moved.Nodes {
					if moved.Nodes[i].Type == workflow.NodeTypeParallelGroup {
						tasks := moved.Nodes[i].Config.Group.Tasks
						if name == "workflows" {
							tasks[0], tasks[1] = tasks[1], tasks[0]
						} else {
							tasks[0].Entry, tasks[1].Entry = tasks[1].Entry, tasks[0].Entry
						}
						break
					}
				}
				movedGraph, err := layout.BuildGraph(&moved)
				require.NoError(t, err)
				require.NotEqual(t, captured, movedGraph.Groups)
				require.False(t, layout.Compatible(best.Geometry, movedGraph))
				view := layout.NewReader(readOnlyStore{store, t}, nil, nil).View(ctx, &moved)
				require.False(t, view.Available)
				require.True(t, view.Unreviewed)
				require.Nil(t, view.Evidence)
				require.Equal(t, "stored geometry is incompatible with this revision", view.FailureReason)
			})
			// Probe coverage is not re-run on read: the stored verdict and its
			// findings are the reviewer's only surviving record. A blocking probe
			// finding or a non-pass verdict under a passed row is never served as
			// reviewed, although the evidence envelope still matches.
			for _, kind := range []string{"blocking probe finding", "non-pass verdict"} {
				t.Run(kind, func(t *testing.T) {
					bad := best
					review := *best.Review
					review.Findings = append([]layout.Finding{}, review.Findings...)
					bad.Review = &review
					reason := "stored result contains blocking findings"
					if kind == "blocking probe finding" {
						review.Findings = append(review.Findings, layout.Finding{Criterion: layout.CriterionTextClipped, Severity: layout.SeverityBlocking, Source: layout.SourceReview, NodeIDs: []string{captured.Order[0]}, Evidence: `state "group:task:unfinished" clipped a reserved row`})
					} else {
						bad.Verdict = layout.VerdictNeedsAttention
						reason = "stored result verdict does not support a pass"
					}
					require.True(t, layout.HasRenderedEvidence(bad.Geometry, bad.Review))
					view := layout.NewReader(readOnlyStore{corruptResultStore{Store: store, result: bad}, t}, nil, nil).View(ctx, wf)
					require.Equal(t, layout.DisplayNeedsAttention, view.DisplayStatus)
					require.False(t, view.Available)
					require.True(t, view.RetryEligible)
					require.Nil(t, view.Evidence)
					require.Equal(t, reason, view.FailureReason)
				})
			}
		})
	}
}
