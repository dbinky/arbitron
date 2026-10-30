package layout

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// unitReport is a geometry-driven unit double, not a DOM or raster producer.
func unitReport(g Geometry) RenderReport {
	c := g.Contract
	text := func(content string, box Rect, font float64) RenderedText {
		r := RenderedText{Content: content, Visible: true, Box: box, FontPx: font, Contrast: 10}
		if content != "" {
			r.Glyphs = []Rect{box}
		}
		return r
	}
	r := RenderReport{SurfaceVersion: c.Version, Contract: c, FontsReady: true, FontFamilyResolved: true, Zoom: c.PracticalZoom, Canvas: g.Canvas}
	for _, n := range g.Nodes {
		typeBox := Rect{X: n.Box.X + c.NodePaddingX + c.NodeBorderPx, Y: n.Box.Y + c.NodeBorderPx + c.NodeTypeBarPx + c.NodePaddingY, W: n.Box.W - 2*(c.NodePaddingX+c.NodeBorderPx), H: c.NodeTypeFontPx * c.LineHeight}
		labelBox := typeBox
		labelBox.Y = typeBox.Bottom() + c.NodeTextGapPx
		labelBox.H = float64(n.TextLines) * c.NodeLabelFontPx * c.LineHeight
		r.Nodes = append(r.Nodes, RenderedNode{ID: n.ID, Box: n.Box, Label: text(n.Text, labelBox, c.NodeLabelFontPx), Type: text(n.TypeCaption, typeBox, c.NodeTypeFontPx)})
	}
	for _, e := range g.Edges {
		re := RenderedEdge{Ordinal: e.Ordinal, Start: e.SourceAttach.Point, End: e.TargetAttach.Point, HasArrow: true, Visible: true, StrokePx: c.StrokePx, StrokeColor: c.ForwardColor}
		if e.Kind != EdgeForward {
			re.Dashed = true
			re.DashPattern = c.LoopReturnDash
			re.StrokeColor = c.LoopReturnColor
		}
		for i := 1; i < len(e.Points); i++ {
			re.Segments = append(re.Segments, RenderedSegment{Start: e.Points[i-1], End: e.Points[i]})
		}
		tip, prev := e.Points[len(e.Points)-1], e.Points[len(e.Points)-2]
		dx, dy := tip.X-prev.X, tip.Y-prev.Y
		length := math.Hypot(dx, dy)
		dx /= length
		dy /= length
		base := Point{tip.X - dx*c.ArrowPx, tip.Y - dy*c.ArrowPx}
		re.ArrowPoints = []Point{tip, {base.X - dy*c.ArrowPx/2, base.Y + dx*c.ArrowPx/2}, {base.X + dy*c.ArrowPx/2, base.Y - dx*c.ArrowPx/2}}
		if e.Text != nil {
			label := text(e.Text.Text, e.Text.Box, c.EdgeLabelFontPx)
			re.Label = &label
		}
		r.Edges = append(r.Edges, re)
	}
	for _, x := range g.Crossings {
		r.Crossings = append(r.Crossings, RenderedCrossing{Point: x.Point, Over: x.Over, Under: x.Under, UnderGapVisible: true, OverContinuous: true})
	}
	for _, s := range RuntimeStates {
		r.States = append(r.States, StateProbe{State: s, NodeBoxesStable: true, LabelsReadable: true})
	}
	return r
}

// scriptedRenderer measures with fakeMeasurer and "renders" by echoing the
// request's geometry digest and contract — it honours the load-bearing
// argument (the candidate geometry) so evidence can be checked against it.
type scriptedRenderer struct {
	mu         sync.Mutex
	measurer   *fakeMeasurer
	measureErr error
	renderErr  map[int]error // by attempt
	block      bool          // Render waits for ctx cancellation
	rendered   []string      // geometry digests in call order
	options    []Options     // options of each rendered candidate
	requests   []RenderRequest
}

func newScriptedRenderer() *scriptedRenderer {
	return &scriptedRenderer{measurer: newFakeMeasurer(), renderErr: map[int]error{}}
}

func (r *scriptedRenderer) Measure(ctx context.Context, req MeasureRequest) (Measurements, error) {
	if err := ctx.Err(); err != nil {
		return Measurements{}, err
	}
	if r.measureErr != nil {
		return Measurements{}, r.measureErr
	}
	return r.measurer.Measure(ctx, req)
}

func (r *scriptedRenderer) Render(ctx context.Context, req RenderRequest) (Rendering, error) {
	if err := ctx.Err(); err != nil {
		return Rendering{}, err
	}
	if err := unitRequestProblem(req); err != nil {
		return Rendering{}, err
	}
	r.mu.Lock()
	r.requests = append(r.requests, req)
	r.rendered = append(r.rendered, GeometryDigest(req.Geometry))
	r.options = append(r.options, req.Geometry.Options)
	err := r.renderErr[req.Attempt]
	block := r.block
	r.mu.Unlock()
	if block {
		<-ctx.Done()
		return Rendering{}, ctx.Err()
	}
	if err != nil {
		return Rendering{}, err
	}
	return Rendering{
		Revision: req.Revision, Attempt: req.Attempt, GeometryDigest: GeometryDigest(req.Geometry),
		Artifacts: []Artifact{
			{Kind: "overview", Path: "/unit/" + GeometryDigest(req.Geometry) + "-overview.png", SHA256: GeometryDigest(req.Geometry), Region: req.Geometry.Canvas, Scale: req.Geometry.Contract.PracticalZoom},
			{Kind: "tile", Path: "/unit/" + GeometryDigest(req.Geometry) + "-tile.png", SHA256: GeometryDigest(req.Geometry), Region: req.Geometry.Canvas, Scale: req.Geometry.Contract.PracticalZoom},
		},
		Report:     unitReport(req.Geometry),
		RenderedAt: time.Unix(0, 0).UTC(),
	}, nil
}

type reviewStep struct {
	findings    []Finding
	err         error
	notRendered bool
	wrongDigest bool
	fontsLate   bool
}

// scriptedReviewer returns per-attempt scripted findings and copies evidence
// from the rendering it was given (so a mismatch is only produced on purpose).
type scriptedReviewer struct {
	steps    map[int]reviewStep
	fallback reviewStep
	seen     []ReviewRequest
}

func (r *scriptedReviewer) Review(ctx context.Context, req ReviewRequest) (Review, error) {
	if err := ctx.Err(); err != nil {
		return Review{}, err
	}
	if req.Revision == "" || req.Rendering.Revision != req.Revision || req.Rendering.Attempt != req.Attempt || req.Rendering.GeometryDigest != GeometryDigest(req.Geometry) {
		return Review{Rendered: false, Findings: []Finding{{Criterion: CriterionRenderMismatch, Severity: SeverityBlocking, Source: SourceReview, Evidence: "unit rendering request binding mismatch"}}}, nil
	}
	if !reflect.DeepEqual(req.Rendering.Report, unitReport(req.Geometry)) {
		return Review{Rendered: false, Findings: []Finding{reviewFinding(CriterionRenderMismatch)}}, nil
	}
	r.seen = append(r.seen, req)
	step, ok := r.steps[req.Attempt]
	if !ok {
		step = r.fallback
	}
	if step.err != nil {
		return Review{}, step.err
	}
	ev := Evidence{
		GeometryDigest: req.Rendering.GeometryDigest, RendererVersion: req.Rendering.Report.SurfaceVersion,
		Contract: req.Rendering.Report.Contract, FontsReady: req.Rendering.Report.FontsReady && req.Rendering.Report.FontFamilyResolved, Zoom: req.Rendering.Report.Zoom, Tiles: 1,
		Artifacts:  append([]Artifact(nil), req.Rendering.Artifacts...),
		ReviewedAt: time.Unix(0, 0).UTC(),
	}
	if step.wrongDigest {
		ev.GeometryDigest = "0000"
	}
	if step.fontsLate {
		ev.FontsReady = false
	}
	return Review{Rendered: !step.notRendered, Findings: append([]Finding(nil), step.findings...), Evidence: ev}, nil
}

func reviewFinding(c Criterion) Finding {
	return Finding{Criterion: c, Severity: SeverityBlocking, Source: SourceReview, Evidence: "scripted " + string(c)}
}

func newCoord(t *testing.T, r Renderer, rv Reviewer, opts ...CoordinatorOption) *Coordinator {
	t.Helper()
	c, err := NewCoordinator(r, rv, opts...)
	require.NoError(t, err)
	return c
}

func prReviewJob(t *testing.T, attempts int) Job {
	return Job{Revision: "rev-1", Manifest: loadSample(t, "pr-review"), MaxAttempts: attempts}
}

func TestNewCoordinator_RequiresBothPorts(t *testing.T) {
	_, err := NewCoordinator(nil, &scriptedReviewer{})
	assert.True(t, errors.Is(err, ErrNilRenderer))
	_, err = NewCoordinator(newScriptedRenderer(), nil)
	assert.True(t, errors.Is(err, ErrNilReviewer))
}

func TestRun_PassesOnFirstCleanRenderedReview(t *testing.T) {
	r := newScriptedRenderer()
	rv := &scriptedReviewer{}
	var stages []Progress
	res := newCoord(t, r, rv).Run(context.Background(), prReviewJob(t, 5), func(p Progress) { stages = append(stages, p) })

	assert.Equal(t, VerdictPassed, res.Verdict)
	assert.Equal(t, 1, res.AttemptsUsed)
	require.NotNil(t, res.Best)
	assert.True(t, res.Best.Passed)
	assert.False(t, res.Unreviewed)
	assert.Nil(t, res.Failure)
	assert.Equal(t, 0, BlockingCount(res.Findings))
	assert.Equal(t, GeometryDigest(res.Best.Geometry), res.Best.Review.Evidence.GeometryDigest, "evidence belongs to the returned candidate")
	assert.Equal(t, []Progress{{StageArranging, 1}, {StageRendering, 1}, {StageReviewing, 1}}, stages)
	assert.Equal(t, 1, r.measurer.calls, "text is measured once per job")
}

func TestRun_VisualOnlyClippingIsRefinedThenPasses(t *testing.T) {
	r := newScriptedRenderer()
	rv := &scriptedReviewer{steps: map[int]reviewStep{1: {findings: []Finding{reviewFinding(CriterionTextClipped)}}}}
	res := newCoord(t, r, rv).Run(context.Background(), prReviewJob(t, 5), nil)

	assert.Equal(t, VerdictPassed, res.Verdict)
	assert.Equal(t, 2, res.AttemptsUsed)
	require.Len(t, r.rendered, 2)
	assert.NotEqual(t, r.rendered[0], r.rendered[1], "refinement produced a different candidate")
	assert.Equal(t, 8.0, r.options[1].NodeTextSlack, "text_clipped grows boxes")
	assert.Equal(t, 2, res.Best.Attempt)
}

func TestRun_BudgetExhaustionReturnsBestWithItsFindings(t *testing.T) {
	r := newScriptedRenderer()
	rv := &scriptedReviewer{
		steps:    map[int]reviewStep{1: {findings: []Finding{reviewFinding(CriterionAmbiguousCrossing)}}},
		fallback: reviewStep{findings: []Finding{reviewFinding(CriterionAmbiguousCrossing), reviewFinding(CriterionMergedLines)}},
	}
	res := newCoord(t, r, rv).Run(context.Background(), prReviewJob(t, 5), nil)

	assert.Equal(t, VerdictNeedsAttention, res.Verdict)
	assert.Equal(t, "budget", res.ExhaustedReason)
	assert.Equal(t, 5, res.AttemptsUsed)
	assert.Len(t, r.rendered, 5, "five attempts means exactly five at most")
	require.NotNil(t, res.Best)
	assert.Equal(t, 1, res.Best.Attempt, "attempt 1 had fewer blocking findings and wins the ranking")
	assert.False(t, res.Best.Passed)
	assert.Contains(t, res.Findings, reviewFinding(CriterionAmbiguousCrossing))
	assert.NotContains(t, res.Findings, reviewFinding(CriterionMergedLines), "findings come from the returned candidate only")
	assert.Nil(t, res.Failure)
}

func TestRun_BudgetOneAndNonPositiveBudgets(t *testing.T) {
	for _, budget := range []int{1, 0, -3} {
		r := newScriptedRenderer()
		rv := &scriptedReviewer{fallback: reviewStep{findings: []Finding{reviewFinding(CriterionNodeOverlap)}}}
		res := newCoord(t, r, rv).Run(context.Background(), prReviewJob(t, budget), nil)
		assert.Equal(t, VerdictNeedsAttention, res.Verdict, "budget %d", budget)
		assert.Equal(t, 1, res.AttemptsUsed, "budget %d is at least one attempt", budget)
		assert.Len(t, r.rendered, 1)
	}
}

func TestRun_EarlyExhaustionWhenNothingCanChange(t *testing.T) {
	r := newScriptedRenderer()
	rv := &scriptedReviewer{fallback: reviewStep{findings: []Finding{reviewFinding(CriterionTextUnreadable)}}}
	res := newCoord(t, r, rv).Run(context.Background(), prReviewJob(t, 5), nil)
	assert.Equal(t, VerdictNeedsAttention, res.Verdict)
	assert.Equal(t, "no_refinement", res.ExhaustedReason)
	assert.Equal(t, 1, res.AttemptsUsed)
}

func TestRun_PreflightFailuresAreStillRenderedAndReviewed(t *testing.T) {
	c := DefaultContract()
	c.NodeTypeFontPx = 8 // unreadable at practical zoom → blocking preflight finding
	r := newScriptedRenderer()
	rv := &scriptedReviewer{}
	res := newCoord(t, r, rv, WithContract(c)).Run(context.Background(), prReviewJob(t, 5), nil)
	assert.Len(t, r.rendered, 1, "a candidate failing preflight is still rendered")
	assert.Len(t, rv.seen, 1, "…and reviewed")
	assert.NotEqual(t, VerdictPassed, res.Verdict, "a clean review never overrides blocking preflight findings")
	assert.Greater(t, BlockingCount(res.Findings), 0)
}

func TestRun_EvidenceMustMatchTheCandidate(t *testing.T) {
	for name, step := range map[string]reviewStep{
		"wrong digest":    {wrongDigest: true},
		"not rendered":    {notRendered: true},
		"fonts not ready": {fontsLate: true},
	} {
		t.Run(name, func(t *testing.T) {
			r := newScriptedRenderer()
			rv := &scriptedReviewer{fallback: step}
			res := newCoord(t, r, rv).Run(context.Background(), prReviewJob(t, 2), nil)
			assert.NotEqual(t, VerdictPassed, res.Verdict, "missing or mismatched render evidence never implies a pass")
			found := false
			for _, f := range res.Findings {
				if f.Criterion == CriterionRenderMismatch && f.Severity == SeverityBlocking {
					found = true
				}
			}
			assert.True(t, found)
		})
	}
}

func TestRun_RenderFailureAfterAReviewedAttemptKeepsReviewedBest(t *testing.T) {
	r := newScriptedRenderer()
	r.renderErr[2] = errors.New("browser crashed")
	rv := &scriptedReviewer{fallback: reviewStep{findings: []Finding{reviewFinding(CriterionNodeOverlap)}}}
	res := newCoord(t, r, rv).Run(context.Background(), prReviewJob(t, 5), nil)
	assert.Equal(t, VerdictFailed, res.Verdict)
	require.NotNil(t, res.Failure)
	assert.Equal(t, StageRendering, res.Failure.Stage)
	assert.Equal(t, "rendering failed", res.Failure.Reason, "adapter detail is logged, never persisted")
	assert.Equal(t, 2, res.AttemptsUsed, "the failed attempt still counts")
	require.NotNil(t, res.Best)
	assert.Equal(t, 1, res.Best.Attempt, "the reviewed candidate outranks the unreviewed one")
	assert.False(t, res.Unreviewed)
	assert.False(t, res.Cancelled)
}

// A browser that dies mid-capture is installed: naming it "no browser
// available" sends operators to reinstall Chrome. It has its own fixed,
// path-free category, and a missing browser keeps its own.
func TestRun_BrowserCrashAndMissingBrowserAreDistinctCategories(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("renderreview: %w: browser exited while capturing screenshots: /tmp/x signal: killed", ErrRendererCrashed), "rendering failed: browser exited"},
		{fmt.Errorf("renderreview: %w: launching /opt/chrome: no such file", ErrRendererUnavailable), "rendering failed: " + ErrRendererUnavailable.Error()},
	} {
		r := newScriptedRenderer()
		r.renderErr[1] = tc.err
		res := newCoord(t, r, &scriptedReviewer{}).Run(context.Background(), prReviewJob(t, 5), nil)
		require.NotNil(t, res.Failure)
		assert.Equal(t, tc.want, res.Failure.Reason)
		for _, detail := range []string{"/tmp/x", "/opt/chrome", "killed", "capturing screenshots"} {
			assert.NotContains(t, res.Failure.Reason, detail, "adapter detail is logged, never persisted")
		}
	}
}

func TestRun_RenderFailureOnFirstAttemptRetainsUnreviewedGeometry(t *testing.T) {
	r := newScriptedRenderer()
	r.renderErr[1] = errors.New("no browser")
	res := newCoord(t, r, &scriptedReviewer{}).Run(context.Background(), prReviewJob(t, 5), nil)
	assert.Equal(t, VerdictFailed, res.Verdict)
	require.NotNil(t, res.Best)
	assert.Nil(t, res.Best.Review)
	assert.True(t, res.Unreviewed, "the retained geometry is clearly marked unreviewed")
	assert.False(t, res.Best.Passed)
}

func unitRequestProblem(req RenderRequest) error {
	if req.Revision == "" || req.Attempt < 1 || len(req.WorkflowNodeTypes) != len(req.Geometry.Nodes) {
		return fmt.Errorf("unit render request binding/coverage mismatch")
	}
	for _, n := range req.Geometry.Nodes {
		typ, ok := req.WorkflowNodeTypes[n.ID]
		if !ok || typ == "" || strings.ReplaceAll(typ, "_", " ") != n.TypeCaption {
			return fmt.Errorf("unit raw type mismatch for %s", n.ID)
		}
	}
	return nil
}

func TestRun_ReviewerErrorNeverPasses(t *testing.T) {
	r := newScriptedRenderer()
	rv := &scriptedReviewer{fallback: reviewStep{err: errors.New("reviewer unavailable")}}
	res := newCoord(t, r, rv).Run(context.Background(), prReviewJob(t, 5), nil)
	assert.Equal(t, VerdictFailed, res.Verdict)
	require.NotNil(t, res.Failure)
	assert.Equal(t, StageReviewing, res.Failure.Stage)
	assert.True(t, res.Unreviewed)
}

func TestRun_MeasureFailure(t *testing.T) {
	r := newScriptedRenderer()
	r.measureErr = errors.New("measure surface failed")
	res := newCoord(t, r, &scriptedReviewer{}).Run(context.Background(), prReviewJob(t, 5), nil)
	assert.Equal(t, VerdictFailed, res.Verdict)
	require.NotNil(t, res.Failure)
	assert.Equal(t, StageRendering, res.Failure.Stage)
	assert.Equal(t, 1, res.AttemptsUsed)
	assert.Nil(t, res.Best)
	assert.Empty(t, r.rendered)
}

func TestRun_InvalidManifest(t *testing.T) {
	r := newScriptedRenderer()
	res := newCoord(t, r, &scriptedReviewer{}).Run(context.Background(), Job{Revision: "r", Manifest: &workflow.Workflow{}, MaxAttempts: 5}, nil)
	assert.Equal(t, VerdictFailed, res.Verdict)
	require.NotNil(t, res.Failure)
	assert.Equal(t, StageArranging, res.Failure.Stage)
	assert.Contains(t, res.Failure.Reason, "no nodes")
	assert.Equal(t, 0, res.AttemptsUsed)
	assert.Nil(t, res.Best)
	assert.Empty(t, r.rendered)

	res = newCoord(t, r, &scriptedReviewer{}).Run(context.Background(), Job{Revision: "r", Manifest: nil, MaxAttempts: 5}, nil)
	assert.Equal(t, VerdictFailed, res.Verdict)
}

func TestRun_CancelledBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := newScriptedRenderer()
	res := newCoord(t, r, &scriptedReviewer{}).Run(ctx, prReviewJob(t, 5), nil)
	assert.True(t, res.Cancelled)
	assert.Equal(t, VerdictFailed, res.Verdict)
	assert.Equal(t, 0, res.AttemptsUsed)
	assert.Empty(t, r.rendered)
}

func TestRun_CancelledMidRenderRetainsBestSoFar(t *testing.T) {
	r := newScriptedRenderer()
	rv := &scriptedReviewer{fallback: reviewStep{findings: []Finding{reviewFinding(CriterionNodeOverlap)}}}
	ctx, cancel := context.WithCancel(context.Background())
	attempt := 0
	progress := func(p Progress) {
		if p.Stage == StageRendering {
			attempt = p.Attempt
			if attempt == 2 {
				r.mu.Lock()
				r.block = true
				r.mu.Unlock()
				go cancel()
			}
		}
	}
	res := newCoord(t, r, rv).Run(ctx, prReviewJob(t, 5), progress)
	assert.True(t, res.Cancelled)
	assert.Equal(t, VerdictFailed, res.Verdict)
	require.NotNil(t, res.Best)
	assert.Equal(t, 1, res.Best.Attempt)
	assert.False(t, res.Unreviewed)
}

func TestRun_IsDeterministic(t *testing.T) {
	run := func() Result {
		rv := &scriptedReviewer{steps: map[int]reviewStep{1: {findings: []Finding{reviewFinding(CriterionMergedLines)}}}}
		return newCoord(t, newScriptedRenderer(), rv).Run(context.Background(), Job{Revision: "r", Manifest: loadSample(t, "legacy-feature-pipeline"), MaxAttempts: 5}, nil)
	}
	a, b := run(), run()
	require.NotNil(t, a.Best)
	assert.Equal(t, GeometryDigest(a.Best.Geometry), GeometryDigest(b.Best.Geometry))
	assert.Equal(t, a.Verdict, b.Verdict)
	assert.Equal(t, a.AttemptsUsed, b.AttemptsUsed)
}
