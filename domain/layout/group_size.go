package layout

import (
	"fmt"
	"math"
)

// reserveGroupHeaderText grows the real group header's node size to include
// its supplemental measured blocks after the original label and type caption.
// Port/self-loop reservations from ordinary node sizing remain intact.
func reserveGroupHeaderText(g Graph, m Measurements, c RendererContract, o Options, sizes []nodeSize) error {
	if g.Groups == nil {
		return nil
	}
	for _, id := range g.Groups.Order {
		i, ok := g.NodeIndex(id)
		if !ok {
			return fmt.Errorf("%w: group header %q has no node index", ErrInvalidGroups, id)
		}
		scope := g.Groups.Groups[id]
		supplement, err := groupHeaderTextSize(groupHeaderTextItems(id, len(scope.Config.Tasks), scope.Config.Form, c), m, c)
		if err != nil {
			return err
		}
		sizes[i].W = math.Max(sizes[i].W, math.Ceil(supplement.W+2*c.NodePaddingX+2*c.NodeBorderPx+o.NodeTextSlack))
		sizes[i].H = math.Ceil(sizes[i].H + supplement.H)
	}
	return nil
}

// Each supplement follows an existing text block, hence one preceding gap.
func groupHeaderTextSize(items []TextItem, m Measurements, c RendererContract) (labelSize, error) {
	var size labelSize
	for _, item := range items {
		fontPx := c.NodeTypeFontPx
		if item.Style == StyleEdgeLabel {
			fontPx = c.EdgeLabelFontPx
		}
		tm, err := metricForText(m, item.Key, item.Text, fontPx, c.LineHeight, item.MaxWidth)
		if err != nil {
			return labelSize{}, err
		}
		size.W = math.Max(size.W, ceilPx(tm.Width))
		size.H += ceilPx(tm.Height) + c.NodeTextGapPx
	}
	return size, nil
}

func checkGroupHeaderTextFits(g Graph, geo Geometry, m Measurements, add addFinding) {
	if g.Groups == nil {
		return
	}
	c := geo.Contract
	for _, n := range geo.Nodes {
		scope, ok := g.Groups.Groups[n.ID]
		if !ok {
			continue
		}
		supplement, err := groupHeaderTextSize(groupHeaderTextItems(n.ID, len(scope.Config.Tasks), scope.Config.Form, c), m, c)
		if err != nil {
			add(CriterionCoverage, SeverityBlocking, []string{n.ID}, nil, "%v", err)
			continue
		}
		label, err1 := metricForText(m, NodeLabelKey(n.ID), n.Text, c.NodeLabelFontPx, c.LineHeight, c.NodeTextMaxWidth)
		caption, err2 := metricForText(m, NodeTypeKey(n.ID), n.TypeCaption, c.NodeTypeFontPx, c.LineHeight, c.NodeTextMaxWidth)
		if err1 != nil || err2 != nil {
			continue // checkTextFits reports primary label/type measurement failures.
		}
		innerW := n.Box.W - 2*c.NodePaddingX - 2*c.NodeBorderPx
		innerH := n.Box.H - 2*c.NodeBorderPx - c.NodeTypeBarPx - 2*c.NodePaddingY - c.NodeTextGapPx
		if innerW+geomEps < supplement.W || innerH+geomEps < math.Ceil(label.Height)+math.Ceil(caption.Height)+supplement.H {
			add(CriterionTextClipped, SeverityBlocking, []string{n.ID}, nil, "header %q cannot fit its complete measured label/type and supplements", n.ID)
		}
	}
}
