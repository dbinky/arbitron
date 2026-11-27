// adapter/http/runner_drain_internal_test.go
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

func TestRunnerHandler_DrainIsAdminOnlyAndRendersProgress(t *testing.T) {
	run := uuid.New()
	fake := sampleRunnerFake()
	fake.drain = runner.DrainProgress{ActiveSteps: 0, HomedWorkspaces: 1, Blocked: []runner.DrainBlocker{
		{Kind: runner.DrainKindWorkspace, RunID: &run, Group: "anthropic_subscription:anthropic", Reason: "no eligible runner for anthropic_subscription:anthropic"}}}

	w := doRunner(t, runnerRouter(fake, identity.RoleUser), http.MethodPost, "/api/v1/runners/mac-1/drain", "")
	assert.Equal(t, http.StatusForbidden, w.Code)

	w = doRunner(t, runnerRouter(fake, identity.RoleAdmin), http.MethodPost, "/api/v1/runners/mac-1/drain", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, runner.RunnerID("mac-1"), fake.gotID)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.EqualValues(t, 1, body["homedWorkspaces"])
	assert.Equal(t, false, body["complete"])
	b := body["blocked"].([]any)[0].(map[string]any)
	assert.Equal(t, run.String(), b["runId"])
	assert.Equal(t, "anthropic_subscription:anthropic", b["group"])

	fake.drainErr = runner.ErrDrainUnavailable
	w = doRunner(t, runnerRouter(fake, identity.RoleAdmin), http.MethodPost, "/api/v1/runners/mac-1/drain", "")
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestRunnerViewResponse_CarriesDrain(t *testing.T) {
	v := runner.RunnerView{Runner: runner.Runner{ID: "mac-1", Draining: true}, Drain: &runner.DrainProgress{ActiveSteps: 2}}
	b, err := json.Marshal(toRunnerViewResponse(v))
	require.NoError(t, err)
	assert.Contains(t, string(b), `"drain":{"activeSteps":2,"homedWorkspaces":0,"sessions":0,"blocked":[],"complete":false}`)
	b, _ = json.Marshal(toRunnerViewResponse(runner.RunnerView{Runner: runner.Runner{ID: "x"}}))
	assert.Contains(t, string(b), `"drain":null`)
}
