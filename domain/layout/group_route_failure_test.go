package layout

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSearchGroupPath_RejectsOutsideEndpoints(t *testing.T) {
	canvas := Rect{X: 10, Y: 20, W: 100, H: 100}
	for _, tc := range []struct {
		name       string
		start, end Point
	}{
		{"source beyond right", Point{111, 30}, Point{50, 90}},
		{"source before left", Point{9, 30}, Point{50, 90}},
		{"target beyond right", Point{50, 30}, Point{111, 90}},
		{"target before left", Point{50, 30}, Point{9, 90}},
		{"source before top", Point{50, 19}, Point{50, 90}},
		{"source beyond bottom", Point{50, 121}, Point{50, 90}},
		{"target before top", Point{50, 30}, Point{50, 19}},
		{"target beyond bottom", Point{50, 30}, Point{50, 121}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, err := searchGroupPath(tc.start, tc.end, nil, nil, canvas, true, 10)
			require.Error(t, err)
			require.Empty(t, path)
		})
	}
}

// A wall spanning the region must exhaust the finite visibility grid; the
// control leaves a corridor around the same wall and must reach both endpoints.
func TestSearchGroupPath_BlockedCorridor(t *testing.T) {
	canvas := Rect{W: 100, H: 100}
	start, end := Point{50, 10}, Point{50, 90}
	path, err := searchGroupPath(start, end, []Rect{{X: 10, Y: 40, W: 80, H: 20}}, nil, canvas, true, 10)
	require.NoError(t, err)
	require.Equal(t, start, path[0])
	require.Equal(t, end, path[len(path)-1])
	path, err = searchGroupPath(start, end, []Rect{{X: -1, Y: 40, W: 102, H: 20}}, nil, canvas, true, 10)
	require.ErrorContains(t, err, "no obstacle-free orthogonal path")
	require.Empty(t, path)
}

func TestCoordinator_GroupBlockedRouteFailsAtArranging(t *testing.T) {
	// The larger required obstacle clearance closes the loop-return corridor.
	// Keep all measurements valid so the failure really comes from route search.
	c := DefaultContract()
	c.MinClearancePx = 64
	wf := crossingGroupWorkflow()
	before, err := json.Marshal(wf)
	require.NoError(t, err)
	g, err := BuildGraph(wf)
	require.NoError(t, err)
	groupsBefore, err := json.Marshal(g.Groups)
	require.NoError(t, err)
	r := newScriptedRenderer()
	m, err := r.Measure(context.Background(), MeasureRequestFor(g, c))
	require.NoError(t, err)
	geo, err := Arrange(context.Background(), g, m, c, DefaultOptions())
	require.ErrorContains(t, err, "no obstacle-free orthogonal path")
	require.Equal(t, Geometry{}, geo, "a partially routed group is never published")
	groupsAfter, err := json.Marshal(g.Groups)
	require.NoError(t, err)
	require.Equal(t, groupsBefore, groupsAfter)
	result := newCoord(t, r, &scriptedReviewer{}, WithContract(c)).Run(context.Background(), Job{Revision: "blocked", Manifest: wf, MaxAttempts: 5}, nil)
	require.Equal(t, VerdictFailed, result.Verdict)
	require.NotNil(t, result.Failure)
	require.Equal(t, StageArranging, result.Failure.Stage)
	require.Contains(t, result.Failure.Reason, "group route edge:")
	require.Contains(t, result.Failure.Reason, "no obstacle-free orthogonal path")
	require.Equal(t, 1, result.AttemptsUsed)
	require.Nil(t, result.Best)
	require.Empty(t, r.rendered)
	after, err := json.Marshal(wf)
	require.NoError(t, err)
	require.Equal(t, before, after)
}
