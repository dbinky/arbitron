package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failures interrupt a real browser call boundary, never fabricate a successful
// rendering or review. The inherited probe understates one actual branch metric
// so the production DOM/reviewer, not a scripted finding, requires refinement.
type coordinatorRenderProbe struct {
	*renderPortsProbe
	failAttempt int
	renderErr   error
	renders     []layout.RenderRequest
	measures    int
	stages      []context.Context
	stageActive bool
}

func (p *coordinatorRenderProbe) Measure(ctx context.Context, req layout.MeasureRequest) (layout.Measurements, error) {
	p.mu.Lock()
	p.measures++
	p.stages = append(p.stages, ctx)
	p.mu.Unlock()
	return p.renderPortsProbe.Measure(ctx, req)
}

func (p *coordinatorRenderProbe) Review(ctx context.Context, req layout.ReviewRequest) (layout.Review, error) {
	p.mu.Lock()
	p.stageActive = p.stageActive || p.stages[len(p.stages)-1].Err() == nil
	p.stages = append(p.stages, ctx)
	p.mu.Unlock()
	return p.renderPortsProbe.Review(ctx, req)
}

func (p *coordinatorRenderProbe) Render(ctx context.Context, req layout.RenderRequest) (layout.Rendering, error) {
	p.mu.Lock()
	p.renders = append(p.renders, req)
	p.stageActive = p.stageActive || p.stages[len(p.stages)-1].Err() == nil
	p.stages = append(p.stages, ctx)
	p.mu.Unlock()
	if req.Attempt == p.failAttempt {
		return layout.Rendering{}, p.renderErr
	}
	return p.renderPortsProbe.Render(ctx, req)
}

func TestCoordinator_RealBrowserBoundedBestReachesSink(t *testing.T) {
	browser := requireBrowserE2E(t)
	for _, tc := range []struct {
		name        string
		budget      string
		failAttempt int
	}{
		{name: "budget", budget: "1"},
		{name: "later-render-failure", budget: "5", failAttempt: 2},
		{name: "first-render-failure", budget: "5", failAttempt: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := &coordinatorRenderProbe{
				renderPortsProbe: &renderPortsProbe{understateBranch: true},
				failAttempt:      tc.failAttempt, renderErr: errors.New("injected browser interruption"),
			}
			original := layoutRenderReviewerFactory
			t.Cleanup(func() { layoutRenderReviewerFactory = original })
			layoutRenderReviewerFactory = func(cfg *config.Config, logger *slog.Logger) (layout.Renderer, layout.Reviewer, func(), error) {
				renderer, reviewer, closeFn, err := newLayoutRenderReviewer(cfg, logger)
				probe.Renderer, probe.reviewer = renderer, reviewer
				return probe, probe, closeFn, err
			}
			env := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: tc.budget})
			useLayoutE2EBrowser(t, browser)
			t.Setenv("ARBITRON_LAYOUT_ARTIFACTDIR", filepath.Join(env.root, "layout-artifacts"))
			ds, _ := env.boot(t)
			ctx := adminCtxForLocalUser(t, ds)
			wf := layoutE2EWorkflow(tc.name, "Überprüfung 審查")
			wf.Edges[0].Label = "Überprüfung\n審查"
			require.NoError(t, ds.workflowSvc.Create(ctx, tc.name, wf))
			saved := layoutE2EManifest(t, env.privatePath(tc.name))
			before, err := os.ReadFile(env.privatePath(tc.name))
			require.NoError(t, err)
			row := layoutRenderWaitTerminal(t, ds, saved.SavedRevision(), time.Minute)
			expectedAttempts, limit := 1, 1
			if tc.failAttempt > 0 {
				expectedAttempts, limit = tc.failAttempt, 5
				require.Equal(t, layout.StateFailed, row.State)
				assert.Equal(t, layout.StageRendering, row.FailureStage)
				assert.Equal(t, "rendering failed", row.FailureReason, "the injected detail is logged, never persisted")
			} else {
				require.Equal(t, layout.StateNeedsAttention, row.State, "%s: %+v", row.FailureReason, row.Findings)
				assert.Empty(t, row.FailureStage)
				assert.Equal(t, "budget", row.FailureReason)
			}
			assert.Equal(t, expectedAttempts, row.AttemptsUsed)
			assert.Equal(t, limit, row.AttemptLimit)
			captured, err := json.Marshal(saved)
			require.NoError(t, err)
			assert.Equal(t, captured, row.Manifest, "the root coordinator consumes this exact immutable saved input")
			require.Positive(t, row.ResultGeneration)
			result, err := ds.layout.store.Result(ctx, row.Revision, row.ResultGeneration)
			require.NoError(t, err)
			graph, err := layout.BuildGraph(saved)
			require.NoError(t, err)
			require.NoError(t, result.Geometry.Validate(graph))
			probe.mu.Lock()
			renders := append([]layout.RenderRequest(nil), probe.renders...)
			request, measurements := probe.measureRequest, probe.used
			measures, stageActive := probe.measures, probe.stageActive
			stages := append([]context.Context(nil), probe.stages...)
			probe.mu.Unlock()
			assert.Equal(t, 1, measures, "real browser measurement runs once per saved job, not once per candidate")
			assert.False(t, stageActive, "previous stage must already be canceled before entering the next collaborator")
			uniqueStages := make(map[context.Context]bool, len(stages))
			for _, stage := range stages {
				assert.False(t, uniqueStages[stage], "Measure, Render and Review get separate bounded contexts")
				uniqueStages[stage] = true
				assert.ErrorIs(t, stage.Err(), context.Canceled, "each stage context is canceled promptly")
				deadline, ok := stage.Deadline()
				assert.True(t, ok)
				assert.False(t, deadline.IsZero())
			}
			require.Len(t, renders, expectedAttempts)
			assert.Equal(t, layout.MeasureRequestFor(graph, layout.DefaultContract()), request)
			for i, render := range renders {
				assert.Equal(t, row.Revision, render.Revision)
				assert.Equal(t, i+1, render.Attempt)
				assert.Equal(t, layout.RuntimeStates, render.States)
				require.Len(t, render.WorkflowNodeTypes, len(saved.Nodes))
				for _, node := range saved.Nodes {
					assert.Equal(t, string(node.Type), render.WorkflowNodeTypes[node.ID])
				}
			}
			assert.Equal(t, layout.GeometryDigest(renders[0].Geometry), layout.GeometryDigest(result.Geometry), "return the best candidate, not the last unreviewed geometry")
			assert.Equal(t, layout.Preflight(graph, renders[0].Geometry, measurements), result.Preflight)
			view := ds.layout.reader.View(ctx, saved)
			require.NotNil(t, view.Geometry)
			assert.Equal(t, layout.GeometryDigest(result.Geometry), layout.GeometryDigest(*view.Geometry))
			reviews := probe.observed()
			assert.Len(t, stages, 1+len(renders)+len(reviews))
			if tc.failAttempt == 1 {
				assert.Empty(t, reviews)
				assert.Nil(t, result.Review)
				assert.True(t, row.Unreviewed)
				assert.True(t, view.Unreviewed)
				assert.Nil(t, view.Evidence, "geometry alone must never imply reviewed")
			} else {
				require.Len(t, reviews, 1)
				first := reviews[0]
				require.True(t, first.review.Rendered)
				require.NotNil(t, first.request.Rendering.Report.Edges[0].Label)
				assert.True(t, first.request.Rendering.Report.Edges[0].Label.Clipped)
				require.NotNil(t, result.Review)
				assert.Equal(t, &first.review, result.Review, "best retains its OWN actual review and evidence")
				assert.True(t, layout.HasRenderedEvidence(result.Geometry, result.Review))
				assertEvidenceOnDisk(t, env, result.Review.Evidence)
				assert.False(t, row.Unreviewed)
				assert.False(t, view.Unreviewed)
				assert.Equal(t, result.UnresolvedFindings(), row.Findings)
				assert.Equal(t, layout.ScoreOf(1, row.Findings, len(result.Geometry.Crossings)), result.Score)
				clipped := false
				for _, finding := range row.Findings {
					if finding.Criterion == layout.CriterionTextClipped {
						assert.Contains(t, finding.EdgeOrdinals, 0)
						clipped = true
					}
				}
				require.True(t, clipped, "actual DOM clipping must reach the stored best findings")
				if tc.failAttempt == 2 {
					assert.NotEqual(t, layout.GeometryDigest(renders[0].Geometry), layout.GeometryDigest(renders[1].Geometry))
				}
			}
			assert.NotEqual(t, layout.VerdictPassed, result.Verdict)
			require.Eventually(t, func() bool {
				entry, ok, err := ds.layout.cache.ReadCache(ctx, row.Destination.Scope, row.Destination.Name)
				return err == nil && ok && entry.Revision == row.Revision && entry.Generation == row.ResultGeneration && entry.JobGeneration == row.JobGeneration && entry.Verdict == result.Verdict && layout.GeometryDigest(entry.Geometry) == layout.GeometryDigest(result.Geometry)
			}, time.Minute, 50*time.Millisecond)
			after, err := os.ReadFile(env.privatePath(tc.name))
			require.NoError(t, err)
			assert.Equal(t, before, after, "attempts and failures never mutate the saved workflow")
		})
	}
}
