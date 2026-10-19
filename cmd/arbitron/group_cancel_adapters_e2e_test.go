package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cancelViaHTTP drives POST /api/v1/instances/{id}/cancel through the router
// runServe builds from the domain stack, returning the status code and the
// decoded body of a successful response (nil otherwise).
func cancelViaHTTP(t *testing.T, ds *domainStack, id uuid.UUID) (int, map[string]string) {
	t.Helper()
	srv := gsServeAPI(t, ds)
	resp, err := http.Post(srv.URL+"/api/v1/instances/"+id.String()+"/cancel", "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil
	}
	var body map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	return resp.StatusCode, body
}

// cancelViaMCP drives the instance_cancel tool through the production MCP
// server and reports whether the tool call succeeded, with the decoded body of
// a successful call (nil otherwise).
func cancelViaMCP(t *testing.T, ds *domainStack, id uuid.UUID) (bool, map[string]string) {
	t.Helper()
	res := callMCPTool(t, newGroupMCPServer(ds), adminCtxForLocalUser(t, ds), "instance_cancel",
		map[string]any{"id": id.String()})
	if res.IsError {
		t.Logf("instance_cancel error: %s", mcpText(t, res))
	}
	if res.IsError {
		return false, nil
	}
	var body map[string]string
	require.NoError(t, json.Unmarshal([]byte(mcpText(t, res)), &body))
	return true, body
}

// WIRING: the HTTP and MCP cancel adapters reach the real engine and its
// SQLite-backed cascade. A completed parent whose independent task is still
// running is stopped, and the parent's outcome is not reopened.
func TestGroupCancelAdaptersE2E_CompletedParentStopsIndependentTask(t *testing.T) {
	channels := map[string]func(*testing.T, *domainStack, uuid.UUID) (bool, map[string]string){
		"http": func(t *testing.T, ds *domainStack, id uuid.UUID) (bool, map[string]string) {
			code, body := cancelViaHTTP(t, ds, id)
			return code == http.StatusOK, body
		},
		"mcp": cancelViaMCP,
	}
	for name, cancel := range channels {
		t.Run(name, func(t *testing.T) {
			ds, gates := bootGatedBehaviorStack(t, 5, map[string]string{"grp-indep-long": fsIndependentLongManifest},
				map[string][]string{"grp-indep-long": {"slow-hold", "fast-set"}})

			parentID := triggerGroupWorkflow(t, ds, "grp-indep-long")
			gates.reached(t, "slow-hold")
			gates.reached(t, "fast-set")
			waitForState(t, ds, parentID, instance.StateWaiting)
			gates.release(t, "fast-set")
			slowID := taskChildID(t, ds, parentID, "race", "slow")
			require.Equal(t, instance.StateCompleted, waitForTerminalState(t, ds, parentID))
			require.False(t, stateOf(t, ds, slowID).IsTerminal(), "precondition: the gated loser is still running")

			ok, body := cancel(t, ds, parentID)
			require.True(t, ok, "cancelling a completed parent with independent work must succeed")
			// The response must not claim the completed parent itself was cancelled.
			assert.Equal(t, "cancel_requested", body["status"])
			assert.Equal(t, "completed", body["state"])

			waitForState(t, ds, slowID, instance.StateCancelled)
			drainBehaviorEngine(t, ds)
			assert.Equal(t, instance.StateCompleted, stateOf(t, ds, parentID), "the parent's outcome is never reopened")
		})
	}
}

// WIRING: cancelling a RUNNING nested-group parent over HTTP cancels every
// level and never routes either port of the cancelled parents.
func TestGroupCancelAdaptersE2E_HTTPCancelCascadesThroughNestedGroups(t *testing.T) {
	ds, gates := bootGatedBehaviorStack(t, 5, map[string]string{
		"grp-cancel-outer": cancelOuterManifest,
		"grp-cancel-inner": cancelInnerManifest,
	}, map[string][]string{"grp-cancel-inner": {"x-hold", "y-hold"}})

	parentID := triggerGroupWorkflow(t, ds, "grp-cancel-outer")
	gates.reached(t, "x-hold")
	gates.reached(t, "y-hold")
	innerID := taskChildID(t, ds, parentID, "outer", "c")
	waitForState(t, ds, innerID, instance.StateWaiting)
	xID := taskChildID(t, ds, innerID, "inner", "x")
	yID := taskChildID(t, ds, innerID, "inner", "y")
	waitForState(t, ds, xID, instance.StateRunning)
	waitForState(t, ds, yID, instance.StateRunning)

	code, body := cancelViaHTTP(t, ds, parentID)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "cancelled", body["status"], "an active parent really is cancelled")
	assert.Equal(t, "cancelled", body["state"])

	for _, id := range []uuid.UUID{parentID, innerID, xID, yID} {
		waitForState(t, ds, id, instance.StateCancelled)
	}
	drainBehaviorEngine(t, ds)
	assert.Nil(t, branchVariable(t, ds, parentID), "neither port of the cancelled parent may run")
}

// A terminal parent with nothing active below it still refuses cancellation
// through the adapters (HTTP 409), so the "cancelled" response is never a lie
// about a no-op.
func TestGroupCancelAdaptersE2E_TerminalParentWithNothingActiveIsRefused(t *testing.T) {
	ds := bootSubtreeStack(t)
	midID, _, err := ds.eng.Trigger(context.Background(), "cs-mid", map[string]any{}, "normal")
	require.NoError(t, err)
	waitForState(t, ds, midID, instance.StateCompleted)
	leafID := onlyChild(t, ds, midID)
	waitForState(t, ds, leafID, instance.StateRunning)
	require.NoError(t, ds.eng.Cancel(adminCtxForLocalUser(t, ds), leafID))
	waitForState(t, ds, leafID, instance.StateCancelled)

	code, _ := cancelViaHTTP(t, ds, midID)
	assert.Equal(t, http.StatusConflict, code)
	ok, _ := cancelViaMCP(t, ds, midID)
	assert.False(t, ok)
}
