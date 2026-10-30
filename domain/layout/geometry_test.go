package layout

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func twoNodeGraph(t *testing.T) Graph {
	t.Helper()
	g, err := BuildGraph(&workflow.Workflow{
		Nodes: []workflow.Node{{ID: "a", Type: workflow.NodeTypeScript}, {ID: "b", Type: workflow.NodeTypeConditional, Label: "Gate"}},
		Edges: []workflow.Edge{{Source: "a", Target: "b", Output: "pass"}},
	})
	require.NoError(t, err)
	return g
}

func validTwoNodeGeometry() Geometry {
	return Geometry{
		Format: FormatV2, Contract: DefaultContract(),
		Options: Options{RankSpacing: 48, NodeSpacing: 56, LaneSeparation: 14, GutterWidth: 48, LabelGap: 10, PortSpacing: 24, DummyWidth: 24, CrossingSweeps: 8, Margin: 40},
		Nodes: []NodeBox{
			{ID: "a", Box: Rect{X: 40, Y: 40, W: 180, H: 60}, Text: "a", TypeCaption: "script", TextLines: 1, Rank: 0},
			{ID: "b", Box: Rect{X: 40, Y: 200, W: 180, H: 60}, Text: "Gate", TypeCaption: "conditional", TextLines: 1, Rank: 1},
		},
		Edges: []EdgeRoute{{
			Ordinal: 0, Source: "a", Target: "b", Output: "pass", Kind: EdgeForward,
			Points:       []Point{{X: 130, Y: 100}, {X: 130, Y: 200}},
			SourceAttach: Attachment{NodeID: "a", ElementID: GroupElementID("a", "header", ""), Side: SideBottom, Point: Point{X: 130, Y: 100}},
			TargetAttach: Attachment{NodeID: "b", ElementID: GroupElementID("b", "header", ""), Side: SideTop, Point: Point{X: 130, Y: 200}},
			Text:         &LabelBox{Text: "pass", Box: Rect{X: 112, Y: 110, W: 36, H: 21}, Lines: 1},
		}},
		Crossings: []Crossing{}, Canvas: Rect{W: 260, H: 300},
	}
}

func TestRect_Helpers(t *testing.T) {
	r := Rect{X: 10, Y: 20, W: 100, H: 50}
	assert.Equal(t, 110.0, r.Right())
	assert.Equal(t, 70.0, r.Bottom())
	assert.Equal(t, 60.0, r.CenterX())
	assert.Equal(t, 45.0, r.CenterY())
	assert.Equal(t, Rect{X: 5, Y: 15, W: 110, H: 60}, r.Inflate(5))
	assert.Equal(t, Rect{X: 15, Y: 25, W: 90, H: 40}, r.Inflate(-5))
	assert.True(t, r.Overlaps(Rect{X: 100, Y: 60, W: 20, H: 20}))
	assert.False(t, r.Overlaps(Rect{X: 110, Y: 20, W: 10, H: 10}))
	assert.False(t, r.Overlaps(Rect{X: 10, Y: 70, W: 10, H: 10}))
	assert.True(t, r.Contains(Rect{X: 20, Y: 30, W: 10, H: 10}))
	assert.True(t, r.Contains(r))
	assert.False(t, r.Contains(Rect{X: 105, Y: 30, W: 10, H: 10}))
	assert.False(t, r.Contains(Rect{X: 20, Y: 65, W: 10, H: 10}))
	assert.False(t, r.Contains(Rect{X: math.MaxFloat64, W: math.MaxFloat64, H: 1}))
}

func TestGeometry_ValidateAcceptsMatchingGeometry(t *testing.T) {
	assert.NoError(t, validTwoNodeGeometry().Validate(twoNodeGraph(t)))
}

func TestGeometry_ValidateRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Geometry)
	}{
		{"wrong format", func(g *Geometry) { g.Format = "other/v9" }},
		{"missing node", func(g *Geometry) { g.Nodes = g.Nodes[:1] }},
		{"extra node", func(g *Geometry) { g.Nodes = append(g.Nodes, g.Nodes[0]) }},
		{"node order differs", func(g *Geometry) { g.Nodes[0], g.Nodes[1] = g.Nodes[1], g.Nodes[0] }},
		{"node ID differs", func(g *Geometry) { g.Nodes[0].ID = "other" }},
		{"node text differs", func(g *Geometry) { g.Nodes[1].Text = "Other" }},
		{"node type caption differs", func(g *Geometry) { g.Nodes[0].TypeCaption = "tool call" }},
		{"zero node width", func(g *Geometry) { g.Nodes[0].Box.W = 0 }},
		{"negative node height", func(g *Geometry) { g.Nodes[0].Box.H = -1 }},
		{"NaN node coordinate", func(g *Geometry) { g.Nodes[0].Box.X = math.NaN() }},
		{"Inf route coordinate", func(g *Geometry) { g.Edges[0].Points[1].Y = math.Inf(1) }},
		{"overflow right", func(g *Geometry) { g.Nodes[0].Box.X, g.Nodes[0].Box.W = math.MaxFloat64, math.MaxFloat64 }},
		{"overflow bottom", func(g *Geometry) { g.Nodes[0].Box.Y, g.Nodes[0].Box.H = math.MaxFloat64, math.MaxFloat64 }},
		{"zero node text lines", func(g *Geometry) { g.Nodes[0].TextLines = 0 }},
		{"negative node text lines", func(g *Geometry) { g.Nodes[0].TextLines = -1 }},
		{"negative rank", func(g *Geometry) { g.Nodes[0].Rank = -1 }},
		{"missing edge", func(g *Geometry) { g.Edges = nil }},
		{"extra edge", func(g *Geometry) { g.Edges = append(g.Edges, g.Edges[0]) }},
		{"wrong ordinal", func(g *Geometry) { g.Edges[0].Ordinal = 3 }},
		{"edge source differs", func(g *Geometry) { g.Edges[0].Source = "b" }},
		{"edge target differs", func(g *Geometry) { g.Edges[0].Target = "a" }},
		{"edge output differs", func(g *Geometry) { g.Edges[0].Output = "fail" }},
		{"edge Label differs", func(g *Geometry) { g.Edges[0].Label = "pass" }},
		{"unknown kind", func(g *Geometry) { g.Edges[0].Kind = "diagonal" }},
		{"self-loop kind for distinct endpoints", func(g *Geometry) { g.Edges[0].Kind = EdgeSelfLoop; g.Edges[0].Side = SideRight }},
		{"forward gutter side", func(g *Geometry) { g.Edges[0].Side = SideRight }},
		{"forward gutter lane", func(g *Geometry) { g.Edges[0].Lane = 1 }},
		{"return missing side", func(g *Geometry) { g.Edges[0].Kind = EdgeLoopReturn; g.Edges[0].Lane = 1 }},
		{"return invalid side", func(g *Geometry) { g.Edges[0].Kind = EdgeLoopReturn; g.Edges[0].Side = SideTop; g.Edges[0].Lane = 1 }},
		{"return missing lane", func(g *Geometry) { g.Edges[0].Kind = EdgeLoopReturn; g.Edges[0].Side = SideLeft }},
		{"return negative lane", func(g *Geometry) { g.Edges[0].Kind = EdgeLoopReturn; g.Edges[0].Side = SideLeft; g.Edges[0].Lane = -1 }},
		{"no route", func(g *Geometry) { g.Edges[0].Points = nil }},
		{"single point route", func(g *Geometry) { g.Edges[0].Points = g.Edges[0].Points[:1] }},
		{"diagonal segment", func(g *Geometry) { g.Edges[0].Points[1].X = 150 }},
		{"0.1px diagonal segment", func(g *Geometry) { g.Edges[0].Points[1].X += 0.1 }},
		{"interior diagonal segment", func(g *Geometry) {
			g.Edges[0].Points = []Point{{X: 130, Y: 100}, {X: 130, Y: 110}, {X: 130.1, Y: 120}, {X: 130.1, Y: 140}, {X: 130, Y: 140}, {X: 130, Y: 200}}
		}},
		{"zero length segment", func(g *Geometry) { g.Edges[0].Points = append(g.Edges[0].Points[:1], g.Edges[0].Points...) }},
		{"route outside canvas", func(g *Geometry) {
			g.Edges[0].Points = []Point{{X: 130, Y: 100}, {X: -1, Y: 100}, {X: -1, Y: 200}, {X: 130, Y: 200}}
		}},
		{"source attachment mismatch", func(g *Geometry) { g.Edges[0].SourceAttach.Point.X = 120 }},
		{"target attachment mismatch", func(g *Geometry) { g.Edges[0].TargetAttach.Point.X = 120 }},
		{"source attachment wrong node", func(g *Geometry) { g.Edges[0].SourceAttach.NodeID = "b" }},
		{"target attachment wrong node", func(g *Geometry) { g.Edges[0].TargetAttach.NodeID = "a" }},
		{"source attachment wrong side", func(g *Geometry) { g.Edges[0].SourceAttach.Side = SideTop }},
		{"target attachment wrong side", func(g *Geometry) { g.Edges[0].TargetAttach.Side = SideBottom }},
		{"unknown attachment side", func(g *Geometry) { g.Edges[0].SourceAttach.Side = "other" }},
		{"source off boundary", func(g *Geometry) { g.Edges[0].SourceAttach.Point.Y = 99; g.Edges[0].Points[0].Y = 99 }},
		{"target off boundary", func(g *Geometry) { g.Edges[0].TargetAttach.Point.Y = 201; g.Edges[0].Points[1].Y = 201 }},
		{"attachment at corner", func(g *Geometry) {
			for i := range g.Edges[0].Points {
				g.Edges[0].Points[i].X = 40
			}
			g.Edges[0].SourceAttach.Point.X = 40
			g.Edges[0].TargetAttach.Point.X = 40
		}},
		{"attachment beyond side", func(g *Geometry) {
			for i := range g.Edges[0].Points {
				g.Edges[0].Points[i].X = 230
			}
			g.Edges[0].SourceAttach.Point.X = 230
			g.Edges[0].TargetAttach.Point.X = 230
		}},
		{"NaN attachment", func(g *Geometry) { g.Edges[0].SourceAttach.Point.X = math.NaN() }},
		{"branch label missing", func(g *Geometry) { g.Edges[0].Text = nil }},
		{"branch text differs", func(g *Geometry) { g.Edges[0].Text.Text = "fail" }},
		{"zero label width", func(g *Geometry) { g.Edges[0].Text.Box.W = 0 }},
		{"negative label height", func(g *Geometry) { g.Edges[0].Text.Box.H = -1 }},
		{"nonfinite label", func(g *Geometry) { g.Edges[0].Text.Box.Y = math.NaN() }},
		{"label outside canvas", func(g *Geometry) { g.Edges[0].Text.Box.X = 250 }},
		{"zero label lines", func(g *Geometry) { g.Edges[0].Text.Lines = 0 }},
		{"negative label lines", func(g *Geometry) { g.Edges[0].Text.Lines = -1 }},
		{"crossing unknown edge", func(g *Geometry) { g.Crossings = []Crossing{{Point: Point{X: 1, Y: 1}, Over: 0, Under: 7}} }},
		{"canvas does not contain node", func(g *Geometry) { g.Canvas.H = 150 }},
		{"zero canvas width", func(g *Geometry) { g.Canvas.W = 0 }},
		{"negative canvas height", func(g *Geometry) { g.Canvas.H = -1 }},
		{"nonfinite canvas", func(g *Geometry) { g.Canvas.W = math.Inf(1) }},
		{"nonzero canvas X", func(g *Geometry) { g.Canvas.X = 0.001 }},
		{"nonzero canvas Y", func(g *Geometry) { g.Canvas.Y = -0.001 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			geo := validTwoNodeGeometry()
			tc.mutate(&geo)
			assert.ErrorIs(t, geo.Validate(twoNodeGraph(t)), ErrInvalidGeometry)
		})
	}
}

func TestGeometry_UnconnectedNodeBounds(t *testing.T) {
	for _, tc := range []struct {
		name string
		box  Rect
	}{
		{"zero width", Rect{X: 40, Y: 40, H: 60}},
		{"negative height", Rect{X: 40, Y: 40, W: 180, H: -1}},
		{"outside canvas", Rect{X: 300, Y: 40, W: 180, H: 60}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, geo := twoNodeGraph(t), validTwoNodeGeometry()
			g.Edges, geo.Edges = nil, nil
			geo.Nodes[0].Box = tc.box
			assert.ErrorIs(t, geo.Validate(g), ErrInvalidGeometry)
		})
	}
}

func TestGeometry_ValidateContractFields(t *testing.T) {
	v := reflect.ValueOf(DefaultContract())
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		var bad []any
		switch v.Field(i).Kind() {
		case reflect.String:
			bad = []any{""}
		case reflect.Int:
			bad = []any{0, -1, 99, 901}
		case reflect.Float64:
			bad = []any{0.0, -1.0, math.NaN(), math.Inf(1), math.Inf(-1)}
			if name == "CornerRadiusPx" {
				bad = bad[1:] // 0 means sharp corners
			}
		}
		for _, value := range bad {
			t.Run(name+"/"+fmt.Sprint(value), func(t *testing.T) {
				geo := validTwoNodeGeometry()
				reflect.ValueOf(&geo.Contract).Elem().Field(i).Set(reflect.ValueOf(value))
				assert.ErrorIs(t, geo.Validate(twoNodeGraph(t)), ErrInvalidGeometry)
			})
		}
	}
	for _, tc := range []struct {
		name   string
		mutate func(*RendererContract)
	}{
		{"zoom above one", func(c *RendererContract) { c.PracticalZoom = 1.01 }},
		{"impossible contrast", func(c *RendererContract) { c.MinContrastRatio = 21.01 }},
		{"overflow font height", func(c *RendererContract) { c.NodeLabelFontPx = math.MaxFloat64 }},
		{"overflow type height", func(c *RendererContract) { c.NodeTypeFontPx = math.MaxFloat64 }},
		{"overflow label height", func(c *RendererContract) { c.EdgeLabelFontPx = math.MaxFloat64 }},
		{"overflow padding", func(c *RendererContract) { c.NodePaddingX = math.MaxFloat64 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			geo := validTwoNodeGeometry()
			tc.mutate(&geo.Contract)
			assert.ErrorIs(t, geo.Validate(twoNodeGraph(t)), ErrInvalidGeometry)
		})
	}
}

func TestGeometry_ValidateOptionsFields(t *testing.T) {
	v := reflect.ValueOf(validTwoNodeGeometry().Options)
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		var bad []any
		if v.Field(i).Kind() == reflect.Int {
			bad = []any{-1}
		} else {
			bad = []any{-1.0, math.NaN(), math.Inf(1), math.Inf(-1)}
			if name != "NodeTextSlack" && name != "Margin" {
				bad = append(bad, 0.0)
			}
		}
		for _, value := range bad {
			t.Run(name+"/"+fmt.Sprint(value), func(t *testing.T) {
				geo := validTwoNodeGeometry()
				reflect.ValueOf(&geo.Options).Elem().Field(i).Set(reflect.ValueOf(value))
				assert.ErrorIs(t, geo.Validate(twoNodeGraph(t)), ErrInvalidGeometry)
			})
		}
	}
}

func TestGeometry_ValidateRejectsEmptyGraph(t *testing.T) {
	assert.ErrorIs(t, validTwoNodeGeometry().Validate(Graph{}), ErrInvalidGeometry)
	geo := validTwoNodeGeometry()
	geo.Nodes, geo.Edges = nil, nil
	assert.ErrorIs(t, geo.Validate(Graph{}), ErrInvalidGeometry)
}

func TestGeometry_ValidateRejectsUnexpectedLabel(t *testing.T) {
	g := twoNodeGraph(t)
	g.Edges[0].Output, g.Edges[0].Text = "", ""
	geo := validTwoNodeGeometry()
	geo.Edges[0].Output = ""
	assert.ErrorIs(t, geo.Validate(g), ErrInvalidGeometry)
}

func TestGeometry_ValidVariantsAndRoundTrips(t *testing.T) {
	for _, variant := range []string{"no edges", "parallel edges", "Unicode", "label precedence", "zero policy values", "custom contract", "quantized route", "loop return", "self loop"} {
		t.Run(variant, func(t *testing.T) {
			g, geo := twoNodeGraph(t), validTwoNodeGeometry()
			switch variant {
			case "no edges":
				g.Edges, geo.Edges = []GraphEdge{}, []EdgeRoute{}
			case "parallel edges":
				g.Edges = append(g.Edges, g.Edges[0])
				g.Edges[1].Ordinal = 1
				geo.Edges = append(geo.Edges, geo.Edges[0])
				geo.Edges[1].Ordinal = 1
			case "Unicode":
				g.Nodes[1].Text = "Überprüfung — 検証ステップ ✅"
				geo.Nodes[1].Text = g.Nodes[1].Text
				g.Edges[0].Output, g.Edges[0].Text = "続行 ✅", "続行 ✅"
				geo.Edges[0].Output, geo.Edges[0].Text.Text = g.Edges[0].Output, g.Edges[0].Text
			case "label precedence":
				g.Edges[0].Label, g.Edges[0].Text = "explicit label", "explicit label"
				geo.Edges[0].Label, geo.Edges[0].Text.Text = g.Edges[0].Label, g.Edges[0].Text
			case "zero policy values":
				geo.Options.Margin, geo.Options.NodeTextSlack, geo.Options.CrossingSweeps = 0, 0, 0
			case "custom contract":
				geo.Contract.Version = "arranged-renderer/0"
				geo.Contract.NodeLabelFontPx = 14
			case "quantized route":
				geo.Edges[0].Points[1].X += 0.004
			case "loop return":
				g.Edges[0].Source, g.Edges[0].Target = "b", "a"
				e := &geo.Edges[0]
				e.Source, e.Target, e.Kind, e.Side, e.Lane = "b", "a", EdgeLoopReturn, SideRight, 1
				e.Points = []Point{{X: 130, Y: 260}, {X: 130, Y: 280}, {X: 240, Y: 280}, {X: 240, Y: 20}, {X: 130, Y: 20}, {X: 130, Y: 40}}
				e.SourceAttach = Attachment{NodeID: "b", ElementID: GroupElementID("b", "header", ""), Side: SideBottom, Point: e.Points[0]}
				e.TargetAttach = Attachment{NodeID: "a", ElementID: GroupElementID("a", "header", ""), Side: SideTop, Point: e.Points[5]}
			case "self loop":
				g, geo = selfLoopGeometry(t)
			}
			require.NoError(t, geo.Validate(g))
			assertGeometryRoundTrip(t, g, geo)
		})
	}
}

func properCrossingGeometry(t *testing.T) (Graph, Geometry) {
	t.Helper()
	g, err := BuildGraph(&workflow.Workflow{
		Nodes: []workflow.Node{{ID: "a", Type: workflow.NodeTypeScript}, {ID: "b", Type: workflow.NodeTypeScript}, {ID: "c", Type: workflow.NodeTypeScript}, {ID: "d", Type: workflow.NodeTypeScript}},
		Edges: []workflow.Edge{{Source: "a", Target: "b"}, {Source: "c", Target: "d"}},
	})
	require.NoError(t, err)
	geo := validTwoNodeGeometry()
	geo.Nodes = []NodeBox{
		{ID: "a", Box: Rect{X: 40, Y: 40, W: 180, H: 60}, Text: "a", TypeCaption: "script", TextLines: 1},
		{ID: "b", Box: Rect{X: 400, Y: 200, W: 180, H: 60}, Text: "b", TypeCaption: "script", TextLines: 1, Rank: 1},
		{ID: "c", Box: Rect{X: 240, Y: 40, W: 180, H: 60}, Text: "c", TypeCaption: "script", TextLines: 1},
		{ID: "d", Box: Rect{X: 240, Y: 200, W: 180, H: 60}, Text: "d", TypeCaption: "script", TextLines: 1, Rank: 1},
	}
	geo.Edges = []EdgeRoute{
		{Ordinal: 0, Source: "a", Target: "b", Kind: EdgeForward, Points: []Point{{X: 130, Y: 100}, {X: 130, Y: 150}, {X: 490, Y: 150}, {X: 490, Y: 200}}, SourceAttach: Attachment{NodeID: "a", Side: SideBottom, Point: Point{X: 130, Y: 100}}, TargetAttach: Attachment{NodeID: "b", Side: SideTop, Point: Point{X: 490, Y: 200}}},
		{Ordinal: 1, Source: "c", Target: "d", Kind: EdgeForward, Points: []Point{{X: 330, Y: 100}, {X: 330, Y: 200}}, SourceAttach: Attachment{NodeID: "c", Side: SideBottom, Point: Point{X: 330, Y: 100}}, TargetAttach: Attachment{NodeID: "d", Side: SideTop, Point: Point{X: 330, Y: 200}}},
	}
	for i := range geo.Edges {
		e := &geo.Edges[i]
		e.SourceAttach.ElementID = GroupElementID(e.Source, "header", "")
		e.TargetAttach.ElementID = GroupElementID(e.Target, "header", "")
	}
	geo.Crossings = []Crossing{{Point: Point{X: 330, Y: 150}, Over: 0, Under: 1}}
	geo.Canvas.W = 620
	return g, geo
}

func TestGeometry_ProperCrossings(t *testing.T) {
	g, geo := properCrossingGeometry(t)
	require.NoError(t, geo.Validate(g))
	assertGeometryRoundTrip(t, g, geo)
	for _, tc := range []struct {
		name   string
		mutate func(*Geometry)
	}{
		{"same edge", func(g *Geometry) { g.Crossings[0].Under = 0 }},
		{"negative ordinal", func(g *Geometry) { g.Crossings[0].Over = -1 }},
		{"out of range ordinal", func(g *Geometry) { g.Crossings[0].Under = 2 }},
		{"nonfinite point", func(g *Geometry) { g.Crossings[0].Point.X = math.NaN() }},
		{"out of canvas", func(g *Geometry) { g.Crossings[0].Point.X = 621 }},
		{"fabricated point", func(g *Geometry) { g.Crossings[0].Point.X = 320 }},
		{"duplicate", func(g *Geometry) { g.Crossings = append(g.Crossings, g.Crossings[0]) }},
		{"reversed orientation", func(g *Geometry) { g.Crossings[0].Over, g.Crossings[0].Under = 1, 0 }},
		{"at bend", func(g *Geometry) {
			g.Edges[0].Points = append(g.Edges[0].Points[:2], append([]Point{{X: 330, Y: 150}}, g.Edges[0].Points[2:]...)...)
		}},
		{"at endpoint", func(g *Geometry) { g.Crossings[0].Point.Y = 100 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, geo := properCrossingGeometry(t)
			tc.mutate(&geo)
			assert.ErrorIs(t, geo.Validate(g), ErrInvalidGeometry)
		})
	}
	geo.Crossings = nil
	assert.NoError(t, geo.Validate(g), "missing crossings are semantic preflight findings")
}

func assertGeometryRoundTrip(t *testing.T, g Graph, geo Geometry) {
	t.Helper()
	raw, err := json.Marshal(geo)
	require.NoError(t, err)
	var back Geometry
	require.NoError(t, json.Unmarshal(raw, &back))
	assert.Equal(t, geo, back)
	assert.Equal(t, GeometryDigest(geo), GeometryDigest(back))
	assert.NoError(t, back.Validate(g))
}

func TestGeometry_JSONRoundTrip(t *testing.T) {
	geo := validTwoNodeGeometry()
	assertGeometryRoundTrip(t, twoNodeGraph(t), geo)
	raw, err := json.Marshal(geo)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"format":"arbitron.layout/v2"`)
	assert.Contains(t, string(raw), `"sourceAttach"`)
}

func TestGeometryDigest_StableAndSensitive(t *testing.T) {
	a, b := validTwoNodeGeometry(), validTwoNodeGeometry()
	assert.Equal(t, GeometryDigest(a), GeometryDigest(b))
	assert.Regexp(t, "^[0-9a-f]{64}$", GeometryDigest(a))
	b.Nodes[1].Box.Y++
	assert.NotEqual(t, GeometryDigest(a), GeometryDigest(b))
	for _, mutate := range []func(*Geometry){
		func(g *Geometry) { g.Contract.NodePaddingX++ },
		func(g *Geometry) { g.Options.Margin++ },
		func(g *Geometry) { g.Edges[0].Label = "changed" },
		func(g *Geometry) { g.Edges[0].Text.Text = "changed" },
		func(g *Geometry) { g.Crossings = []Crossing{{Over: 0, Under: 1}} },
	} {
		b = validTwoNodeGeometry()
		mutate(&b)
		assert.NotEqual(t, GeometryDigest(a), GeometryDigest(b))
	}
	b.Nodes[0].Box.X = math.NaN()
	assert.Empty(t, GeometryDigest(b), "unmarshalable geometry has no digest")
	b = validTwoNodeGeometry()
	b.Contract.StrokePx = math.Inf(1)
	assert.Empty(t, GeometryDigest(b))
}

func TestCompatible(t *testing.T) {
	g, geo := twoNodeGraph(t), validTwoNodeGeometry()
	assert.True(t, Compatible(geo, g))
	broken := validTwoNodeGeometry()
	broken.Nodes = broken.Nodes[:1]
	assert.False(t, Compatible(broken, g))
	// Same-version font/padding/crossing changes must fail too, not just version drift.
	v := reflect.ValueOf(&geo.Contract).Elem()
	for i := 0; i < v.NumField(); i++ {
		t.Run(v.Type().Field(i).Name, func(t *testing.T) {
			changed := validTwoNodeGeometry()
			f := reflect.ValueOf(&changed.Contract).Elem().Field(i)
			switch f.Kind() {
			case reflect.String:
				f.SetString(f.String() + "/different")
			case reflect.Int:
				f.SetInt(f.Int() + 1)
			case reflect.Float64:
				f.SetFloat(f.Float() + 0.01)
			}
			require.NoError(t, changed.Validate(g), "contract is structurally sound but not this renderer's")
			assert.False(t, Compatible(changed, g))
		})
	}
}

// These inputs remain otherwise structurally valid: rejection must depend on
// comparison with the manifest producer, not an unrelated route/label failure.
func TestGeometry_ManifestIdentityGuards(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*workflow.Workflow)
	}{
		{"node ID", func(w *workflow.Workflow) { w.Edges = nil; w.Nodes[0].ID = "changed"; w.Nodes[0].Label = "a" }},
		{"node text", func(w *workflow.Workflow) { w.Nodes[1].Label = "changed" }},
		{"node caption", func(w *workflow.Workflow) { w.Nodes[0].Type = workflow.NodeTypeToolCall }},
		{"edge source", func(w *workflow.Workflow) { w.Edges[0].Source = "b" }},
		{"edge target", func(w *workflow.Workflow) { w.Edges[0].Target = "a" }},
		{"edge output", func(w *workflow.Workflow) { w.Edges[0].Output = "changed" }},
		{"edge label", func(w *workflow.Workflow) { w.Edges[0].Label = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wf := &workflow.Workflow{
				Nodes: []workflow.Node{{ID: "a", Type: workflow.NodeTypeScript}, {ID: "b", Type: workflow.NodeTypeConditional, Label: "Gate"}},
				Edges: []workflow.Edge{{Source: "a", Target: "b", Output: "pass", Label: "pass"}},
			}
			geo := validTwoNodeGeometry()
			geo.Edges[0].Label = "pass"
			tc.mutate(wf)
			if wf.Edges == nil {
				geo.Edges = nil
			}
			g, err := BuildGraph(wf)
			require.NoError(t, err)
			assert.ErrorIs(t, geo.Validate(g), ErrInvalidGeometry)
			assert.False(t, Compatible(geo, g))
		})
	}
}

func TestGeometry_KindMatchesEndpointIdentity(t *testing.T) {
	t.Run("self endpoints with forward metadata", func(t *testing.T) {
		g, geo := selfLoopGeometry(t)
		e := &geo.Edges[0]
		e.Kind, e.Side = EdgeForward, ""
		e.Points = []Point{{X: 130, Y: 100}, {X: 130, Y: 40}}
		e.SourceAttach = Attachment{NodeID: "a", Side: SideBottom, Point: e.Points[0]}
		e.TargetAttach = Attachment{NodeID: "a", Side: SideTop, Point: e.Points[1]}
		assert.ErrorIs(t, geo.Validate(g), ErrInvalidGeometry)
	})
	t.Run("distinct endpoints with self metadata", func(t *testing.T) {
		g, geo := twoNodeGraph(t), validTwoNodeGeometry()
		e := &geo.Edges[0]
		e.Kind, e.Side = EdgeSelfLoop, SideRight
		e.Points = []Point{{X: 220, Y: 60}, {X: 240, Y: 60}, {X: 240, Y: 220}, {X: 220, Y: 220}}
		e.SourceAttach = Attachment{NodeID: "a", Side: SideRight, Point: e.Points[0]}
		e.TargetAttach = Attachment{NodeID: "b", Side: SideRight, Point: e.Points[3]}
		assert.ErrorIs(t, geo.Validate(g), ErrInvalidGeometry)
	})
}

func TestGeometry_AttachmentSidePolicy(t *testing.T) {
	for _, source := range []bool{true, false} {
		t.Run(fmt.Sprint("source=", source), func(t *testing.T) {
			geo := validTwoNodeGeometry()
			e := &geo.Edges[0]
			if source {
				e.SourceAttach.Side, e.SourceAttach.Point.Y, e.Points[0].Y = SideTop, 40, 40
			} else {
				e.TargetAttach.Side, e.TargetAttach.Point.Y, e.Points[1].Y = SideBottom, 260, 260
			}
			assert.ErrorIs(t, geo.Validate(twoNodeGraph(t)), ErrInvalidGeometry)
		})
	}
}

func selfLoopGeometry(t *testing.T) (Graph, Geometry) {
	t.Helper()
	g, err := BuildGraph(&workflow.Workflow{
		Nodes: []workflow.Node{{ID: "a", Type: workflow.NodeTypeScript}, {ID: "b", Type: workflow.NodeTypeConditional, Label: "Gate"}},
		Edges: []workflow.Edge{{Source: "a", Target: "a", Output: "pass"}},
	})
	require.NoError(t, err)
	geo := validTwoNodeGeometry()
	e := &geo.Edges[0]
	e.Target, e.Kind, e.Side = "a", EdgeSelfLoop, SideRight
	e.Points = []Point{{X: 220, Y: 60}, {X: 240, Y: 60}, {X: 240, Y: 80}, {X: 220, Y: 80}}
	e.SourceAttach = Attachment{NodeID: "a", Side: SideRight, Point: e.Points[0]}
	e.TargetAttach = Attachment{NodeID: "a", Side: SideRight, Point: e.Points[3]}
	e.SourceAttach.ElementID = GroupElementID("a", "header", "")
	e.TargetAttach.ElementID = GroupElementID("a", "header", "")
	return g, geo
}

func TestGeometry_SelfLoopMetadata(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*EdgeRoute)
	}{
		{"forward kind", func(e *EdgeRoute) { e.Kind, e.Side = EdgeForward, "" }},
		{"return kind", func(e *EdgeRoute) { e.Kind, e.Lane = EdgeLoopReturn, 1 }},
		{"left side", func(e *EdgeRoute) { e.Side = SideLeft }},
		{"negative lane", func(e *EdgeRoute) { e.Lane = -1 }},
		{"source bottom", func(e *EdgeRoute) {
			e.Points[0], e.Points[1] = Point{X: 130, Y: 100}, Point{X: 240, Y: 100}
			e.SourceAttach.Side, e.SourceAttach.Point = SideBottom, e.Points[0]
		}},
		{"target top", func(e *EdgeRoute) {
			e.Points[2], e.Points[3] = Point{X: 240, Y: 40}, Point{X: 130, Y: 40}
			e.TargetAttach.Side, e.TargetAttach.Point = SideTop, e.Points[3]
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, geo := selfLoopGeometry(t)
			tc.mutate(&geo.Edges[0])
			assert.ErrorIs(t, geo.Validate(g), ErrInvalidGeometry)
		})
	}
}

func TestGeometry_OnSide(t *testing.T) {
	r := Rect{X: 10, Y: 20, W: 100, H: 60}
	for _, tc := range []struct {
		side  Side
		point Point
	}{
		{SideTop, Point{X: 60, Y: 20}}, {SideBottom, Point{X: 60, Y: 80}},
		{SideLeft, Point{X: 10, Y: 50}}, {SideRight, Point{X: 110, Y: 50}},
	} {
		t.Run(string(tc.side), func(t *testing.T) {
			assert.True(t, onSide(tc.point, tc.side, r))
			assert.False(t, onSide(Point{X: 60, Y: 50}, tc.side, r))
			assert.False(t, onSide(Point{X: 10, Y: 20}, tc.side, r))
			assert.False(t, onSide(Point{X: 110, Y: 80}, tc.side, r))
			assert.False(t, onSide(tc.point, tc.side, Rect{X: math.MaxFloat64, W: math.MaxFloat64, H: 10}))
			assert.False(t, onSide(Point{X: math.Inf(1)}, tc.side, r))
		})
	}
	assert.False(t, onSide(Point{X: 60, Y: 20}, "unknown", r))
}

func TestGeometry_ValidateDoesNotMutate(t *testing.T) {
	g, geo := twoNodeGraph(t), validTwoNodeGeometry()
	before, err := json.Marshal(geo)
	require.NoError(t, err)
	graphBefore, err := json.Marshal(g)
	require.NoError(t, err)
	require.NoError(t, geo.Validate(g))
	after, err := json.Marshal(geo)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	graphAfter, err := json.Marshal(g)
	require.NoError(t, err)
	assert.Equal(t, graphBefore, graphAfter)
}
