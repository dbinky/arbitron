package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This observer delegates to the production factory's browser and reviewer.
// Faults affect their inputs, never fabricate a report, artifact or verdict.
// The production Worker/Coordinator/Store remain untouched.
type renderPortsProbe struct {
	layout.Renderer
	reviewer         layout.Reviewer
	omitReport       bool
	understateBranch bool
	mu               sync.Mutex
	measureRequest   layout.MeasureRequest
	measured         layout.Measurements
	used             layout.Measurements
	attempts         []renderPortsAttempt
}

type renderPortsAttempt struct {
	request layout.ReviewRequest
	review  layout.Review
}

func (p *renderPortsProbe) Measure(ctx context.Context, req layout.MeasureRequest) (layout.Measurements, error) {
	actual, err := p.Renderer.Measure(ctx, req)
	used := actual
	if err == nil && p.understateBranch {
		used.Items = make(map[string]layout.TextMetrics, len(actual.Items))
		for key, metric := range actual.Items {
			used.Items[key] = metric
		}
		// Inject a one-line measurement defect, not a fabricated finding. Only
		// the actual browser rendering can reveal the saved branch's second line.
		key := layout.EdgeLabelKey(0)
		metric := used.Items[key]
		metric.Lines = 1
		metric.Height = req.Contract.EdgeLabelFontPx * req.Contract.LineHeight
		used.Items[key] = metric
	}
	p.mu.Lock()
	p.measureRequest, p.measured, p.used = req, actual, used
	p.mu.Unlock()
	return used, err
}

func (p *renderPortsProbe) Render(ctx context.Context, req layout.RenderRequest) (layout.Rendering, error) {
	rendering, err := p.Renderer.Render(ctx, req)
	if p.omitReport {
		rendering.Report = layout.RenderReport{}
	}
	return rendering, err
}

func (p *renderPortsProbe) Review(ctx context.Context, req layout.ReviewRequest) (layout.Review, error) {
	review, err := p.reviewer.Review(ctx, req)
	p.mu.Lock()
	p.attempts = append(p.attempts, renderPortsAttempt{request: req, review: review})
	p.mu.Unlock()
	return review, err
}

func (p *renderPortsProbe) observed() []renderPortsAttempt {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]renderPortsAttempt(nil), p.attempts...)
}

func installRenderPortsProbe(t *testing.T, probe *renderPortsProbe) {
	t.Helper()
	original := layoutRenderReviewerFactory
	t.Cleanup(func() { layoutRenderReviewerFactory = original })
	layoutRenderReviewerFactory = func(cfg *config.Config, logger *slog.Logger) (layout.Renderer, layout.Reviewer, func(), error) {
		renderer, reviewer, closeFn, err := newLayoutRenderReviewer(cfg, logger)
		probe.Renderer, probe.reviewer = renderer, reviewer
		return probe, probe, closeFn, err
	}
}

// Saved authoring data is fed through initDomain, the real render.html DOM/PNG
// producer and the real reviewer into SQLite and Reader. A missing report must
// change those sinks, even when genuine PNGs and clean geometry still exist.
func TestRenderPorts_RealBrowserSavedReportReachesSink(t *testing.T) {
	browser := requireBrowserE2E(t)
	for _, missing := range []bool{false, true} {
		name := "complete"
		if missing {
			name = "missing-report"
		}
		t.Run(name, func(t *testing.T) {
			probe := &renderPortsProbe{omitReport: missing}
			installRenderPortsProbe(t, probe)
			env := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "5"})
			useLayoutE2EBrowser(t, browser)
			t.Setenv("ARBITRON_LAYOUT_ARTIFACTDIR", filepath.Join(env.root, "layout-artifacts"))
			ds, _ := env.boot(t)
			ctx := adminCtxForLocalUser(t, ds)
			wf := layoutE2EWorkflow(name, "Überprüfung 審查 👩‍💻")
			wf.Edges[0].Label = "Freigabe 審查"
			require.NoError(t, ds.workflowSvc.Create(ctx, name, wf))
			saved := layoutE2EManifest(t, env.privatePath(name))
			before, err := os.ReadFile(env.privatePath(name))
			require.NoError(t, err)
			rec := layoutRenderWaitTerminal(t, ds, saved.SavedRevision(), time.Minute)
			if missing {
				require.Equal(t, layout.StateNeedsAttention, rec.State, "%s: %+v", rec.FailureReason, rec.Findings)
			} else {
				require.Equal(t, layout.StatePassed, rec.State, "%s: %+v", rec.FailureReason, rec.Findings)
			}
			require.Positive(t, rec.ResultGeneration)
			result, err := ds.layout.store.Result(ctx, rec.Revision, rec.ResultGeneration)
			require.NoError(t, err)
			view := ds.layout.reader.View(ctx, saved)
			assert.Equal(t, rec.Revision, view.SavedRevision)
			captured, err := json.Marshal(saved)
			require.NoError(t, err)
			assert.JSONEq(t, string(captured), string(rec.Manifest))
			attempts := probe.observed()
			require.Len(t, attempts, 1)
			a := attempts[0]
			assert.Equal(t, rec.Revision, a.request.Revision)
			assert.Equal(t, layout.GeometryDigest(result.Geometry), a.request.Rendering.GeometryDigest)
			require.NotNil(t, result.Review)
			assert.Equal(t, a.review.Evidence, result.Review.Evidence)
			for _, finding := range a.review.Findings {
				assert.Contains(t, result.Review.Findings, finding, "review findings must survive coordinator validation and persistence")
			}
			assertEvidenceOnDisk(t, env, a.review.Evidence)
			if missing {
				assert.Equal(t, layout.DisplayNeedsAttention, view.DisplayStatus)
				assert.True(t, rec.Unreviewed)
				assert.True(t, view.Unreviewed)
				assert.False(t, result.Review.Rendered)
				assert.Positive(t, layout.BlockingCount(rec.Findings))
				assert.Nil(t, view.Evidence, "missing DOM evidence must never become a reviewed display")
			} else {
				assert.Equal(t, layout.DisplayComplete, view.DisplayStatus)
				require.NotNil(t, view.Geometry)
				assert.Equal(t, layout.GeometryDigest(result.Geometry), layout.GeometryDigest(*view.Geometry))
				assert.True(t, layout.HasRenderedEvidence(result.Geometry, result.Review))
				report := a.request.Rendering.Report
				require.Len(t, report.Nodes, len(saved.Nodes))
				for i, n := range report.Nodes {
					assert.Equal(t, saved.Nodes[i].ID, n.ID)
					assert.Equal(t, saved.Nodes[i].Label, n.Label.Content)
					assert.True(t, n.Label.Visible)
					require.NotEmpty(t, n.Label.Glyphs)
				}
				require.Len(t, report.Edges, len(saved.Edges))
				for i, e := range report.Edges {
					assert.Equal(t, i, e.Ordinal)
					assert.True(t, e.Visible)
					require.NotEmpty(t, e.Segments)
					require.Len(t, e.ArrowPoints, 3)
					assert.NotEmpty(t, e.StrokeColor)
				}
				require.NotNil(t, report.Edges[0].Label)
				assert.Equal(t, saved.Edges[0].Label, report.Edges[0].Label.Content)
				assert.True(t, report.Edges[0].Label.Visible)
				assert.NotEmpty(t, report.Edges[0].Label.Glyphs)
				require.Len(t, report.States, len(layout.RuntimeStates))
				for i, state := range report.States {
					assert.Equal(t, layout.RuntimeStates[i], state.State)
					assert.True(t, state.NodeBoxesStable)
					assert.True(t, state.LabelsReadable)
				}
			}
			after, err := os.ReadFile(env.privatePath(name))
			require.NoError(t, err)
			assert.Equal(t, before, after)
		})
	}
}
