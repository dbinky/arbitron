package layout

import (
	"fmt"
	"math"
)

// Browsers may round a measured text block to the nearest CSS pixel.
const metricRoundingTolerance = 0.5

// labelSize includes the branch label's padding and rounding slack.
type labelSize struct {
	W, H  float64
	Lines int
}

// nodeSize includes the full wrapped text and attachment reservations.
type nodeSize struct {
	W, H      float64
	TextLines int
}

// ceilPx rounds up with one CSS pixel of slack to preserve browser line breaks.
func ceilPx(v float64) float64 { return math.Ceil(v) + 1 }

// metricForText checks dimensions and lines against the requested text/style.
func metricForText(m Measurements, key, text string, fontPx, lineHeight, maxWidth float64) (TextMetrics, error) {
	tm, err := m.Metric(key)
	if err != nil {
		return TextMetrics{}, err
	}
	if text == "" {
		if tm != (TextMetrics{}) {
			return TextMetrics{}, fmt.Errorf("layout: nonzero metric for empty text %s", key)
		}
		return tm, nil
	}
	if tm.Width <= 0 || tm.Height <= 0 || tm.Lines <= 0 || tm.Width > maxWidth+metricRoundingTolerance ||
		math.Abs(tm.Height-float64(tm.Lines)*fontPx*lineHeight) > metricRoundingTolerance {
		return TextMetrics{}, fmt.Errorf("layout: metric disagrees with requested text/style for %s: %+v", key, tm)
	}
	return tm, nil
}

// edgeLabelSizes sizes each nonempty branch label using its actual measurement.
func edgeLabelSizes(g Graph, m Measurements, c RendererContract, o Options) (map[int]labelSize, error) {
	out := make(map[int]labelSize)
	for _, e := range g.Edges {
		if e.Text == "" {
			continue
		}
		tm, err := metricForText(m, EdgeLabelKey(e.Ordinal), e.Text, c.EdgeLabelFontPx, c.LineHeight, c.EdgeLabelMaxWidth)
		if err != nil {
			return nil, err
		}
		out[e.Ordinal] = labelSize{
			W:     ceilPx(tm.Width) + 2*c.EdgeLabelPaddingX + o.NodeTextSlack,
			H:     ceilPx(tm.Height) + 2*c.EdgeLabelPaddingY + o.NodeTextSlack,
			Lines: tm.Lines,
		}
	}
	return out, nil
}

// nodeSizes grows boxes to fit full text and all top/bottom or right-side ports.
// Self-loop label widths are reserved separately by placement, not in node width.
func nodeSizes(g Graph, kinds []EdgeKind, m Measurements, c RendererContract, o Options, labels map[int]labelSize) ([]nodeSize, error) {
	bottomNeed := make([]float64, len(g.Nodes))
	topCount := make([]int, len(g.Nodes))
	selfCount := make([]int, len(g.Nodes))
	maxSelfLabelH := make([]float64, len(g.Nodes))
	for i := range bottomNeed {
		bottomNeed[i] = o.PortSpacing
	}
	for _, e := range g.Edges {
		s, _ := g.NodeIndex(e.Source)
		if kinds[e.Ordinal] == EdgeSelfLoop {
			selfCount[s]++
			maxSelfLabelH[s] = math.Max(maxSelfLabelH[s], labels[e.Ordinal].H)
			continue
		}
		t, _ := g.NodeIndex(e.Target)
		slot := o.PortSpacing
		if l, ok := labels[e.Ordinal]; ok {
			slot += o.LabelGap + l.W
		}
		bottomNeed[s] += slot
		topCount[t]++
	}

	out := make([]nodeSize, len(g.Nodes))
	for i, n := range g.Nodes {
		label, err := metricForText(m, NodeLabelKey(n.ID), n.Text, c.NodeLabelFontPx, c.LineHeight, c.NodeTextMaxWidth)
		if err != nil {
			return nil, err
		}
		caption, err := metricForText(m, NodeTypeKey(n.ID), n.TypeCaption, c.NodeTypeFontPx, c.LineHeight, c.NodeTextMaxWidth)
		if err != nil {
			return nil, err
		}
		textW := ceilPx(math.Max(label.Width, caption.Width)) + 2*c.NodePaddingX + 2*c.NodeBorderPx + o.NodeTextSlack
		topNeed := float64(topCount[i]+1) * o.PortSpacing
		w := math.Max(math.Max(c.NodeMinWidth, textW), math.Max(bottomNeed[i], topNeed))
		h := 2*c.NodeBorderPx + c.NodeTypeBarPx + 2*c.NodePaddingY + ceilPx(label.Height) + o.NodeTextSlack
		if n.TypeCaption != "" {
			h += ceilPx(caption.Height) + c.NodeTextGapPx
		}
		if selfCount[i] > 0 {
			step := math.Max(o.PortSpacing, maxSelfLabelH[i]+2*o.LabelGap+c.MinClearancePx)
			h = math.Max(h, float64(2*selfCount[i]+1)*step)
		}
		out[i] = nodeSize{W: math.Ceil(w), H: math.Ceil(h), TextLines: max(1, label.Lines)}
	}
	if err := reserveGroupHeaderText(g, m, c, o, out); err != nil {
		return nil, err
	}
	return out, nil
}
