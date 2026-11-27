package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/runner"
)

// rhuFakeClaude is rwFakeClaudeScript with the `--version` answer as a
// parameter (@VERSION@) and every model call released.
func rhuFakeClaude(dir, version string) string {
	s := strings.ReplaceAll(rwFakeClaudeScript, `echo "2.1.330 (Claude Code)"`, `echo "`+version+` (Claude Code)"`)
	return strings.ReplaceAll(s, "@DIR@", dir)
}

// rhuInstallClaude atomically replaces the fake claude binary, so a concurrent
// inventory `--version` never reads a half-written script.
func rhuInstallClaude(t *testing.T, dir, version string) {
	t.Helper()
	tmp := filepath.Join(dir, "claude.next")
	require.NoError(t, os.WriteFile(tmp, []byte(rhuFakeClaude(dir, version)), 0o700))
	require.NoError(t, os.Rename(tmp, filepath.Join(dir, "claude")))
}

type rhuRunnerView struct {
	HarnessVersions map[string]string `json:"harnessVersions"`
	Sources         []struct {
		ID                string `json:"id"`
		UnavailableReason string `json:"unavailableReason"`
	} `json:"sources"`
}

func (v rhuRunnerView) reason(t *testing.T, id runner.SourceID) string {
	t.Helper()
	for _, s := range v.Sources {
		if s.ID == string(id) {
			return s.UnavailableReason
		}
	}
	t.Fatalf("source %s missing from %+v", id, v.Sources)
	return ""
}

// Alignment acceptance (P1 T16, inventory/version refusal -> registry ->
// placement -> API): an unsupported claude `--version` blocks the built-in
// anthropic subscription source (registry, REST, MCP), the llm_call waits on
// capacity with zero offers and zero model calls while the production
// inventory loop keeps re-detecting the refusal, and once the binary reports
// a supported version the LOOP (never the test) restores the source and the
// waiting step completes on builtin with exactly one call.
func TestRunnerWiring_HarnessUpgradeRestoresEligibility(t *testing.T) {
	const unsupported, supported = "2.1.200", "2.1.330"
	ok, refusal := claudecli.CheckVersion(unsupported)
	require.False(t, ok, "fixture: %s must be below the claude floor", unsupported)
	require.NotEmpty(t, refusal)
	ok, _ = claudecli.CheckVersion(supported)
	require.True(t, ok, "fixture: %s must meet the claude floor", supported)

	configPath, fake := rwWriteRoot(t, 8)
	rhuInstallClaude(t, fake, unsupported)
	rwTouch(t, fake, "release-all")
	f, err := os.OpenFile(configPath, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString("  probeintervalseconds: 5\n  probejitterseconds: 0\n") // the inventory loop's cadence
	require.NoError(t, err)
	require.NoError(t, f.Close())

	ds, err := initStartedDomain(context.Background(), configPath) // Start runs the real inventory loop
	require.NoError(t, err)
	t.Cleanup(func() { ds.Close(context.Background()) })
	ctx := adminCtxForLocalUser(t, ds)
	rwChatProfile(t, ctx, ds)
	srv := gsServeAPI(t, ds)
	mcp := mcpToolCallerAs(t, ds, ctx)

	viewREST := func() rhuRunnerView {
		resp, err := http.Get(srv.URL + "/api/v1/runners/builtin")
		require.NoError(t, err)
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
		var v rhuRunnerView
		require.NoError(t, json.Unmarshal(raw, &v))
		return v
	}
	viewMCP := func() rhuRunnerView {
		isErr, text := mcp("runner_get", map[string]any{"id": "builtin"})
		require.False(t, isErr, text)
		var v rhuRunnerView
		require.NoError(t, json.Unmarshal([]byte(text), &v))
		return v
	}
	registryReason := func() string {
		src, err := ds.runners.registry.GetSource(ctx, rwAnthropic)
		require.NoError(t, err)
		return src.UnavailableReason
	}
	lastContact := func() time.Time {
		r, _, err := ds.runners.registry.GetRunner(ctx, runner.BuiltInRunnerID)
		require.NoError(t, err)
		require.NotNil(t, r.LastContact)
		return *r.LastContact
	}

	// Refusal: registry, REST and MCP all carry the version refusal.
	require.Equal(t, "harness_unsupported", registryReason())
	for name, v := range map[string]rhuRunnerView{"REST": viewREST(), "MCP": viewMCP()} {
		assert.Equal(t, "harness_unsupported", v.reason(t, rwAnthropic), name)
		assert.Equal(t, unsupported, v.HarnessVersions["claude-code"], "%s shows the refused version", name)
	}

	id, _, err := ds.eng.Trigger(ctx, "rr-block", map[string]any{}, "normal")
	require.NoError(t, err)
	t.Cleanup(func() { // a failed assertion must not leave a capacity wait blocking Close
		if inst, gerr := ds.instanceRepo.Get(context.Background(), id); gerr == nil && !inst.State.IsTerminal() {
			_ = ds.eng.Cancel(ctx, id)
		}
	})
	var st instance.ExecutionStatus
	require.Eventually(t, func() bool {
		var found bool
		st, found = rwExecutionOf(ctx, ds, id)
		return found && st.State == instance.ExecWaitingCapacity
	}, 30*time.Second, 50*time.Millisecond, "the step waits on capacity while the harness is refused")
	assert.Equal(t, "anthropic_subscription:anthropic", st.Group)
	assert.Contains(t, st.Reason, "blocked=harness_unsupported", "the wait names the version refusal")

	// Sustained window spanning at least one inventory tick: the loop re-detects
	// the refusal (runner contact advances) and placement still offers nothing.
	windowStart := lastContact()
	sawTick := false
	deadline := time.Now().Add(7 * time.Second)
	for time.Now().Before(deadline) {
		require.Equal(t, 0, rwCountJobs(ds.db, "run_id = '"+id.String()+"' AND state IN ('offered', 'running', 'completed')"), "no offer while unsupported")
		require.Equal(t, 0, rwCalls(fake), "no model call while unsupported")
		if lastContact().After(windowStart) {
			sawTick = true
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.True(t, sawTick, "the inventory loop ticked during the window")
	assert.Equal(t, "harness_unsupported", registryReason(), "the loop keeps refusing the unsupported version")
	st, found := rwExecutionOf(ctx, ds, id)
	require.True(t, found)
	assert.Equal(t, instance.ExecWaitingCapacity, st.State)
	inst, err := ds.instanceRepo.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, instance.StateRunning, inst.State, "a capacity wait never changes the run state")

	// Upgrade: only the binary changes; the loop must notice and wake placement.
	rhuInstallClaude(t, fake, supported)
	require.Eventually(t, func() bool { return registryReason() == "" }, 20*time.Second, 50*time.Millisecond,
		"the inventory loop restores the source after a supported version is detected")
	for name, v := range map[string]rhuRunnerView{"REST": viewREST(), "MCP": viewMCP()} {
		assert.Empty(t, v.reason(t, rwAnthropic), name)
		assert.Equal(t, supported, v.HarnessVersions["claude-code"], name)
	}
	require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, id).State)
	jobs := rwJobs(t, ds.db, id)
	assert.True(t, rwEveryOpPlacedOnBuiltin(jobs, map[string]string{"ask": "chat"}), "%+v", jobs)
	require.Len(t, jobs, 1)
	assert.Equal(t, string(rwAnthropic), jobs[0].SourceID)
	assert.Equal(t, 1, rwCalls(fake), "exactly one model call after the upgrade")
}
