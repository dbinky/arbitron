package layout_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func unitReview(geo layout.Geometry) *layout.Review {
	return &layout.Review{Rendered: true, Evidence: layout.Evidence{GeometryDigest: layout.GeometryDigest(geo), RendererVersion: layout.ContractVersion, Contract: geo.Contract, FontsReady: true, Zoom: geo.Contract.PracticalZoom, Tiles: 1, Artifacts: layouttest.Artifacts(geo), ReviewedAt: time.Now().UTC()}}
}
func TestStoreModels(t *testing.T) {
	owner := uuid.New()
	assert.Equal(t, "shared/demo", layout.LocatorKey(workflow.Scope{Kind: workflow.ScopeShared, OwnerID: owner}, "demo"))
	assert.Equal(t, "private/"+uuid.Nil.String()+"/demo", layout.LocatorKey(workflow.Private(uuid.Nil), "demo"))
	assert.Equal(t, layout.Destination{Scope: workflow.Shared(), Name: "demo"}.Key(), layout.Destination{Scope: workflow.Shared(), Name: "demo", WorkflowID: owner}.Key())
	for _, s := range []layout.State{layout.StatePassed, layout.StateNeedsAttention, layout.StateFailed, layout.StateSuperseded} {
		assert.True(t, s.Terminal())
	}
	for _, s := range []layout.State{layout.StatePending, layout.StateInProgress, "unknown"} {
		assert.False(t, s.Terminal())
	}
	geo := layouttest.Arranged(t, layouttest.SampleWorkflow())
	result := layout.StoredResult{Geometry: geo, Review: unitReview(geo), Preflight: []layout.Finding{{NodeIDs: []string{"start"}, Evidence: "preflight"}}}
	assert.False(t, result.IsUnreviewed())
	findings := result.UnresolvedFindings()
	require.Len(t, findings, 1)
	findings[0].NodeIDs[0] = "mutated"
	assert.Equal(t, "start", result.Preflight[0].NodeIDs[0])
	assert.NotNil(t, (layout.StoredResult{}).UnresolvedFindings())
	result.Review.Findings = []layout.Finding{{EdgeOrdinals: []int{2}, Evidence: "review"}}
	combined := result.UnresolvedFindings()
	require.Len(t, combined, 2)
	assert.Equal(t, "preflight", combined[0].Evidence)
	assert.Equal(t, "review", combined[1].Evidence)
	combined[1].EdgeOrdinals[0] = 99
	assert.Equal(t, 2, result.Review.Findings[0].EdgeOrdinals[0])
	for _, mutate := range []func(*layout.Review){func(r *layout.Review) { r.Evidence.GeometryDigest = "wrong" }, func(r *layout.Review) { r.Evidence.Artifacts = nil }, func(r *layout.Review) { r.Evidence.Contract.NodePaddingX++ }, func(r *layout.Review) { r.Evidence.FontsReady = false }, func(r *layout.Review) { r.Evidence.Zoom = 0.1 }, func(r *layout.Review) { r.Evidence.RendererVersion = "old" }} {
		r := result
		r.Review = unitReview(geo)
		mutate(r.Review)
		assert.True(t, r.IsUnreviewed())
	}
	assert.True(t, (layout.StoredResult{Geometry: geo}).IsUnreviewed())
	raw, err := json.Marshal(layout.CacheEntry{})
	require.NoError(t, err)
	var obj map[string]any
	require.NoError(t, json.Unmarshal(raw, &obj))
	assert.Len(t, obj, 7)
	for _, key := range []string{"format", "revision", "generation", "jobGeneration", "contract", "verdict", "geometry"} {
		assert.Contains(t, obj, key)
	}
}
func TestLayoutTestProducers(t *testing.T) {
	wf := layouttest.SampleWorkflow()
	require.Len(t, wf.Nodes, 4)
	require.Len(t, wf.Edges, 4)
	var decoded workflow.Workflow
	require.NoError(t, json.Unmarshal(layouttest.ManifestJSON(t, wf), &decoded))
	assert.Equal(t, *wf, decoded)
	g, err := layout.BuildGraph(wf)
	require.NoError(t, err)
	geo := layouttest.Arranged(t, wf)
	require.NoError(t, geo.Validate(g))
	c := layout.DefaultContract()
	items := []layout.TextItem{{Key: "wide", Text: strings.Repeat("字🙂", 70) + "\nend", Style: layout.StyleNodeLabel, MaxWidth: 240}, {Key: "narrow", Text: strings.Repeat("字🙂", 70) + "\nend", Style: layout.StyleNodeLabel, MaxWidth: 120}, {Key: "small", Text: strings.Repeat("字🙂", 70) + "\nend", Style: layout.StyleEdgeLabel, MaxWidth: 240}, {Key: "empty", MaxWidth: 240}, {Key: "invalid", Text: "x", MaxWidth: 0}}
	m := layouttest.MeasureItems(c, items)
	require.True(t, m.FontsReady)
	assert.Equal(t, c, m.Contract)
	require.Contains(t, m.Items, "wide")
	assert.Greater(t, m.Items["wide"].Lines, 1)
	assert.Greater(t, m.Items["narrow"].Lines, m.Items["wide"].Lines)
	assert.Less(t, m.Items["small"].Height, m.Items["wide"].Height)
	assert.Equal(t, layout.TextMetrics{}, m.Items["empty"])
	assert.NotContains(t, m.Items, "invalid")
	c.NodeLabelFontPx *= 2
	bigger := layouttest.MeasureItems(c, items)
	assert.Greater(t, bigger.Items["wide"].Lines, m.Items["wide"].Lines)
	wf.Nodes[0].Label = strings.Repeat("Unicode字🙂", 30)
	changed := layouttest.Arranged(t, wf)
	assert.NotEqual(t, geo.Nodes[0].Box, changed.Nodes[0].Box)
	assert.True(t, (layout.StoredResult{Geometry: changed, Review: unitReview(geo)}).IsUnreviewed())
}
