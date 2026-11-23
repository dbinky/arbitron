package http

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func homesOf(t *testing.T, body []byte) []any {
	t.Helper()
	var d map[string]any
	require.NoError(t, json.Unmarshal(body, &d))
	homes, ok := d["homes"].([]any)
	require.True(t, ok, "homes is always a JSON array: %s", body)
	return homes
}

// Happy path: each home renders as {runId, hidden, generation, dirty}.
func TestRunnerHandler_GetRendersHomes(t *testing.T) {
	fake := sampleRunnerFake()
	run := uuid.New()
	fake.detail.Homes = []runner.HomeView{{RunID: &run, Generation: 3, Dirty: true}, {Hidden: true, Generation: 5}}
	w := doRunner(t, runnerRouter(fake, identity.RoleUser), http.MethodGet, "/api/v1/runners/builtin", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	homes := homesOf(t, w.Body.Bytes())
	require.Len(t, homes, 2)
	assert.Equal(t, map[string]any{"runId": run.String(), "hidden": false, "generation": float64(3), "dirty": true}, homes[0])
	assert.Equal(t, map[string]any{"runId": nil, "hidden": true, "generation": float64(5), "dirty": false}, homes[1])
}

// Edge: a hidden item never renders a run id, even if the view carried one.
func TestRunnerHandler_HiddenHomeNeverLeaksRunID(t *testing.T) {
	fake := sampleRunnerFake()
	run := uuid.New()
	fake.detail.Homes = []runner.HomeView{{RunID: &run, Hidden: true, Generation: 2}}
	w := doRunner(t, runnerRouter(fake, identity.RoleUser), http.MethodGet, "/api/v1/runners/builtin", "")
	require.Equal(t, http.StatusOK, w.Code)
	homes := homesOf(t, w.Body.Bytes())
	require.Len(t, homes, 1)
	assert.Nil(t, homes[0].(map[string]any)["runId"])
	assert.NotContains(t, w.Body.String(), run.String())
}

// Edge: no homes renders an empty array, never null.
func TestRunnerHandler_GetWithoutHomesRendersEmptyList(t *testing.T) {
	w := doRunner(t, runnerRouter(sampleRunnerFake(), identity.RoleUser), http.MethodGet, "/api/v1/runners/builtin", "")
	require.Equal(t, http.StatusOK, w.Code)
	assert.Empty(t, homesOf(t, w.Body.Bytes()))
	assert.Contains(t, w.Body.String(), `"homes":[]`)
}
