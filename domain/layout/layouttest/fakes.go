package layouttest

import (
	"context"
	"errors"
	"math"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
)

type ReviewScript struct {
	Findings []layout.Finding
	Err      error
}

// FakeRenderer supplies argument-bound unit envelopes, never browser evidence.
// Configure exported fields before use; access counters through the methods.
type FakeRenderer struct {
	MeasureErr  error
	RenderErr   error
	RenderDelay time.Duration
	OnRender    func(layout.RenderRequest)
	Calls       int
	mu          sync.Mutex
	measures    int
	inFlight    int
	maxInFlight int
	requests    []layout.RenderRequest
}

func NewFakeRenderer() *FakeRenderer { return &FakeRenderer{} }
func (r *FakeRenderer) Measure(ctx context.Context, req layout.MeasureRequest) (layout.Measurements, error) {
	r.mu.Lock()
	r.measures++
	r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return layout.Measurements{}, err
	}
	if r.MeasureErr != nil {
		return layout.Measurements{}, r.MeasureErr
	}
	if len(req.Items) == 0 || req.Contract.Version == "" {
		return layout.Measurements{}, errors.New("unit measure: missing items/contract")
	}
	for _, item := range req.Items {
		if item.Key == "" || item.MaxWidth <= 0 || (item.Style != layout.StyleNodeLabel && item.Style != layout.StyleNodeType && item.Style != layout.StyleEdgeLabel) {
			return layout.Measurements{}, errors.New("unit measure: invalid item")
		}
	}
	return MeasureItems(req.Contract, req.Items), nil
}
func (r *FakeRenderer) Render(ctx context.Context, req layout.RenderRequest) (layout.Rendering, error) {
	// Capture before invoking hooks so callers may safely mutate their own input.
	req.Geometry = copyGeometry(req.Geometry)
	req.WorkflowNodeTypes = cloneTypes(req.WorkflowNodeTypes)
	req.States = slices.Clone(req.States)
	var copyErr error
	req.WorkflowGroups, copyErr = copyGroups(req.WorkflowGroups)
	if copyErr != nil {
		return layout.Rendering{}, copyErr
	}
	r.mu.Lock()
	r.Calls++
	r.inFlight++
	r.maxInFlight = max(r.maxInFlight, r.inFlight)
	r.requests = append(r.requests, req)
	r.mu.Unlock()
	defer func() { r.mu.Lock(); r.inFlight--; r.mu.Unlock() }()
	if err := ctx.Err(); err != nil {
		return layout.Rendering{}, err
	}
	if req.Revision == "" || req.Attempt < 1 || !slices.Equal(req.States, layout.RuntimeStates) {
		return layout.Rendering{}, errors.New("unit render: missing revision/attempt/states")
	}
	if err := validateUnitGeometry(req.Geometry, req.WorkflowNodeTypes, req.WorkflowGroups); err != nil {
		return layout.Rendering{}, err
	}
	if r.OnRender != nil {
		hook := req
		hook.Geometry = copyGeometry(req.Geometry)
		hook.WorkflowNodeTypes = cloneTypes(req.WorkflowNodeTypes)
		hook.States = slices.Clone(req.States)
		hook.WorkflowGroups, _ = copyGroups(req.WorkflowGroups)
		r.OnRender(hook)
	}
	if r.RenderDelay > 0 {
		timer := time.NewTimer(r.RenderDelay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return layout.Rendering{}, ctx.Err()
		case <-timer.C:
		}
	}
	if err := ctx.Err(); err != nil {
		return layout.Rendering{}, err
	}
	if r.RenderErr != nil {
		return layout.Rendering{}, r.RenderErr
	}
	return layout.Rendering{Revision: req.Revision, Attempt: req.Attempt, GeometryDigest: layout.GeometryDigest(req.Geometry), Report: unitReport(req.Geometry, req.WorkflowGroups), Artifacts: Artifacts(req.Geometry), RenderedAt: time.Now().UTC()}, nil
}
func cloneTypes(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
func (r *FakeRenderer) RenderCalls() int  { r.mu.Lock(); defer r.mu.Unlock(); return r.Calls }
func (r *FakeRenderer) MeasureCalls() int { r.mu.Lock(); defer r.mu.Unlock(); return r.measures }
func (r *FakeRenderer) MaxInFlight() int  { r.mu.Lock(); defer r.mu.Unlock(); return r.maxInFlight }
func (r *FakeRenderer) Digests() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.requests))
	for i, q := range r.requests {
		out[i] = layout.GeometryDigest(q.Geometry)
	}
	return out
}
func (r *FakeRenderer) Revisions() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.requests))
	for i, q := range r.requests {
		out[i] = q.Revision
	}
	return out
}

// FakeReviewer checks the entire unit report against the candidate supplied to
// Review. The production reviewer must additionally inspect DOM and PNG bytes.
type FakeReviewer struct {
	Script   []ReviewScript
	mu       sync.Mutex
	attempts []int
	next     int
}

func NewFakeReviewer(script ...ReviewScript) *FakeReviewer {
	r := &FakeReviewer{}
	r.SetScript(script...)
	return r
}
func (r *FakeReviewer) SetScript(script ...ReviewScript) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Script = slices.Clone(script)
	r.next = 0
	for i := range r.Script {
		r.Script[i].Findings = copyFindings(r.Script[i].Findings)
	}
}
func (r *FakeReviewer) Review(ctx context.Context, req layout.ReviewRequest) (layout.Review, error) {
	r.mu.Lock()
	idx := r.next
	r.next++
	r.attempts = append(r.attempts, req.Attempt)
	var script ReviewScript
	if idx < len(r.Script) {
		script = r.Script[idx]
		script.Findings = copyFindings(script.Findings)
	}
	r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return layout.Review{}, err
	}
	if script.Err != nil {
		return layout.Review{}, script.Err
	}
	out := req.Rendering
	g := req.Geometry
	types := map[string]string{}
	for _, n := range g.Nodes {
		types[n.ID] = strings.ReplaceAll(n.TypeCaption, " ", "_")
	}
	if err := validateUnitGeometry(g, types, req.WorkflowGroups); err != nil {
		return layout.Review{}, err
	}
	expected := unitReport(g, req.WorkflowGroups)
	expected.FontsReady = out.Report.FontsReady
	expected.FontFamilyResolved = out.Report.FontFamilyResolved
	if req.Revision == "" || req.Attempt < 1 || out.Revision != req.Revision || out.Attempt != req.Attempt || out.GeometryDigest != layout.GeometryDigest(g) || out.RenderedAt.IsZero() || !reflect.DeepEqual(out.Report, expected) {
		return layout.Review{}, errors.New("unit review: rendering does not match request")
	}
	tiles := 0
	for _, a := range out.Artifacts {
		if a.Kind == "tile" {
			tiles++
		}
	}
	review := layout.Review{Rendered: true, Findings: script.Findings, Evidence: layout.Evidence{GeometryDigest: out.GeometryDigest, RendererVersion: out.Report.SurfaceVersion, Contract: out.Report.Contract, FontsReady: out.Report.FontsReady && out.Report.FontFamilyResolved, Zoom: out.Report.Zoom, Tiles: tiles, Artifacts: slices.Clone(out.Artifacts), ReviewedAt: time.Now().UTC()}}
	if review.Evidence.ReviewedAt.Before(out.RenderedAt) || !layout.HasRenderedEvidence(g, &review) {
		return layout.Review{}, errors.New("unit review: incomplete evidence")
	}
	return review, nil
}
func (r *FakeReviewer) Calls() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.attempts) }
func (r *FakeReviewer) Attempts() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.attempts)
}

func validateUnitGeometry(g layout.Geometry, types map[string]string, groups *workflow.GroupGraph) error {
	if len(g.Nodes) == 0 || len(types) != len(g.Nodes) {
		return errors.New("unit render: missing geometry/types")
	}
	for _, n := range g.Nodes {
		typ, ok := types[n.ID]
		if !ok || typ == "" || strings.ReplaceAll(typ, "_", " ") != n.TypeCaption {
			return errors.New("unit render: type caption mismatch")
		}
	}
	return (layout.ReviewRequest{Geometry: g, WorkflowGroups: groups}).ValidateGeometry()
}
func unitReport(g layout.Geometry, groups *workflow.GroupGraph) layout.RenderReport {
	c := g.Contract
	report := layout.RenderReport{SurfaceVersion: c.Version, Contract: c, FontsReady: true, FontFamilyResolved: true, Zoom: c.PracticalZoom, Canvas: g.Canvas}
	text := func(s string, b layout.Rect, font float64) layout.RenderedText {
		return layout.RenderedText{Content: s, Box: b, Glyphs: []layout.Rect{b}, Visible: true, FontPx: font, Contrast: c.MinContrastRatio}
	}
	for _, n := range g.Nodes {
		report.Nodes = append(report.Nodes, layout.RenderedNode{ID: n.ID, Box: n.Box, Label: text(n.Text, n.Box, c.NodeLabelFontPx), Type: text(n.TypeCaption, n.Box, c.NodeTypeFontPx)})
	}
	for _, e := range g.Edges {
		edge := layout.RenderedEdge{Ordinal: e.Ordinal, Start: e.SourceAttach.Point, End: e.TargetAttach.Point, HasArrow: true, Visible: true, StrokePx: c.StrokePx, StrokeColor: c.ForwardColor, ArrowPoints: unitArrow(e.Points, c.ArrowPx)}
		if e.Kind != layout.EdgeForward {
			edge.Dashed = true
			edge.DashPattern = c.LoopReturnDash
			edge.StrokeColor = c.LoopReturnColor
		}
		for i := 1; i < len(e.Points); i++ {
			edge.Segments = append(edge.Segments, layout.RenderedSegment{Start: e.Points[i-1], End: e.Points[i]})
		}
		if e.Text != nil {
			v := text(e.Text.Text, e.Text.Box, c.EdgeLabelFontPx)
			edge.Label = &v
		}
		report.Edges = append(report.Edges, edge)
	}
	for _, x := range g.Crossings {
		report.Crossings = append(report.Crossings, layout.RenderedCrossing{Point: x.Point, Over: x.Over, Under: x.Under, UnderGapVisible: true, OverContinuous: true})
	}
	for _, e := range g.GroupElements {
		fill := "#1e1e38"
		if e.Role == "container" || e.Role == "lane" {
			fill = "#1a1a2e"
		}
		actual := layout.RenderedGroupElement{ID: e.ID, Box: e.Box, Visible: true, BorderColor: c.ForwardColor, BorderPx: c.NodeBorderPx, BackgroundColor: fill}
		for _, t := range e.Texts {
			px := c.NodeTypeFontPx
			if t.Style == layout.StyleNodeLabel {
				px = c.NodeLabelFontPx
			}
			if t.Style == layout.StyleEdgeLabel {
				px = c.EdgeLabelFontPx
			}
			actual.Texts = append(actual.Texts, layout.RenderedGroupText{Key: t.Key, Text: text(t.Text, t.Box, px)})
		}
		report.GroupElements = append(report.GroupElements, actual)
	}
	for _, e := range g.GroupConnectors {
		actual := layout.RenderedGroupConnector{ID: e.ID, Start: e.SourceAttach.Point, End: e.TargetAttach.Point, Visible: true, HasArrow: true, StrokeColor: c.ForwardColor, StrokePx: c.StrokePx, DashPattern: c.GroupConnectorDash, ArrowPoints: unitArrow(e.Points, c.ArrowPx)}
		for i := 1; i < len(e.Points); i++ {
			actual.Segments = append(actual.Segments, layout.RenderedSegment{Start: e.Points[i-1], End: e.Points[i]})
		}
		report.GroupConnectors = append(report.GroupConnectors, actual)
	}
	for _, x := range g.GroupCrossings {
		report.GroupCrossings = append(report.GroupCrossings, layout.RenderedGroupCrossing{Point: x.Point, Over: x.Over, Under: x.Under, UnderGapVisible: true, OverContinuous: true})
	}
	for _, state := range append(slices.Clone(layout.RuntimeStates), layout.GroupProbeNames(groups)...) {
		report.States = append(report.States, layout.StateProbe{State: state, NodeBoxesStable: true, LabelsReadable: true})
	}
	return report
}

func unitArrow(points []layout.Point, size float64) []layout.Point {
	end, start := points[len(points)-1], points[len(points)-2]
	dx, dy := end.X-start.X, end.Y-start.Y
	l := math.Hypot(dx, dy)
	dx /= l
	dy /= l
	return []layout.Point{{X: end.X - dx*size + dy*size/2, Y: end.Y - dy*size - dx*size/2}, end, {X: end.X - dx*size - dy*size/2, Y: end.Y - dy*size + dx*size/2}}
}
