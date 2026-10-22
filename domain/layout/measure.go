package layout

import (
	"errors"
	"fmt"
)

// TextStyle names the renderer CSS style used for measurement.
type TextStyle string

const (
	StyleNodeLabel TextStyle = "node_label"
	StyleNodeType  TextStyle = "node_type"
	StyleEdgeLabel TextStyle = "edge_label"
)

// TextItem requests full text wrapped at MaxWidth CSS px at zoom 1.
type TextItem struct {
	Key      string    `json:"key"`
	Text     string    `json:"text"`
	Style    TextStyle `json:"style"`
	MaxWidth float64   `json:"maxWidth"`
}

// MeasureRequest lists every displayed text item with the renderer contract.
type MeasureRequest struct {
	Contract RendererContract `json:"contract"`
	Items    []TextItem       `json:"items"`
}

// TextMetrics describes the widest line and full block height in CSS px.
type TextMetrics struct {
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	Lines  int     `json:"lines"`
}

// Measurements records the contract and font readiness actually used to measure.
type Measurements struct {
	Contract   RendererContract       `json:"contract"`
	FontsReady bool                   `json:"fontsReady"`
	Items      map[string]TextMetrics `json:"items"`
}

// ErrMissingMeasurement means a required text item was not measured.
var ErrMissingMeasurement = errors.New("layout: missing text measurement")

// NodeLabelKey identifies a node's primary text measurement.
func NodeLabelKey(id string) string { return "node:" + id + ":label" }

// NodeTypeKey identifies a node's type caption measurement.
func NodeTypeKey(id string) string { return "node:" + id + ":type" }

// EdgeLabelKey identifies an edge occurrence's branch text measurement.
func EdgeLabelKey(ordinal int) string { return fmt.Sprintf("edge:%d", ordinal) }

// MeasureRequestFor lists node labels, type captions and nonempty branch labels
// in manifest order, preserving their full text and the contract's wrap widths.
func MeasureRequestFor(g Graph, c RendererContract) MeasureRequest {
	items := make([]TextItem, 0, 2*len(g.Nodes)+len(g.Edges))
	for _, n := range g.Nodes {
		items = append(items,
			TextItem{Key: NodeLabelKey(n.ID), Text: n.Text, Style: StyleNodeLabel, MaxWidth: c.NodeTextMaxWidth},
			TextItem{Key: NodeTypeKey(n.ID), Text: n.TypeCaption, Style: StyleNodeType, MaxWidth: c.NodeTextMaxWidth},
		)
	}
	for _, e := range g.Edges {
		if e.Text != "" {
			items = append(items, TextItem{Key: EdgeLabelKey(e.Ordinal), Text: e.Text, Style: StyleEdgeLabel, MaxWidth: c.EdgeLabelMaxWidth})
		}
	}
	items = append(items, groupTextItems(g, c)...)
	return MeasureRequest{Contract: c, Items: items}
}

// Metric rejects missing, nonfinite, negative and partially zero measurements.
// All-zero is a valid value for empty text; sizing checks it against Graph text.
func (m Measurements) Metric(key string) (TextMetrics, error) {
	tm, ok := m.Items[key]
	if !ok {
		return TextMetrics{}, fmt.Errorf("%w: %s", ErrMissingMeasurement, key)
	}
	if !finite(tm.Width) || !finite(tm.Height) || tm.Width < 0 || tm.Height < 0 || tm.Lines < 0 {
		return TextMetrics{}, fmt.Errorf("layout: invalid measurement for %s: %+v", key, tm)
	}
	if (tm.Width == 0 || tm.Height == 0 || tm.Lines == 0) && tm != (TextMetrics{}) {
		return TextMetrics{}, fmt.Errorf("layout: inconsistent measurement for %s: %+v", key, tm)
	}
	return tm, nil
}
