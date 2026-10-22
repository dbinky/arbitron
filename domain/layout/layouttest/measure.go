// Package layouttest provides argument-honoring unit support, never production evidence.
package layouttest

import (
	"math"
	"strings"
	"unicode/utf8"

	"github.com/arbitron/arbitron/domain/layout"
)

func Measurements(g layout.Graph, c layout.RendererContract) layout.Measurements {
	return MeasureItems(c, layout.MeasureRequestFor(g, c).Items)
}

// MeasureItems models explicit newlines and wrap width with deterministic rune metrics.
// Invalid wrap widths produce no metric, allowing the real sizing stage to reject them.
func MeasureItems(c layout.RendererContract, items []layout.TextItem) layout.Measurements {
	m := layout.Measurements{Contract: c, FontsReady: true, Items: map[string]layout.TextMetrics{}}
	for _, it := range items {
		if it.MaxWidth <= 0 || math.IsNaN(it.MaxWidth) || math.IsInf(it.MaxWidth, 0) {
			continue
		}
		font := c.NodeLabelFontPx
		switch it.Style {
		case layout.StyleNodeType:
			font = c.NodeTypeFontPx
		case layout.StyleEdgeLabel:
			font = c.EdgeLabelFontPx
		}
		tm := layout.TextMetrics{}
		if it.Text != "" {
			for _, p := range strings.Split(it.Text, "\n") {
				natural := float64(utf8.RuneCountInString(p)) * 7 * font / 13
				tm.Lines += max(1, int(math.Ceil(natural/it.MaxWidth)))
				tm.Width = math.Max(tm.Width, math.Min(natural, it.MaxWidth))
			}
			tm.Height = float64(tm.Lines) * font * c.LineHeight
		}
		m.Items[it.Key] = tm
	}
	return m
}
