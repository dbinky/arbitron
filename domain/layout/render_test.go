package layout

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Pins the render surface's wire schema with synthetic Unicode text, SVG
// subpaths and false evidence values. This is not a rendered acceptance test.
func TestRenderReport_JSONKeys(t *testing.T) {
	r := RenderReport{
		SurfaceVersion: ContractVersion, Contract: DefaultContract(), FontsReady: true, FontFamilyResolved: true,
		Zoom: 0.9, Canvas: Rect{W: 100, H: 200},
		Nodes: []RenderedNode{{ID: "a", Box: Rect{X: 1, Y: 2, W: 3, H: 4},
			Label: RenderedText{Content: "検証 e\u0301 😀", Glyphs: []Rect{{X: 1, Y: 2, W: 3, H: 4}, {X: 1, Y: 6, W: 2, H: 4}}, Visible: false,
				Box: Rect{W: 1, H: 1}, Clipped: true, FontPx: 13, Contrast: 9.1, Occluded: false},
			Type: RenderedText{Content: "agent call", Visible: true, FontPx: 10}}},
		Edges: []RenderedEdge{{Ordinal: 0, Start: Point{X: 1, Y: 2}, End: Point{X: 3, Y: 4}, HasArrow: true, Dashed: false, StrokePx: 2,
			Segments:    []RenderedSegment{{Start: Point{X: 1, Y: 2}, End: Point{X: 1, Y: 3}}, {Start: Point{X: 1, Y: 4}, End: Point{X: 3, Y: 4}}},
			StrokeColor: "rgb(138, 138, 176)", DashPattern: "6 4", Visible: false,
			ArrowPoints: []Point{{X: 3, Y: 4}, {X: 1, Y: 3}, {X: 1, Y: 5}},
			Label:       &RenderedText{Content: "再試行", Glyphs: []Rect{{W: 22, H: 15}}, Visible: true}}},
		Crossings: []RenderedCrossing{{Point: Point{X: 5, Y: 6}, Over: 0, Under: 1, UnderGapVisible: true, OverContinuous: true}},
		States:    []StateProbe{{State: "running", NodeBoxesStable: true, LabelsReadable: true}},
	}
	raw, err := json.Marshal(r)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	for _, k := range []string{"surfaceVersion", "contract", "fontsReady", "fontFamilyResolved", "zoom", "canvas", "nodes", "edges", "crossings", "states"} {
		require.Contains(t, m, k)
	}
	node := m["nodes"].([]any)[0].(map[string]any)
	for _, k := range []string{"id", "box", "label", "type"} {
		require.Contains(t, node, k)
	}
	label := node["label"].(map[string]any)
	for _, k := range []string{"content", "glyphs", "visible", "box", "clipped", "fontPx", "contrast", "occluded"} {
		require.Contains(t, label, k)
	}
	edge := m["edges"].([]any)[0].(map[string]any)
	for _, k := range []string{"segments", "strokeColor", "dashPattern", "visible", "arrowPoints", "ordinal", "start", "end", "hasArrow", "dashed", "strokePx", "label"} {
		require.Contains(t, edge, k)
	}
	crossing := m["crossings"].([]any)[0].(map[string]any)
	for _, k := range []string{"point", "over", "under", "underGapVisible", "overContinuous"} {
		require.Contains(t, crossing, k)
	}
	probe := m["states"].([]any)[0].(map[string]any)
	for _, k := range []string{"state", "nodeBoxesStable", "labelsReadable"} {
		require.Contains(t, probe, k)
	}
	assert.Equal(t, "検証 e\u0301 😀", label["content"])
	assert.Equal(t, false, label["visible"], "false visibility is evidence, not an omitted field")
	assert.Equal(t, false, label["occluded"])
	assert.Equal(t, false, edge["visible"])
	assert.Equal(t, false, edge["dashed"])
	segments := edge["segments"].([]any)
	require.Len(t, segments, 2, "SVG subpath gaps must survive serialization")
	assert.Equal(t, map[string]any{"start": map[string]any{"x": float64(1), "y": float64(4)}, "end": map[string]any{"x": float64(3), "y": float64(4)}}, segments[1])
	assert.Equal(t, "rgb(138, 138, 176)", edge["strokeColor"])
	assert.Equal(t, "6 4", edge["dashPattern"])
	assert.Len(t, edge["arrowPoints"], 3)
	assert.Len(t, label["glyphs"], 2)

	var back RenderReport
	require.NoError(t, json.Unmarshal(raw, &back))
	assert.Equal(t, r, back)
}

func TestRenderReport_FalseValuesAreNotOmitted(t *testing.T) {
	raw, err := json.Marshal(RenderReport{Nodes: []RenderedNode{{}}, Edges: []RenderedEdge{{}}, Crossings: []RenderedCrossing{{}}, States: []StateProbe{{}}})
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	for _, k := range []string{"fontsReady", "fontFamilyResolved"} {
		v, ok := m[k]
		assert.True(t, ok, k)
		assert.Equal(t, false, v, k)
	}
	for _, tc := range []struct {
		object map[string]any
		keys   []string
	}{
		{m["nodes"].([]any)[0].(map[string]any)["label"].(map[string]any), []string{"visible", "clipped", "occluded"}},
		{m["edges"].([]any)[0].(map[string]any), []string{"visible", "hasArrow", "dashed"}},
		{m["crossings"].([]any)[0].(map[string]any), []string{"underGapVisible", "overContinuous"}},
		{m["states"].([]any)[0].(map[string]any), []string{"nodeBoxesStable", "labelsReadable"}},
	} {
		for _, k := range tc.keys {
			v, ok := tc.object[k]
			assert.True(t, ok, k)
			assert.Equal(t, false, v, k)
		}
	}
}

func TestNewRenderRequest_ProbesEveryRuntimeState(t *testing.T) {
	geo := validTwoNodeGeometry()
	types := map[string]string{"a": "agent_call", "b": "conditional"}
	req := NewRenderRequest("rev-1", 2, geo, types)
	require.Equal(t, types, req.WorkflowNodeTypes)
	types["a"] = "mutated"
	assert.Equal(t, "agent_call", req.WorkflowNodeTypes["a"], "raw types are defensively copied")
	assert.Equal(t, "rev-1", req.Revision)
	assert.Equal(t, 2, req.Attempt)
	assert.Equal(t, geo, req.Geometry)
	require.Equal(t, []string{"pending", "running", "completed", "failed", "waiting", "skipped", "selected"}, req.States)

	req.States[0] = "mutated"
	assert.Equal(t, "pending", RuntimeStates[0], "callers get a copy of the state list")
	req.WorkflowNodeTypes["b"] = "mutated"
	assert.Equal(t, "conditional", types["b"], "request mutation must not change the captured input")
}

func TestNewRenderRequest_WireKeysPreserveRawNodeTypes(t *testing.T) {
	geo := validTwoNodeGeometry()
	geo.Nodes[0].TypeCaption = "agent call"
	geo.Nodes[1].TypeCaption = "conditional"
	types := map[string]string{"a": "agent_call", "b": "conditional"}
	req := NewRenderRequest("rev-raw-types", 3, geo, types)
	raw, err := json.Marshal(req)
	require.NoError(t, err)
	var wire map[string]any
	require.NoError(t, json.Unmarshal(raw, &wire))
	require.Len(t, wire, 5)
	assert.Equal(t, "rev-raw-types", wire["revision"])
	assert.Equal(t, float64(3), wire["attempt"])
	assert.Equal(t, map[string]any{"a": "agent_call", "b": "conditional"}, wire["workflowNodeTypes"])
	assert.Len(t, wire["states"], 7)
	assert.NotEmpty(t, wire["geometry"])
	var back RenderRequest
	require.NoError(t, json.Unmarshal(raw, &back))
	assert.Equal(t, req, back)
}

// Compile-time proof only; accidental runtime use cannot fabricate evidence.
type portProbe struct{}

func (portProbe) Measure(context.Context, MeasureRequest) (Measurements, error) {
	panic("compile-time signature probe only")
}
func (portProbe) Render(context.Context, RenderRequest) (Rendering, error) {
	panic("compile-time signature probe only")
}
func (portProbe) Review(context.Context, ReviewRequest) (Review, error) {
	panic("compile-time signature probe only")
}

var (
	_ Renderer = portProbe{}
	_ Reviewer = portProbe{}
)

func TestEvidence_JSONRoundTrip(t *testing.T) {
	ev := Evidence{GeometryDigest: "abc", RendererVersion: ContractVersion, Contract: DefaultContract(), FontsReady: true, Zoom: 0.9, Tiles: 6,
		Artifacts:  []Artifact{{Kind: "tile", Path: "/tmp/t.png", SHA256: "ff", Region: Rect{W: 10, H: 10}, Scale: 0.9}},
		ReviewedAt: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
	raw, err := json.Marshal(Review{Rendered: true, Findings: []Finding{}, Evidence: ev})
	require.NoError(t, err)
	var back Review
	require.NoError(t, json.Unmarshal(raw, &back))
	assert.Equal(t, ev, back.Evidence)
	assert.True(t, back.Rendered)
}

func TestEvidence_AbsentReviewDoesNotClaimRendered(t *testing.T) {
	for _, raw := range []string{`{}`, `null`, `{"rendered":false,"evidence":{}}`} {
		var review Review
		require.NoError(t, json.Unmarshal([]byte(raw), &review))
		assert.False(t, review.Rendered)
		assert.Empty(t, review.Evidence.GeometryDigest)
		assert.Empty(t, review.Evidence.Artifacts)
		assert.False(t, review.Evidence.FontsReady)
	}
	var report RenderReport
	require.Error(t, json.Unmarshal([]byte(`{"nodes":[{"label":{"glyphs":"invalid"}}]}`), &report))
}
