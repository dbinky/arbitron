package renderreview

import (
	"context"
	"io/fs"
	"math"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The bounded coordinator, driven by the real browser renderer and this
// reviewer, passes the simple acceptance sample at practical zoom. Its
// evidence belongs to the returned candidate.
func TestReadabilityReviewer_RealBrowser_PRReviewPassesThroughTheCoordinator(t *testing.T) {
	b := newTestRenderer(t, testBrowserConfig(t, embeddedSurface(t)))
	coord, err := layout.NewCoordinator(b, NewReadabilityReviewer(nil))
	require.NoError(t, err)

	res := coord.Run(context.Background(), layout.Job{Revision: "rev-pr-review", Manifest: loadSampleWorkflow(t, "pr-review"), MaxAttempts: 5}, nil)
	require.Nil(t, res.Failure, "failure: %+v", res.Failure)
	require.Equal(t, layout.VerdictPassed, res.Verdict, "unresolved findings: %+v", res.Findings)
	require.NotNil(t, res.Best)
	require.NotNil(t, res.Best.Review)
	assert.LessOrEqual(t, res.AttemptsUsed, 5)
	assert.False(t, res.Unreviewed)

	rv := res.Best.Review
	assert.True(t, rv.Rendered)
	assert.Zero(t, layout.BlockingCount(rv.Findings))
	assert.Equal(t, layout.GeometryDigest(res.Best.Geometry), rv.Evidence.GeometryDigest)
	assert.Equal(t, res.Best.Geometry.Contract, rv.Evidence.Contract)
	assert.True(t, rv.Evidence.FontsReady)
	assert.Equal(t, 0.9, rv.Evidence.Zoom)
	assert.GreaterOrEqual(t, rv.Evidence.Tiles, 1)
	require.NotEmpty(t, rv.Evidence.Artifacts)
	assert.Equal(t, "overview", rv.Evidence.Artifacts[0].Kind)
}

// The loop-heavy acceptance sample passes with unchanged thresholds. Its loop
// returns cross forward edges both over and under (G7), which only read as
// crossings when the renderer restarts the loop dash at each crossing.
func TestReadabilityReviewer_RealBrowser_RalphLoopPassesThroughTheCoordinator(t *testing.T) {
	b := newTestRenderer(t, testBrowserConfig(t, embeddedSurface(t)))
	coord, err := layout.NewCoordinator(b, NewReadabilityReviewer(nil))
	require.NoError(t, err)

	res := coord.Run(context.Background(), layout.Job{Revision: "rev-ralph-loop", Manifest: loadSampleWorkflow(t, "ralph-loop"), MaxAttempts: 5}, nil)
	require.Nil(t, res.Failure, "failure: %+v", res.Failure)
	require.Equal(t, layout.VerdictPassed, res.Verdict, "unresolved findings: %+v", res.Findings)
	require.NotNil(t, res.Best)
	require.NotNil(t, res.Best.Review)
	assert.LessOrEqual(t, res.AttemptsUsed, 5)
	assert.Zero(t, layout.BlockingCount(res.Best.Review.Findings))
	assert.True(t, layout.HasRenderedEvidence(res.Best.Geometry, res.Best.Review))
	loopCrossings := 0
	for _, c := range res.Best.Geometry.Crossings {
		if res.Best.Geometry.Edges[c.Over].Kind != layout.EdgeForward || res.Best.Geometry.Edges[c.Under].Kind != layout.EdgeForward {
			loopCrossings++
		}
	}
	assert.Positive(t, loopCrossings, "the sample must still exercise dashed loop crossings")
}

// The feature-pipeline acceptance sample (G7 label half) passes with unchanged
// thresholds: every branch label keeps ink, clearance and its own route as the
// nearest route.
func TestReadabilityReviewer_RealBrowser_FeaturePipelinePassesThroughTheCoordinator(t *testing.T) {
	b := newTestRenderer(t, testBrowserConfig(t, embeddedSurface(t)))
	coord, err := layout.NewCoordinator(b, NewReadabilityReviewer(nil))
	require.NoError(t, err)

	res := coord.Run(context.Background(), layout.Job{Revision: "rev-feature-pipeline", Manifest: loadSampleWorkflow(t, "legacy-feature-pipeline"), MaxAttempts: 5}, nil)
	require.Nil(t, res.Failure, "failure: %+v", res.Failure)
	require.Equal(t, layout.VerdictPassed, res.Verdict, "unresolved findings: %+v", res.Findings)
	require.NotNil(t, res.Best)
	require.NotNil(t, res.Best.Review)
	assert.LessOrEqual(t, res.AttemptsUsed, 5)
	assert.Zero(t, layout.BlockingCount(res.Best.Review.Findings))
	assert.True(t, layout.HasRenderedEvidence(res.Best.Geometry, res.Best.Review))
}

// Reports from actual CSS/SVG mutations must reach the reviewer sink. Synthetic
// report edits alone cannot establish that the browser collector is wired in.
func TestReadabilityReviewer_RealBrowser_DOMDefectsReachFindings(t *testing.T) {
	cases := []struct {
		name, style, script string
		criterion           layout.Criterion
	}{
		{name: "ancestor opacity", style: `#render-root {opacity:0!important}`, criterion: layout.CriterionTextUnreadable},
		{name: "ancestor visibility", style: `#render-root {visibility:hidden!important}`, criterion: layout.CriterionTextUnreadable},
		{name: "hidden marker", style: `marker {opacity:0!important}`, criterion: layout.CriterionArrowDirection},
		{name: "reversed marker", script: `for(const e of document.querySelectorAll('marker'))if(e.getAttribute('orient')!=='180')e.setAttribute('orient','180')`, criterion: layout.CriterionArrowDirection},
		{name: "wrong actual label", script: `for(const e of document.querySelectorAll('[data-node-text="label"]'))if(e.textContent!=='Changed')e.textContent='Changed'`, criterion: layout.CriterionRenderMismatch},
		{name: "wrong middle same endpoints", script: `for(const e of document.querySelectorAll('[data-edge-path="0"], [data-edge-overlay="0"]')){if(!e.dataset.original)e.dataset.original=e.getAttribute('d');const ns=e.dataset.original.match(/-?\d+(?:\.\d+)?/g).map(Number),x=ns[0],y=ns[1],ex=ns.at(-2),ey=ns.at(-1);const d='M '+x+','+y+' L '+(x+5)+','+y+' L '+(x+5)+','+ey+' L '+ex+','+ey;if(e.getAttribute('d')!==d)e.setAttribute('d',d)}`, criterion: layout.CriterionRenderMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assets := embeddedSurface(t)
			html, err := fs.ReadFile(assets, surfacePage)
			require.NoError(t, err)
			injection := "<style>" + tc.style + "</style>"
			if tc.script != "" {
				injection += `<script>new MutationObserver(()=>{` + tc.script + `}).observe(document.documentElement,{subtree:true,childList:true,attributes:true,attributeFilter:['data-state','d','orient']})</script>`
			}
			assets = surfaceHTML{assets, []byte(strings.Replace(string(html), "</head>", injection+"</head>", 1))}
			b := newTestRenderer(t, testBrowserConfig(t, assets))
			wf := loadSampleWorkflow(t, "pr-review")
			g, err := layout.BuildGraph(wf)
			require.NoError(t, err)
			m, err := b.Measure(context.Background(), layout.MeasureRequestFor(g, layout.DefaultContract()))
			require.NoError(t, err)
			geo, err := layout.Arrange(context.Background(), g, m, layout.DefaultContract(), layout.DefaultOptions())
			require.NoError(t, err)
			types := map[string]string{}
			for _, n := range wf.Nodes {
				types[n.ID] = string(n.Type)
			}
			rend, err := b.Render(context.Background(), layout.NewRenderRequest("browser-mutated", 1, geo, types))
			require.NoError(t, err)
			rv, err := NewReadabilityReviewer(nil).Review(context.Background(), layout.ReviewRequest{Revision: "browser-mutated", Attempt: 1, Geometry: geo, Rendering: rend})
			require.NoError(t, err)
			findings := withCriterion(rv.Findings, tc.criterion)
			require.NotEmpty(t, findings, "%+v", rv.Findings)
			require.Positive(t, layout.BlockingCount(findings), "actual DOM defects must prevent a pass")
		})
	}
}

func TestReadabilityReviewer_RealBrowser_LoopCrossingAndSeam(t *testing.T) {
	cfg := testBrowserConfig(t, embeddedSurface(t))
	cfg.TileWidth = 360
	cfg.TileHeight = 260
	b := newTestRenderer(t, cfg)
	geo := reviewGeometry()
	rend, err := b.Render(context.Background(), layout.NewRenderRequest("fresh-loop", 1, geo, map[string]string{"a": "agent_call", "b": "agent_call", "c": "transform"}))
	require.NoError(t, err)
	rv, err := NewReadabilityReviewer(nil).Review(context.Background(), layout.ReviewRequest{Revision: "fresh-loop", Attempt: 1, Geometry: geo, Rendering: rend})
	require.NoError(t, err)
	require.True(t, rv.Rendered)
	require.Zero(t, layout.BlockingCount(rv.Findings), "%+v", rv.Findings)
	require.Greater(t, rv.Evidence.Tiles, 1)
	require.NotEmpty(t, withCriterion(rv.Findings, layout.CriterionCrossings))
}

// A dashed loop return must read as a crossing whichever stroke it is. Its 6/4
// dash cannot span the over continuity probe, and an under gap's banks land in
// dash holes at some phases, unless the renderer restarts the dash there.
func TestReadabilityReviewer_RealBrowser_DashedLoopCrossings(t *testing.T) {
	// Over: the loop's horizontal exit crosses c→d's vertical at (400,285).
	over := reviewGeometry()
	over.Nodes = append(over.Nodes, layout.NodeBox{ID: "d", Box: layout.Rect{X: 300, Y: 360, W: 200, H: 60}, Text: "Done", TypeCaption: "transform", TextLines: 1, Rank: 2})
	over.Edges[1].Points = []layout.Point{{X: 210, Y: 260}, {X: 210, Y: 285}, {X: 540, Y: 285}, {X: 540, Y: 20}, {X: 140, Y: 20}, {X: 140, Y: 40}}
	over.Edges = append(over.Edges, layout.EdgeRoute{
		Ordinal: 3, Source: "c", Target: "d", Kind: layout.EdgeForward,
		Points:       []layout.Point{{X: 400, Y: 260}, {X: 400, Y: 360}},
		SourceAttach: layout.Attachment{NodeID: "c", ElementID: layout.GroupElementID("c", "header", ""), Side: layout.SideBottom, Point: layout.Point{X: 400, Y: 260}},
		TargetAttach: layout.Attachment{NodeID: "d", ElementID: layout.GroupElementID("d", "header", ""), Side: layout.SideTop, Point: layout.Point{X: 400, Y: 360}},
	})
	over.Crossings = []layout.Crossing{{Point: layout.Point{X: 400, Y: 285}, Over: 1, Under: 3}}
	over.Canvas = layout.Rect{X: 0, Y: 0, W: 600, H: 460}

	// Under: 3px more route before the gap puts its upper bank in a dash hole.
	under := reviewGeometry()
	under.Edges[1].Points[0].X, under.Edges[1].Points[1].X = 207, 207
	under.Edges[1].SourceAttach.Point.X = 207

	types := map[string]string{"a": "agent_call", "b": "agent_call", "c": "transform", "d": "transform"}
	for name, geo := range map[string]layout.Geometry{"over": over, "under": under} {
		t.Run(name, func(t *testing.T) {
			b := newTestRenderer(t, testBrowserConfig(t, embeddedSurface(t)))
			nodeTypes := map[string]string{}
			for _, n := range geo.Nodes {
				nodeTypes[n.ID] = types[n.ID]
			}
			rend, err := b.Render(context.Background(), layout.NewRenderRequest("dashed-"+name, 1, geo, nodeTypes))
			require.NoError(t, err)
			for _, c := range rend.Report.Crossings {
				assert.True(t, c.UnderGapVisible, "%+v", c)
				assert.True(t, c.OverContinuous, "%+v", c)
			}
			rv, err := NewReadabilityReviewer(nil).Review(context.Background(), layout.ReviewRequest{Revision: "dashed-" + name, Attempt: 1, Geometry: geo, Rendering: rend})
			require.NoError(t, err)
			require.True(t, rv.Rendered)
			require.Zero(t, layout.BlockingCount(rv.Findings), "%+v", rv.Findings)
		})
	}
}

func TestReadabilityReviewer_RealBrowser_ImmutableRetryCaptures(t *testing.T) {
	b := newTestRenderer(t, testBrowserConfig(t, embeddedSurface(t)))
	wf := loadSampleWorkflow(t, "pr-review")
	g, err := layout.BuildGraph(wf)
	require.NoError(t, err)
	m, err := b.Measure(context.Background(), layout.MeasureRequestFor(g, layout.DefaultContract()))
	require.NoError(t, err)
	geo, err := layout.Arrange(context.Background(), g, m, layout.DefaultContract(), layout.DefaultOptions())
	require.NoError(t, err)
	types := map[string]string{}
	for _, n := range wf.Nodes {
		types[n.ID] = string(n.Type)
	}
	req := layout.NewRenderRequest("same-revision", 1, geo, types)
	first, err := b.Render(context.Background(), req)
	require.NoError(t, err)
	second, err := b.Render(context.Background(), req)
	require.NoError(t, err)
	require.NotEqual(t, first.Artifacts[0].Path, second.Artifacts[0].Path)
	for _, rend := range []layout.Rendering{first, second} {
		rv, err := NewReadabilityReviewer(nil).Review(context.Background(), layout.ReviewRequest{Revision: req.Revision, Attempt: req.Attempt, Geometry: geo, Rendering: rend})
		require.NoError(t, err)
		require.True(t, rv.Rendered)
		require.Zero(t, layout.BlockingCount(rv.Findings), "%+v", rv.Findings)
		require.Equal(t, rend.Artifacts, rv.Evidence.Artifacts)
	}
}

// understate keeps real widths but claims every wrapped text fits on one line.
// Geometry built from it is internally consistent, so preflight trusts it.
func understate(m layout.Measurements, c layout.RendererContract) layout.Measurements {
	out := layout.Measurements{Contract: m.Contract, FontsReady: m.FontsReady, Items: make(map[string]layout.TextMetrics, len(m.Items))}
	for k, v := range m.Items {
		if v.Lines > 1 {
			v.Lines = 1
			v.Height = math.Ceil(c.NodeLabelFontPx * c.LineHeight)
		}
		out.Items[k] = v
	}
	return out
}

// Design Phase 1 "Failure": injected visual-only clipping prevents a pass
// even when geometric checks pass.
func TestReadabilityReviewer_RealBrowser_CatchesVisualOnlyClipping(t *testing.T) {
	ctx := context.Background()
	b := newTestRenderer(t, testBrowserConfig(t, embeddedSurface(t)))
	long := "Überprüfe sämtliche Änderungen im Pull-Request und bestätige jede einzelne Abhängigkeit gründlich, bevor du fortfährst"
	wf := &workflow.Workflow{
		Name: "visual-clip",
		Nodes: []workflow.Node{
			{ID: "n1", Type: workflow.NodeTypeTransform, Entrypoint: true, Label: long},
			{ID: "n2", Type: workflow.NodeTypeTransform, Label: "Done"},
		},
		Edges: []workflow.Edge{{Source: "n1", Target: "n2"}},
	}
	g, err := layout.BuildGraph(wf)
	require.NoError(t, err)
	c := layout.DefaultContract()
	real, err := b.Measure(ctx, layout.MeasureRequestFor(g, c))
	require.NoError(t, err)
	require.Greater(t, real.Items[layout.NodeLabelKey("n1")].Lines, 1, "the long label really wraps in the browser")

	lying := understate(real, c)
	geo, err := layout.Arrange(context.Background(), g, lying, c, layout.DefaultOptions())
	require.NoError(t, err)
	assert.Empty(t, withCriterion(layout.Preflight(g, geo, lying), layout.CriterionTextClipped),
		"geometric preflight trusts the measurements it was given")

	rend, err := b.Render(ctx, layout.NewRenderRequest("rev-clip", 1, geo, map[string]string{"n1": "transform", "n2": "transform"}))
	require.NoError(t, err)
	rv, err := NewReadabilityReviewer(nil).Review(ctx, layout.ReviewRequest{Revision: "rev-clip", Attempt: 1, Geometry: geo, Rendering: rend})
	require.NoError(t, err)
	// The overflowing glyphs are painted, so the captured ink is complete and
	// Rendered is true: the defect travels as blocking findings, not as missing
	// evidence. (A hanging-space rect once made this read as incomplete ink.)
	assert.True(t, rv.Rendered, "ink was captured for every glyph line: %+v", rv.Findings)
	clipped := withCriterion(rv.Findings, layout.CriterionTextClipped)
	require.NotEmpty(t, clipped, "the real rendering clips the long label: %+v", rv.Findings)
	assert.Equal(t, []string{"n1"}, clipped[0].NodeIDs)
	assert.NotZero(t, layout.BlockingCount(rv.Findings), "a clipped label can never pass")
}
