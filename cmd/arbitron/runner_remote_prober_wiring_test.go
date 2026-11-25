package main

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The offline path needs no harness execution or cordon. It still goes through
// the installed application root, persisted remote inventory and operator API.
func TestRemoteProbe_InstalledRootRefusesOfflineSource(t *testing.T) {
	st := bootRemoteRunnerStack(t, nil, false)
	_, id, _ := rrCreateKey(t, st, "offline-health")
	sid := runner.MakeSourceID(runner.RunnerID(id), runner.CredAnthropicSubscription, "")
	ctx := context.Background()
	require.NoError(t, st.ds.runners.registry.ReplaceSources(ctx, runner.RunnerID(id), []runner.Source{{
		ID: sid, RunnerID: runner.RunnerID(id), Kind: runner.CredAnthropicSubscription,
		Health: runner.SourceHealth{Auth: runner.AuthOK, Capacity: runner.CapacityAvailable},
	}}))
	_, err := st.ds.runners.monitor.ProbeNow(ctx, sid)
	require.ErrorIs(t, err, runner.ErrRemoteProbeUnavailable)
	assert.NotErrorIs(t, err, runner.ErrRemoteProberNotWired)
	code, body := rrDo(t, http.MethodPost, st.srv.URL+"/api/v1/runners/sources/"+url.PathEscape(string(sid))+"/probe", nil)
	assert.NotEqual(t, http.StatusOK, code, string(body))
}
