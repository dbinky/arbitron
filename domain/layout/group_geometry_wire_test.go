package layout

import (
	"encoding/json"
	"fmt"
	"github.com/arbitron/arbitron/domain/workflow"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGroupGeometry_FieldParity(t *testing.T) {
	raw, err := os.ReadFile("../../dashboard/src/graph/arranged/groupGeometry.fields.json")
	require.NoError(t, err)
	var want map[string]wireKeys
	require.NoError(t, json.Unmarshal(raw, &want))
	got := map[string]wireKeys{
		"groupText": jsonKeys(t, GroupText{}), "groupElement": jsonKeys(t, GroupElement{}),
		"groupLane": jsonKeys(t, GroupLane{}), "groupConnector": jsonKeys(t, GroupConnector{}),
		"groupCrossing": jsonKeys(t, GroupCrossing{}), "attachment": jsonKeys(t, Attachment{}),
		"geometry": jsonKeys(t, Geometry{}),
	}
	require.Equal(t, want, got)
}

func TestGroupGeometry_WireRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		form   string
		nested bool
	}{{"branches", false}, {"workflows", false}, {"branches", true}} {
		t.Run(tc.form+fmt.Sprint(tc.nested), func(t *testing.T) {
			g, err := BuildGraph(capturedGroupWorkflow(tc.form, tc.nested))
			require.NoError(t, err)
			geo := arrangeFor(t, g, DefaultOptions())
			require.Equal(t, "arbitron.layout/v2", geo.Format)
			require.Len(t, geo.GroupLanes, 2*len(g.Groups.Order))
			require.GreaterOrEqual(t, len(geo.GroupElements), 5*len(g.Groups.Order))
			require.NotEmpty(t, geo.GroupConnectors)
			for _, e := range geo.Edges {
				require.NotEmpty(t, e.SourceAttach.ElementID)
				require.NotEmpty(t, e.TargetAttach.ElementID)
			}
			raw, err := json.Marshal(geo)
			require.NoError(t, err)
			var decoded Geometry
			require.NoError(t, json.Unmarshal(raw, &decoded))
			require.Equal(t, geo, decoded)
			require.NoError(t, decoded.Validate(g))
			require.True(t, Compatible(decoded, g))
			for i := range geo.GroupElements {
				t.Run(fmt.Sprint(i), func(t *testing.T) {
					var bad Geometry
					require.NoError(t, json.Unmarshal(raw, &bad))
					bad.GroupElements = append(bad.GroupElements[:i], bad.GroupElements[i+1:]...)
					require.ErrorIs(t, bad.Validate(g), ErrInvalidGeometry)
				})
			}
		})
	}
}

func TestGroupGeometry_SharedV2Fixtures(t *testing.T) {
	raw, err := os.ReadFile("../../dashboard/src/graph/arranged/groupGeometry.v2.json")
	require.NoError(t, err)
	var fixtures []struct {
		Definition workflow.Workflow    `json:"definition"`
		Groups     *workflow.GroupGraph `json:"groups"`
		Geometry   Geometry             `json:"geometry"`
	}
	require.NoError(t, json.Unmarshal(raw, &fixtures))
	require.Len(t, fixtures, 5)
	for _, f := range fixtures {
		g, err := BuildGraph(&f.Definition)
		require.NoError(t, err)
		require.Equal(t, g.Groups, f.Groups)
		require.NoError(t, f.Geometry.Validate(g))
		require.Equal(t, DefaultContract(), f.Geometry.Contract)
		require.Equal(t, arrangeFor(t, g, DefaultOptions()), f.Geometry)
	}
}

// The client shares this corpus (groupGeometry.contract.test.ts): every
// tampered geometry the Reader would refuse must also be refused by the route,
// and tampered ownership is input the Reader can never serve for its revision.
func TestGroupGeometry_SharedV2InvalidFixtures(t *testing.T) {
	raw, err := os.ReadFile("../../dashboard/src/graph/arranged/groupGeometry.v2.json")
	require.NoError(t, err)
	var fixtures []map[string]any
	require.NoError(t, json.Unmarshal(raw, &fixtures))
	raw, err = os.ReadFile("../../dashboard/src/graph/arranged/groupGeometry.v2.invalid.json")
	require.NoError(t, err)
	var cases []struct {
		Name    string `json:"name"`
		Fixture int    `json:"fixture"`
		Set     []struct {
			Path  []any `json:"path"`
			Value any   `json:"value"`
		} `json:"set"`
	}
	require.NoError(t, json.Unmarshal(raw, &cases))
	require.GreaterOrEqual(t, len(cases), 40)
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			base, err := json.Marshal(fixtures[c.Fixture])
			require.NoError(t, err)
			var doc any
			require.NoError(t, json.Unmarshal(base, &doc))
			geometryPatched := false
			for _, s := range c.Set {
				require.NotEmpty(t, s.Path)
				geometryPatched = geometryPatched || s.Path[0] == "geometry"
				doc = setJSONPath(t, doc, s.Path, s.Value)
			}
			patched, err := json.Marshal(doc)
			require.NoError(t, err)
			var f struct {
				Definition workflow.Workflow    `json:"definition"`
				Groups     *workflow.GroupGraph `json:"groups"`
				Geometry   Geometry             `json:"geometry"`
			}
			require.NoError(t, json.Unmarshal(patched, &f))
			g, err := BuildGraph(&f.Definition)
			require.NoError(t, err)
			if geometryPatched {
				assert.False(t, Compatible(f.Geometry, g), "Reader must refuse tampered geometry")
			} else {
				assert.True(t, Compatible(f.Geometry, g), "ownership-only case keeps the reviewed geometry")
				assert.NotEqual(t, g.Groups, f.Groups, "Reader serves View.Groups from BuildGraph, never this ownership")
			}
		})
	}
}

func setJSONPath(t *testing.T, doc any, path []any, value any) any {
	t.Helper()
	if len(path) == 0 {
		return value
	}
	switch v := doc.(type) {
	case map[string]any:
		key, ok := path[0].(string)
		require.True(t, ok, "object key %v", path[0])
		v[key] = setJSONPath(t, v[key], path[1:], value)
		return v
	case []any:
		i, ok := path[0].(float64)
		require.True(t, ok && int(i) < len(v), "array index %v", path[0])
		v[int(i)] = setJSONPath(t, v[int(i)], path[1:], value)
		return v
	default:
		require.Failf(t, "unaddressable path", "%v", path)
		return nil
	}
}

func TestGroupGeometry_RequiredFields(t *testing.T) {
	g, err := BuildGraph(crossingGroupWorkflow())
	require.NoError(t, err)
	geo := arrangeFor(t, g, DefaultOptions())
	raw, err := json.Marshal(geo)
	require.NoError(t, err)
	for _, field := range []string{"groupElements", "groupLanes", "groupConnectors", "groupCrossings"} {
		var body map[string]any
		require.NoError(t, json.Unmarshal(raw, &body))
		items := body[field].([]any)
		for i, item := range items {
			for key := range item.(map[string]any) {
				t.Run(fmt.Sprintf("%s/%d/%s", field, i, key), func(t *testing.T) {
					var body map[string]any
					require.NoError(t, json.Unmarshal(raw, &body))
					delete(body[field].([]any)[i].(map[string]any), key)
					encoded, err := json.Marshal(body)
					require.NoError(t, err)
					var bad Geometry
					require.NoError(t, json.Unmarshal(encoded, &bad))
					require.ErrorIs(t, bad.Validate(g), ErrInvalidGeometry)
				})
			}
		}
	}
	for _, field := range []string{"key", "text", "style", "box", "lines"} {
		t.Run("text/"+field, func(t *testing.T) {
			var body map[string]any
			require.NoError(t, json.Unmarshal(raw, &body))
			e := body["groupElements"].([]any)[0].(map[string]any)
			delete(e["texts"].([]any)[0].(map[string]any), field)
			encoded, err := json.Marshal(body)
			require.NoError(t, err)
			var bad Geometry
			require.NoError(t, json.Unmarshal(encoded, &bad))
			require.ErrorIs(t, bad.Validate(g), ErrInvalidGeometry)
		})
	}
	t.Run("synthetic endpoint ownership field", func(t *testing.T) {
		var body map[string]any
		require.NoError(t, json.Unmarshal(raw, &body))
		c := body["groupConnectors"].([]any)[0].(map[string]any)
		delete(c["sourceAttach"].(map[string]any), "nodeId")
		encoded, err := json.Marshal(body)
		require.NoError(t, err)
		var bad Geometry
		err = json.Unmarshal(encoded, &bad)
		if err == nil {
			err = bad.Validate(g)
		}
		require.ErrorIs(t, err, ErrInvalidGeometry)
	})
	geo.Format = FormatV1
	require.False(t, Compatible(geo, g))
}

// The shared wire-shape fixture uses Unicode IDs and synthetic-only endpoints.
func TestGroupGeometry_WireAndValidation(t *testing.T) {
	raw, err := os.ReadFile("../../dashboard/src/graph/arranged/groupGeometry.wire.json")
	require.NoError(t, err)
	var fixture map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &fixture))
	geo := validTwoNodeGeometry()
	base, err := json.Marshal(geo)
	require.NoError(t, err)
	var body map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(base, &body))
	for field, item := range map[string]string{"groupElements": "element", "groupLanes": "lane", "groupConnectors": "connector", "groupCrossings": "crossing"} {
		t.Run("wire/"+field, func(t *testing.T) {
			input := make(map[string]json.RawMessage, len(body)+1)
			for k, v := range body {
				input[k] = v
			}
			input[field] = append(append([]byte{'['}, fixture[item]...), ']')
			encoded, err := json.Marshal(input)
			require.NoError(t, err)
			var got Geometry
			require.NoError(t, json.Unmarshal(encoded, &got))
			out, err := json.Marshal(got)
			require.NoError(t, err)
			require.JSONEq(t, string(encoded), string(out), "stored geometry cannot discard %s", field)
			assert.NotEqual(t, GeometryDigest(geo), GeometryDigest(got), "evidence must bind %s", field)
			assert.ErrorIs(t, got.Validate(twoNodeGraph(t)), ErrInvalidGeometry, "ordinary v2 geometry cannot accept unowned group additions")
		})
	}
	t.Run("wire/attachment", func(t *testing.T) {
		var connector map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(fixture["connector"], &connector))
		for _, field := range []string{"sourceAttach", "targetAttach"} {
			var attachment Attachment
			require.NoError(t, json.Unmarshal(connector[field], &attachment))
			out, err := json.Marshal(attachment)
			require.NoError(t, err)
			assert.JSONEq(t, string(connector[field]), string(out), "attachment must preserve rendered element identity independently of NodeID")
			got := validTwoNodeGeometry()
			got.Edges[0].SourceAttach.ElementID = "wrong-element"
			assert.ErrorIs(t, got.Validate(twoNodeGraph(t)), ErrInvalidGeometry, "v2 cannot silently ignore rendered element identity")
		}
	})
	t.Run("wire/ordinary", func(t *testing.T) {
		for _, key := range []string{"groupElements", "groupLanes", "groupConnectors", "groupCrossings"} {
			assert.NotContains(t, body, key)
		}
	})
}
