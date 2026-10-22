package layout

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMeasureKeys(t *testing.T) {
	assert.Equal(t, "node:init:label", NodeLabelKey("init"))
	assert.Equal(t, "node:init:type", NodeTypeKey("init"))
	assert.Equal(t, "edge:7", EdgeLabelKey(7))
}

func TestMeasureRequestFor_OneItemPerDisplayedText(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{
		Nodes: []workflow.Node{{ID: "a", Type: workflow.NodeTypeScript, Label: "Init"}, {ID: "b", Type: workflow.NodeTypeConditional}},
		Edges: []workflow.Edge{{Source: "a", Target: "b"}, {Source: "b", Target: "a", Output: "retry"}},
	})
	require.NoError(t, err)
	c := DefaultContract()
	req := MeasureRequestFor(g, c)
	assert.Equal(t, c, req.Contract)
	assert.Equal(t, []TextItem{
		{Key: "node:a:label", Text: "Init", Style: StyleNodeLabel, MaxWidth: c.NodeTextMaxWidth},
		{Key: "node:a:type", Text: "script", Style: StyleNodeType, MaxWidth: c.NodeTextMaxWidth},
		{Key: "node:b:label", Text: "b", Style: StyleNodeLabel, MaxWidth: c.NodeTextMaxWidth},
		{Key: "node:b:type", Text: "conditional", Style: StyleNodeType, MaxWidth: c.NodeTextMaxWidth},
		{Key: "edge:1", Text: "retry", Style: StyleEdgeLabel, MaxWidth: c.EdgeLabelMaxWidth},
	}, req.Items, "unlabelled edges are not measured")
}

func TestMeasurements_Metric(t *testing.T) {
	m := Measurements{Items: map[string]TextMetrics{
		"ok":   {Width: 10, Height: 17.55, Lines: 1},
		"zero": {},
	}}
	got, err := m.Metric("ok")
	require.NoError(t, err)
	assert.Equal(t, 10.0, got.Width)
	_, err = m.Metric("absent")
	assert.ErrorIs(t, err, ErrMissingMeasurement)
	z, err := m.Metric("zero")
	require.NoError(t, err, "empty text legitimately measures zero")
	assert.Equal(t, TextMetrics{}, z)
}

func TestMeasurements_MetricRejectsInvalidValues(t *testing.T) {
	for name, tm := range map[string]TextMetrics{
		"nan width":       {Width: math.NaN(), Height: 1, Lines: 1},
		"nan height":      {Width: 1, Height: math.NaN(), Lines: 1},
		"inf width":       {Width: math.Inf(1), Height: 1, Lines: 1},
		"inf height":      {Width: 1, Height: math.Inf(-1), Lines: 1},
		"negative width":  {Width: -1, Height: 1, Lines: 1},
		"negative height": {Width: 1, Height: -1, Lines: 1},
		"negative lines":  {Width: 1, Height: 1, Lines: -1},
		"zero width":      {Height: 1, Lines: 1},
		"zero height":     {Width: 1, Lines: 1},
		"zero lines":      {Width: 1, Height: 1},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := (Measurements{Items: map[string]TextMetrics{"bad": tm}}).Metric("bad")
			require.Error(t, err)
			assert.False(t, errors.Is(err, ErrMissingMeasurement))
			assert.Contains(t, err.Error(), "bad")
		})
	}
}

func TestFakeMeasurer_CanceledContext(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{
		Nodes: []workflow.Node{{ID: "a", Type: workflow.NodeTypeScript, Label: "Measure this label"}},
	})
	require.NoError(t, err)
	req := MeasureRequestFor(g, DefaultContract())
	f := newFakeMeasurer()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := f.Measure(ctx, req)

	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, Measurements{}, got, "cancellation must not return successful measurements")
	assert.Zero(t, f.calls, "cancellation must not count as a measurement call")

	got, err = f.Measure(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, 1, f.calls)
	assert.Equal(t, req.Contract, got.Contract)
	assert.True(t, got.FontsReady)
	require.Len(t, got.Items, len(req.Items))
	assert.Positive(t, got.Items[NodeLabelKey("a")].Width, "the live request still produces measurements")
}

func TestFakeMeasurer_HonoursWrapWidthAndStyle(t *testing.T) {
	c := DefaultContract()
	long := strings.Repeat("word ", 30)
	got, err := newFakeMeasurer().Measure(context.Background(), MeasureRequest{Contract: c, Items: []TextItem{
		{Key: "wide", Text: long, Style: StyleNodeLabel, MaxWidth: 240},
		{Key: "narrow", Text: long, Style: StyleNodeLabel, MaxWidth: 120},
		{Key: "small", Text: long, Style: StyleEdgeLabel, MaxWidth: 240},
	}})
	require.NoError(t, err)
	assert.LessOrEqual(t, got.Items["wide"].Width, 240.0)
	assert.Greater(t, got.Items["narrow"].Lines, got.Items["wide"].Lines)
	assert.Less(t, got.Items["small"].Lines, got.Items["wide"].Lines)
}

func TestFakeMeasurer_UnicodeNewlinesAndEmptyText(t *testing.T) {
	c := DefaultContract()
	f := newFakeMeasurer()
	got, err := f.Measure(context.Background(), MeasureRequest{Contract: c, Items: []TextItem{
		{Key: "unicode", Text: strings.Repeat("界", 50), Style: StyleNodeLabel, MaxWidth: 70},
		{Key: "ascii", Text: strings.Repeat("x", 50), Style: StyleNodeLabel, MaxWidth: 70},
		{Key: "newlines", Text: "界\n\n界\n", Style: StyleNodeType, MaxWidth: 70},
		{Key: "empty", Text: "", Style: StyleEdgeLabel, MaxWidth: 70},
	}})
	require.NoError(t, err)
	assert.Equal(t, c, got.Contract)
	assert.True(t, got.FontsReady)
	assert.Equal(t, 1, f.calls)
	assert.Equal(t, TextMetrics{Width: 70, Height: 5 * c.NodeLabelFontPx * c.LineHeight, Lines: 5}, got.Items["unicode"])
	assert.Equal(t, got.Items["ascii"], got.Items["unicode"], "count runes, not bytes")
	assert.Equal(t, TextMetrics{Width: 7 * c.NodeTypeFontPx / 13, Height: 4 * c.NodeTypeFontPx * c.LineHeight, Lines: 4}, got.Items["newlines"])
	assert.Equal(t, TextMetrics{}, got.Items["empty"])
}

func TestFakeMeasurer_RejectsInvalidWrapWidths(t *testing.T) {
	for _, width := range []float64{0, -1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		_, err := newFakeMeasurer().Measure(context.Background(), MeasureRequest{Contract: DefaultContract(), Items: []TextItem{
			{Key: "invalid", Text: "text", Style: StyleNodeLabel, MaxWidth: width},
		}})
		require.Error(t, err)
	}
}

func TestFakeMeasurer_PropagatesFailureAndFontsReadiness(t *testing.T) {
	f := newFakeMeasurer()
	f.err = errors.New("measurement unavailable")
	_, err := f.Measure(context.Background(), MeasureRequest{})
	require.ErrorIs(t, err, f.err)
	f.err, f.fontsReady = nil, false
	got, err := f.Measure(context.Background(), MeasureRequest{Contract: DefaultContract()})
	require.NoError(t, err)
	assert.False(t, got.FontsReady)
	assert.Equal(t, 2, f.calls)
}

func TestMeasureRequestFor_PreservesTextOccurrencesAndCustomWidths(t *testing.T) {
	text := "完整标签\n" + strings.Repeat("界🙂", 70)
	g, err := BuildGraph(&workflow.Workflow{
		Nodes: []workflow.Node{{ID: "a", Label: text}, {ID: "b", Type: workflow.NodeTypeAgentCall}},
		Edges: []workflow.Edge{{Source: "a", Target: "b", Label: text, Output: "hidden"}, {Source: "a", Target: "b", Label: text}},
	})
	require.NoError(t, err)
	c := DefaultContract()
	c.NodeTextMaxWidth, c.EdgeLabelMaxWidth = 123, 89
	before := MeasureRequestFor(g, c)
	require.Len(t, before.Items, 6)
	assert.Equal(t, TextItem{Key: "node:a:label", Text: text, Style: StyleNodeLabel, MaxWidth: 123}, before.Items[0])
	assert.Equal(t, TextItem{Key: "node:a:type", Style: StyleNodeType, MaxWidth: 123}, before.Items[1], "empty caption is still measured as empty")
	assert.Equal(t, "agent call", before.Items[3].Text)
	assert.Equal(t, TextItem{Key: "edge:0", Text: text, Style: StyleEdgeLabel, MaxWidth: 89}, before.Items[4])
	assert.Equal(t, TextItem{Key: "edge:1", Text: text, Style: StyleEdgeLabel, MaxWidth: 89}, before.Items[5])
	assert.Equal(t, before, MeasureRequestFor(g, c), "stable manifest order")
	before.Items[0].Text = "changed request"
	assert.Equal(t, text, g.Nodes[0].Text, "request does not alias graph")
}
