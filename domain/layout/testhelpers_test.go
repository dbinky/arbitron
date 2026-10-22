package layout

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

// loadSample reads a bundled sample manifest (samples/<name>/workflow.json),
// the spec's acceptance examples (feature-pipeline, pr-review, ralph-loop).
func loadSample(t *testing.T, name string) *workflow.Workflow {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "samples", name, "workflow.json"))
	require.NoError(t, err, "sample %s must exist", name)
	var wf workflow.Workflow
	require.NoError(t, json.Unmarshal(raw, &wf))
	return &wf
}

// edgeGraph builds a Graph from "src>dst" pairs; nodes are declared in first
// appearance order (sources and targets), like loops.test.ts fixtures.
func edgeGraph(t *testing.T, pairs ...[2]string) Graph {
	t.Helper()
	wf := &workflow.Workflow{}
	seen := map[string]bool{}
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			wf.Nodes = append(wf.Nodes, workflow.Node{ID: id, Type: workflow.NodeTypeScript})
		}
	}
	for _, p := range pairs {
		add(p[0])
		add(p[1])
		wf.Edges = append(wf.Edges, workflow.Edge{Source: p[0], Target: p[1]})
	}
	g, err := BuildGraph(wf)
	require.NoError(t, err)
	return g
}

// fakeMeasurer models text, style and wrap width deterministically; production
// measurements must come from the browser renderer instead.
type fakeMeasurer struct {
	charW      float64
	fontsReady bool
	err        error
	calls      int
}

func newFakeMeasurer() *fakeMeasurer { return &fakeMeasurer{charW: 7, fontsReady: true} }

func (f *fakeMeasurer) Measure(ctx context.Context, req MeasureRequest) (Measurements, error) {
	if err := ctx.Err(); err != nil {
		return Measurements{}, err
	}
	f.calls++
	if f.err != nil {
		return Measurements{}, f.err
	}
	out := Measurements{Contract: req.Contract, FontsReady: f.fontsReady, Items: map[string]TextMetrics{}}
	for _, it := range req.Items {
		if !finite(it.MaxWidth) || it.MaxWidth <= 0 {
			return Measurements{}, fmt.Errorf("invalid max width for %s", it.Key)
		}
		out.Items[it.Key] = fakeMetric(it, req.Contract, f.charW)
	}
	return out, nil
}

func fakeMetric(it TextItem, c RendererContract, charW float64) TextMetrics {
	font := c.NodeLabelFontPx
	switch it.Style {
	case StyleNodeType:
		font = c.NodeTypeFontPx
	case StyleEdgeLabel:
		font = c.EdgeLabelFontPx
	}
	if it.Text == "" {
		return TextMetrics{}
	}
	lines, width := 0, 0.0
	for _, paragraph := range strings.Split(it.Text, "\n") {
		natural := float64(utf8.RuneCountInString(paragraph)) * charW * font / 13
		lines += max(1, int(math.Ceil(natural/it.MaxWidth)))
		width = math.Max(width, math.Min(natural, it.MaxWidth))
	}
	return TextMetrics{Width: width, Height: float64(lines) * font * c.LineHeight, Lines: lines}
}
