package layout

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"

	"github.com/stretchr/testify/require"
)

func TestGroupProbeNames(t *testing.T) {
	raw, err := os.ReadFile("../../dashboard/src/graph/arranged/groupGeometry.v2.json")
	require.NoError(t, err)
	var fixtures []struct {
		Groups *workflow.GroupGraph `json:"groups"`
	}
	require.NoError(t, json.Unmarshal(raw, &fixtures))
	raw, err = os.ReadFile("../../dashboard/src/graph/arranged/groupProbe.contract.json")
	require.NoError(t, err)
	var names []string
	require.NoError(t, json.Unmarshal(raw, &names))
	require.Equal(t, names, GroupProbeNames(fixtures[3].Groups))
	require.Empty(t, GroupProbeNames(nil))
	groups := &workflow.GroupGraph{Order: []string{"\ue000", "🧪", "中<&\u2028"}, Groups: map[string]*workflow.GroupScope{}}
	for _, id := range groups.Order {
		groups.Groups[id] = &workflow.GroupScope{Config: workflow.GroupConfig{Tasks: []workflow.GroupTask{{Key: "valid"}}}}
	}
	actual := GroupProbeNames(groups)
	require.Equal(t, []string{"[\"group-probe\",\"winner\",\"中<&\u2028\",\"valid\"]", "[\"group-probe\",\"winner\",\"🧪\",\"valid\"]", "[\"group-probe\",\"winner\",\"\ue000\",\"valid\"]"}, actual[len(actual)-3:])
}

func TestRenderReport_GroupWireFields(t *testing.T) {
	raw := `{"groupElements":[{"id":"element","box":{"x":1,"y":2,"w":3,"h":4},"texts":[{"key":"text","text":{"content":"Actual 中","glyphs":[],"visible":false,"box":{"x":1,"y":2,"w":3,"h":4},"clipped":true,"fontPx":13,"contrast":4.5,"occluded":true}}],"visible":false,"borderColor":"rgb(1, 2, 3)","borderPx":2,"backgroundColor":"rgb(4, 5, 6)"}],"groupConnectors":[{"id":"connector","segments":[],"strokeColor":"red","dashPattern":"4 3","visible":false,"arrowPoints":[],"start":{"x":1,"y":2},"end":{"x":3,"y":4},"hasArrow":false,"strokePx":2}],"groupCrossings":[{"point":{"x":2,"y":3},"over":"edge:0","under":"connector","underGapVisible":false,"overContinuous":false}]}`
	var report RenderReport
	require.NoError(t, json.Unmarshal([]byte(raw), &report))
	encoded, err := json.Marshal(report)
	require.NoError(t, err)
	var want, got map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(raw), &want))
	require.NoError(t, json.Unmarshal(encoded, &got))
	for key, value := range want {
		require.Contains(t, got, key)
		require.JSONEq(t, string(value), string(got[key]))
	}
	empty, err := json.Marshal(RenderReport{})
	require.NoError(t, err)
	for _, key := range []string{"groupElements", "groupConnectors", "groupCrossings"} {
		require.NotContains(t, string(empty), `"`+key+`"`)
	}
}
