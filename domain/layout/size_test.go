package layout

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func measured(t *testing.T, g Graph) Measurements {
	t.Helper()
	m, err := newFakeMeasurer().Measure(context.Background(), MeasureRequestFor(g, DefaultContract()))
	require.NoError(t, err)
	return m
}

func sizesFor(t *testing.T, g Graph, o Options) ([]nodeSize, map[int]labelSize) {
	t.Helper()
	c := DefaultContract()
	m := measured(t, g)
	labels, err := edgeLabelSizes(g, m, c, o)
	require.NoError(t, err)
	sizes, err := nodeSizes(g, classifyEdges(g), m, c, o, labels)
	require.NoError(t, err)
	return sizes, labels
}

func TestDefaultOptions(t *testing.T) {
	o := DefaultOptions()
	assert.Equal(t, Options{
		RankSpacing: 48, NodeSpacing: 56, LaneSeparation: 14, GutterWidth: 48, LabelGap: 10,
		PortSpacing: 24, NodeTextSlack: 0, DummyWidth: 24, CrossingSweeps: 8, Margin: 40,
	}, o)
	assert.NoError(t, validateGeometryOptions(o))
}

func TestSizing_CeilPx(t *testing.T) {
	assert.Equal(t, 1.0, ceilPx(0))
	assert.Equal(t, 11.0, ceilPx(10))
	assert.Equal(t, 12.0, ceilPx(10.01))
}

func TestNodeSizes_ShortLabelUsesMinimumWidth(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{Nodes: []workflow.Node{{ID: "a", Type: workflow.NodeTypeScript, Label: "Init"}}})
	require.NoError(t, err)
	sizes, _ := sizesFor(t, g, DefaultOptions())
	c := DefaultContract()
	assert.Equal(t, c.NodeMinWidth, sizes[0].W)
	assert.Equal(t, 1, sizes[0].TextLines)
	typeH := math.Ceil(c.NodeTypeFontPx*c.LineHeight) + 1
	labelH := math.Ceil(c.NodeLabelFontPx*c.LineHeight) + 1
	want := 2*c.NodeBorderPx + c.NodeTypeBarPx + 2*c.NodePaddingY + typeH + c.NodeTextGapPx + labelH
	assert.Equal(t, want, sizes[0].H)
}

func TestNodeSizes_LongLabelWrapsAndGrowsTallerNotWider(t *testing.T) {
	long := strings.Repeat("Resolve blockers then re-align ", 4)
	g, err := BuildGraph(&workflow.Workflow{Nodes: []workflow.Node{
		{ID: "short", Type: workflow.NodeTypeAgentCall, Label: "Plan"},
		{ID: "long", Type: workflow.NodeTypeAgentCall, Label: long},
	}})
	require.NoError(t, err)
	sizes, _ := sizesFor(t, g, DefaultOptions())
	c := DefaultContract()
	assert.Greater(t, sizes[1].TextLines, 1)
	assert.Greater(t, sizes[1].H, sizes[0].H)
	maxW := math.Ceil(c.NodeTextMaxWidth) + 1 + 2*c.NodePaddingX + 2*c.NodeBorderPx
	assert.LessOrEqual(t, sizes[1].W, maxW)
}

func TestNodeSizes_LabelledBranchesWidenTheSource(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{
		Nodes: []workflow.Node{{ID: "gate", Type: workflow.NodeTypeConditional}, {ID: "x"}, {ID: "y"}, {ID: "z"}},
		Edges: []workflow.Edge{
			{Source: "gate", Target: "x", Label: "plans exist — re-align + revalidate"},
			{Source: "gate", Target: "y", Label: "spec+designs exist"},
			{Source: "gate", Target: "z", Label: "fresh run"},
		},
	})
	require.NoError(t, err)
	o := DefaultOptions()
	sizes, labels := sizesFor(t, g, o)
	m, c := measured(t, g), DefaultContract()
	need := o.PortSpacing
	for ord := range 3 {
		textW := m.Items[EdgeLabelKey(ord)].Width
		labelW := math.Ceil(textW) + 1 + 2*c.EdgeLabelPaddingX
		assert.Equal(t, labelW, labels[ord].W)
		need += o.PortSpacing + o.LabelGap + labelW
	}
	assert.Equal(t, math.Ceil(need), sizes[0].W, "every measured branch label fits beside its own port")
	assert.Greater(t, sizes[0].W, DefaultContract().NodeMinWidth)
}

func TestNodeSizes_ManyIncomingEdgesWidenTheTarget(t *testing.T) {
	wf := &workflow.Workflow{Nodes: []workflow.Node{{ID: "join"}}}
	for i := range 12 {
		id := string(rune('a' + i))
		wf.Nodes = append(wf.Nodes, workflow.Node{ID: id})
		wf.Edges = append(wf.Edges, workflow.Edge{Source: id, Target: "join"})
	}
	g, err := BuildGraph(wf)
	require.NoError(t, err)
	o := DefaultOptions()
	sizes, _ := sizesFor(t, g, o)
	assert.Equal(t, 13*o.PortSpacing, sizes[0].W)
	o.PortSpacing *= 2
	wider, _ := sizesFor(t, g, o)
	assert.Equal(t, 2*sizes[0].W, wider[0].W)
}

func TestNodeSizes_TextSlackGrowsBoxes(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{
		Nodes: []workflow.Node{{ID: "a", Label: strings.Repeat("界", 60)}, {ID: "b"}},
		Edges: []workflow.Edge{{Source: "a", Target: "b", Label: "branch"}},
	})
	require.NoError(t, err)
	base, baseLabels := sizesFor(t, g, DefaultOptions())
	o := DefaultOptions()
	o.NodeTextSlack = 8
	slack, slackLabels := sizesFor(t, g, o)
	assert.Equal(t, base[0].H+8, slack[0].H)
	assert.Equal(t, base[0].W+8, slack[0].W)
	assert.Equal(t, baseLabels[0].W+8, slackLabels[0].W)
	assert.Equal(t, baseLabels[0].H+8, slackLabels[0].H)
}

func TestEdgeLabelSizes_IncludePadding(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{
		Nodes: []workflow.Node{{ID: "a"}, {ID: "b"}},
		Edges: []workflow.Edge{{Source: "a", Target: "b", Output: "pass"}, {Source: "a", Target: "b"}},
	})
	require.NoError(t, err)
	c, o := DefaultContract(), DefaultOptions()
	m := measured(t, g)
	labels, err := edgeLabelSizes(g, m, c, o)
	require.NoError(t, err)
	require.Contains(t, labels, 0)
	assert.NotContains(t, labels, 1)
	tm := m.Items[EdgeLabelKey(0)]
	assert.Equal(t, math.Ceil(tm.Width)+1+2*c.EdgeLabelPaddingX, labels[0].W)
	assert.Equal(t, math.Ceil(tm.Height)+1+2*c.EdgeLabelPaddingY, labels[0].H)
	assert.Equal(t, tm.Lines, labels[0].Lines)
}

func TestSizing_MissingMeasurementIsAnError(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{
		Nodes: []workflow.Node{{ID: "a"}, {ID: "b"}},
		Edges: []workflow.Edge{{Source: "a", Target: "b", Output: "pass"}},
	})
	require.NoError(t, err)
	c, o := DefaultContract(), DefaultOptions()
	for _, key := range []string{EdgeLabelKey(0), NodeLabelKey("b"), NodeTypeKey("b")} {
		t.Run(key, func(t *testing.T) {
			m := measured(t, g)
			delete(m.Items, key)
			if key == EdgeLabelKey(0) {
				_, err = edgeLabelSizes(g, m, c, o)
			} else {
				_, err = nodeSizes(g, classifyEdges(g), m, c, o, map[int]labelSize{0: {W: 30, H: 20, Lines: 1}})
			}
			assert.ErrorIs(t, err, ErrMissingMeasurement)
		})
	}
	_, err = nodeSizes(g, classifyEdges(g), Measurements{}, c, o, nil)
	assert.ErrorIs(t, err, ErrMissingMeasurement, "nil input cannot size real graph text")
}

func TestSizing_RejectsMetricsDisagreeingWithTextAndStyle(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{
		Nodes: []workflow.Node{{ID: "a", Type: workflow.NodeTypeScript}, {ID: "b"}},
		Edges: []workflow.Edge{{Source: "a", Target: "b", Label: "branch"}},
	})
	require.NoError(t, err)
	c, o := DefaultContract(), DefaultOptions()
	for _, key := range []string{NodeLabelKey("a"), NodeTypeKey("a"), EdgeLabelKey(0)} {
		for _, invalid := range []string{"zero", "too wide", "wrong height", "wrong lines"} {
			t.Run(key+"/"+invalid, func(t *testing.T) {
				m := measured(t, g)
				tm := m.Items[key]
				switch invalid {
				case "zero":
					tm = TextMetrics{}
				case "too wide":
					tm.Width = c.NodeTextMaxWidth + c.EdgeLabelMaxWidth
				case "wrong height":
					tm.Height += 2
				case "wrong lines":
					tm.Lines++
				}
				m.Items[key] = tm
				if key == EdgeLabelKey(0) {
					_, err = edgeLabelSizes(g, m, c, o)
				} else {
					_, err = nodeSizes(g, classifyEdges(g), m, c, o, nil)
				}
				require.Error(t, err)
				assert.Contains(t, err.Error(), key)
			})
		}
	}
}

func TestNodeSizes_EmptyCaptionReservesNoBlockOrGap(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{Nodes: []workflow.Node{{ID: "a", Label: "Init"}}})
	require.NoError(t, err)
	c, o := DefaultContract(), DefaultOptions()
	m := measured(t, g)
	sizes, err := nodeSizes(g, classifyEdges(g), m, c, o, nil)
	require.NoError(t, err)
	require.Len(t, sizes, 1)
	assert.Equal(t, 2*c.NodeBorderPx+c.NodeTypeBarPx+2*c.NodePaddingY+math.Ceil(m.Items[NodeLabelKey("a")].Height)+1, sizes[0].H)
	m.Items[NodeTypeKey("a")] = TextMetrics{Width: 12, Height: c.NodeTypeFontPx * c.LineHeight, Lines: 1}
	_, err = nodeSizes(g, classifyEdges(g), m, c, o, nil)
	require.Error(t, err, "a nonempty measurement cannot silently manufacture a caption")
}

func TestSizing_UnicodeNewlinesAndMeasurementChangesReachBoxes(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{
		Nodes: []workflow.Node{{ID: "short", Label: "Init", Type: workflow.NodeTypeScript}, {ID: "tall", Label: "审批\n" + strings.Repeat("界🙂", 80), Type: workflow.NodeTypeScript}},
		Edges: []workflow.Edge{{Source: "short", Target: "tall", Label: "重试\n" + strings.Repeat("界", 70)}},
	})
	require.NoError(t, err)
	c, o := DefaultContract(), DefaultOptions()
	f := newFakeMeasurer()
	sizeWith := func() ([]nodeSize, map[int]labelSize) {
		m, err := f.Measure(context.Background(), MeasureRequestFor(g, c))
		require.NoError(t, err)
		labels, err := edgeLabelSizes(g, m, c, o)
		require.NoError(t, err)
		sizes, err := nodeSizes(g, classifyEdges(g), m, c, o, labels)
		require.NoError(t, err)
		return sizes, labels
	}
	base, labels := sizeWith()
	require.Len(t, base, 2)
	assert.Equal(t, 6, base[1].TextLines, "explicit newline plus five wrapped Unicode lines")
	assert.Greater(t, base[1].H, base[0].H)
	assert.Equal(t, 4, labels[0].Lines)
	assert.Equal(t, math.Ceil(c.NodeTextMaxWidth)+1+2*c.NodePaddingX+2*c.NodeBorderPx, base[1].W)
	f.charW *= 2
	larger, largerLabels := sizeWith()
	assert.Greater(t, larger[1].H, base[1].H, "real measurements, not constant text sizes, feed boxes")
	assert.Greater(t, largerLabels[0].H, labels[0].H)
	c.NodeTextMaxWidth, c.EdgeLabelMaxWidth = 120, 80
	narrower, narrowerLabels := sizeWith()
	assert.Greater(t, narrower[1].H, larger[1].H)
	assert.Greater(t, narrowerLabels[0].H, largerLabels[0].H)
	assert.Less(t, narrower[1].W, larger[1].W)
}

func TestNodeSizes_LoopReturnPortsCount(t *testing.T) {
	wf := &workflow.Workflow{Nodes: []workflow.Node{{ID: "a"}, {ID: "b"}}, Edges: []workflow.Edge{{Source: "a", Target: "b"}}}
	for range 12 {
		wf.Edges = append(wf.Edges, workflow.Edge{Source: "b", Target: "a"})
	}
	g, err := BuildGraph(wf)
	require.NoError(t, err)
	o := DefaultOptions()
	sizes, _ := sizesFor(t, g, o)
	assert.Equal(t, 13*o.PortSpacing, sizes[0].W, "loop returns need top ports")
	assert.Equal(t, 13*o.PortSpacing, sizes[1].W, "loop returns need bottom ports")
}

func TestNodeSizes_SelfLoopAttachmentHeight(t *testing.T) {
	for _, count := range []int{2, 12} {
		for _, text := range []string{"", "重试\n" + strings.Repeat("界🙂", 60)} {
			wf := &workflow.Workflow{Nodes: []workflow.Node{{ID: "a", Type: workflow.NodeTypeScript}, {ID: "tall", Label: strings.Repeat("界", 200)}}}
			for range count {
				wf.Edges = append(wf.Edges, workflow.Edge{Source: "a", Target: "a", Label: text})
			}
			g, err := BuildGraph(wf)
			require.NoError(t, err)
			o, c := DefaultOptions(), DefaultContract()
			sizes, labels := sizesFor(t, g, o)
			assert.Equal(t, c.NodeMinWidth, sizes[0].W, "self loops have right-side ports, not top/bottom")
			labelH := 0.0
			if text != "" {
				tm := measured(t, g).Items[EdgeLabelKey(0)]
				labelH = math.Ceil(tm.Height) + 1 + 2*c.EdgeLabelPaddingY
				assert.Equal(t, labelH, labels[0].H)
			}
			step := math.Max(o.PortSpacing, labelH+2*o.LabelGap+c.MinClearancePx)
			assert.GreaterOrEqual(t, sizes[0].H, float64(2*count+1)*step)
			assert.GreaterOrEqual(t, sizes[0].H/float64(2*count+1), o.PortSpacing, "all fractional right-side ports clear each other and corners")
			if text != "" {
				require.Len(t, labels, count, "identical labelled occurrences must not collapse")
				o.LabelGap += 10
			} else {
				o.PortSpacing *= 2
			}
			bigger, _ := sizesFor(t, g, o)
			assert.Greater(t, bigger[0].H, sizes[0].H, "spacing must feed attachment reservation")
		}
	}
}

func TestSizing_MeasurementRoundingTolerance(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{Nodes: []workflow.Node{{ID: "a", Type: workflow.NodeTypeScript}}})
	require.NoError(t, err)
	c, o := DefaultContract(), DefaultOptions()
	for _, delta := range []float64{-0.49, 0.49, -0.51, 0.51} {
		m := measured(t, g)
		tm := m.Items[NodeLabelKey("a")]
		tm.Height += delta
		m.Items[NodeLabelKey("a")] = tm
		_, err := nodeSizes(g, classifyEdges(g), m, c, o, nil)
		if math.Abs(delta) < 0.5 {
			assert.NoError(t, err)
		} else {
			assert.Error(t, err)
		}
	}
}

func TestNodeSizes_TypeCaptionAndContractFeedBoxModel(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{Nodes: []workflow.Node{{ID: "a", Label: "Hi", Type: workflow.NodeType(strings.Repeat("界", 80))}}})
	require.NoError(t, err)
	c, o := DefaultContract(), DefaultOptions()
	c.NodeTypeFontPx, c.NodeLabelFontPx, c.LineHeight = 14, 15, 1.5
	c.NodePaddingX, c.NodePaddingY, c.NodeBorderPx, c.NodeTypeBarPx, c.NodeTextGapPx = 17, 11, 3, 6, 5
	m, err := newFakeMeasurer().Measure(context.Background(), MeasureRequestFor(g, c))
	require.NoError(t, err)
	sizes, err := nodeSizes(g, classifyEdges(g), m, c, o, nil)
	require.NoError(t, err)
	caption, primary := m.Items[NodeTypeKey("a")], m.Items[NodeLabelKey("a")]
	require.Greater(t, caption.Lines, 1)
	assert.Equal(t, math.Ceil(caption.Width)+1+2*c.NodePaddingX+2*c.NodeBorderPx, sizes[0].W)
	assert.Equal(t, 2*c.NodeBorderPx+c.NodeTypeBarPx+2*c.NodePaddingY+math.Ceil(caption.Height)+1+c.NodeTextGapPx+math.Ceil(primary.Height)+1, sizes[0].H)
	assert.Equal(t, 1, sizes[0].TextLines, "caption line count must not replace primary line count")
}

func TestSizing_ErrorDoesNotMutateInputs(t *testing.T) {
	wf := &workflow.Workflow{Nodes: []workflow.Node{{ID: "a", Type: workflow.NodeTypeScript}}, Edges: []workflow.Edge{{Source: "a", Target: "a", Label: "retry"}}}
	g, err := BuildGraph(wf)
	require.NoError(t, err)
	before, err := BuildGraph(wf)
	require.NoError(t, err)
	c, o := DefaultContract(), DefaultOptions()
	m := measured(t, g)
	delete(m.Items, EdgeLabelKey(0))
	snapshot := measured(t, g)
	delete(snapshot.Items, EdgeLabelKey(0))
	_, err = edgeLabelSizes(g, m, c, o)
	assert.ErrorIs(t, err, ErrMissingMeasurement, "missing self-loop text cannot be ignored")
	assert.Equal(t, before, g)
	assert.Equal(t, snapshot, m)
	m = measured(t, g)
	m.Items[NodeLabelKey("a")] = TextMetrics{}
	snapshot = measured(t, g)
	snapshot.Items[NodeLabelKey("a")] = TextMetrics{}
	labels := map[int]labelSize{0: {W: 30, H: 20, Lines: 1}}
	_, err = nodeSizes(g, classifyEdges(g), m, c, o, labels)
	require.Error(t, err)
	assert.Equal(t, before, g)
	assert.Equal(t, snapshot, m)
	assert.Equal(t, map[int]labelSize{0: {W: 30, H: 20, Lines: 1}}, labels)
}
