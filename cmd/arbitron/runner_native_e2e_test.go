package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
)

const nativeAgentManifest = `{
  "name": "native-agent",
  "description": "one remote agent step on the runner's own account",
  "inputs": [],
  "nodes": [{"id": "work", "type": "agent_call", "entrypoint": true, "config": {"model_profile": "@PROFILE@", "templateFile": "templates/work.md"}}],
  "edges": []
}`

func nativeWorkflow(profile, prompt string) p3Workflow {
	return p3Workflow{manifest: strings.ReplaceAll(nativeAgentManifest, "@PROFILE@", profile), files: map[string]string{"templates/work.md": prompt}}
}

func createAgentProfile(t *testing.T, st *p3Stack, p provider.Profile) {
	t.Helper()
	p.Kind, p.SelectionMode = provider.ProfileKindAgent, provider.SelectionPinned
	ctx := adminCtxForLocalUser(t, st.ds)
	_, _, err := st.ds.profileSvc.Create(ctx, p)
	require.NoError(t, err)
	if p.Backend == "claude-code" {
		// D1 requests chat-profile models at admission; the same natively
		// verified credential then advertises both chat and agent capabilities.
		_, _, err = st.ds.profileSvc.Create(ctx, provider.Profile{Alias: p.Alias + "-chat", Kind: provider.ProfileKindChat, Model: p.Model})
		require.NoError(t, err)
	}
}

func cordonBuiltIn(t *testing.T, st *p3Stack) {
	t.Helper()
	require.NoError(t, st.ds.runners.service.Cordon(adminCtxForLocalUser(t, st.ds), runner.BuiltInRunnerID))
}

func runAndWait(t *testing.T, st *p3Stack, wf string) string {
	t.Helper()
	ctx := adminCtxForLocalUser(t, st.ds)
	id, _, err := st.ds.eng.Trigger(ctx, wf, map[string]any{}, "normal")
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.ds.eng.Cancel(ctx, id) })
	p3RequireCompleted(t, st.ds, id, 90*time.Second)
	return id.String()
}

// Happy: a runner with ONLY codex installed and logged in is discovered, offered
// the step, launches the exact codex model, and the modified bytes come home.
func TestPhase3E2E_CodexOnlyRunnerIsDiscoveredAndExecutesAgent(t *testing.T) {
	st := bootP3Stack(t, map[string]p3Workflow{"native-agent": nativeWorkflow("codex-worker", "P3-CODEX do the work")}, "")
	createAgentProfile(t, st, provider.Profile{Alias: "codex-worker", Backend: "codex", Model: "gpt-6.1-sol"})
	cordonBuiltIn(t, st)
	mac := startNativeRunner(t, st, nativeRunnerOpts{name: "mac-codex", codex: true})

	require.Eventually(t, func() bool {
		return len(rrStrings(t, st.ds, `SELECT capabilities FROM runner_sources WHERE runner_id = ? AND kind = 'openai_subscription' AND unavailable_reason = ''`, mac.id)) == 1
	}, 20*time.Second, 100*time.Millisecond, "SINK: the discovered codex source is registered and available")
	require.Empty(t, rrStrings(t, st.ds, `SELECT id FROM runner_sources WHERE runner_id = ? AND kind = 'anthropic_subscription'`, mac.id), "no claude on this runner")

	id := runAndWait(t, st, "native-agent")
	assert.Equal(t, []string{mac.id}, p3CompletedOn(t, st.ds, uuidMust(t, id), "work"), "trace: the step ran on the codex runner")
	assert.Equal(t, []string{"openai_subscription"}, rrStrings(t, st.ds,
		`SELECT s.kind FROM node_executions n JOIN runner_sources s ON s.id = n.source_id WHERE n.instance_id = ? AND n.node_id = 'work' AND n.status = 'succeeded'`, id), "trace: source")

	log := readLog(t, mac.codexLog)
	assert.Contains(t, log, mac.codexBin+" exec --dangerously-bypass-approvals-and-sandbox -m gpt-6.1-sol", "exact model on the configured binary")
	b, err := os.ReadFile(filepath.Join(st.workspaceDir, "instances", id, "codex-output.txt"))
	require.NoError(t, err, "SINK: the committed checkpoint was restored with codex's bytes")
	assert.Equal(t, "codex ran gpt-6.1-sol\n", string(b))
	assert.Contains(t, log, mac.codexBin+" app-server", "the runner's own rateLimits RPC ran")
	assert.NotEmpty(t, rrStrings(t, st.ds, `SELECT windows FROM runner_sources WHERE runner_id = ? AND kind = 'openai_subscription' AND windows NOT IN ('', '[]', 'null')`, mac.id),
		"SINK: a non-empty rate-limit observation from the runner's RPC")
}

// Success: one Claude source serves chat AND agent; the restrictive tool scope,
// context and effort reach the real CLI argv/env; installing a newer harness
// refreshes eligibility and queued work proceeds without a server restart.
func TestPhase3E2E_ClaudeChatSourceAlsoAdvertisesWorkingAgent(t *testing.T) {
	st := bootP3Stack(t, map[string]p3Workflow{"native-agent": nativeWorkflow("claude-worker", "P3-CLAUDE edit")}, "")
	createAgentProfile(t, st, provider.Profile{Alias: "claude-worker", Backend: "claude-code", Model: "claude-opus-5-5",
		AllowedTools: []string{"Read", "Grep"}, ContextWindow: provider.Context200K, Effort: provider.EffortHigh})
	cordonBuiltIn(t, st)
	mac := startNativeRunner(t, st, nativeRunnerOpts{name: "mac-claude", claude: true, claudeVersion: "0.0.1"}) // below the floor: visible, ineligible

	id, _, err := st.ds.eng.Trigger(adminCtxForLocalUser(t, st.ds), "native-agent", map[string]any{}, "normal")
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.ds.eng.Cancel(adminCtxForLocalUser(t, st.ds), id) })
	time.Sleep(3 * time.Second)
	assert.NotContains(t, readLog(t, mac.claudeLog), "--allowedTools", "an unsupported harness is never offered the step")

	mac.installClaude(t, "2.1.400") // operator upgrades the harness; no server restart
	p3RequireCompleted(t, st.ds, id, 90*time.Second)

	sources := rrStrings(t, st.ds, `SELECT id FROM runner_sources WHERE runner_id = ? AND kind = 'anthropic_subscription'`, mac.id)
	require.Len(t, sources, 1, "one source per credential kind after the refresh, no duplicate")
	caps := rrStrings(t, st.ds, `SELECT capabilities FROM runner_sources WHERE id = ?`, sources[0])[0]
	assert.Contains(t, caps, `"chat"`)
	assert.Contains(t, caps, `"agent"`)
	log := readLog(t, mac.claudeLog)
	assert.Contains(t, log, "--allowedTools Read,Grep", "the restrictive scope reached the real CLI")
	assert.Contains(t, log, "--model claude-opus-5-5")
	assert.Contains(t, log, "effort=high window=200000", "context and effort reached the CLI env")
	assert.Len(t, rrStrings(t, st.ds, `SELECT id FROM runner_jobs WHERE run_id = ? AND node_id = 'work'`, id.String()), 1, "no duplicate job after the reconnect")
}

// Failure: a wrong executed model leaves the prior checkpoint, local checkpoint
// (RR-17 B) and trace success untouched.
func TestNativeAgent_StrictWrongModelCannotPushOrCommit(t *testing.T) {
	st := bootP3Stack(t, map[string]p3Workflow{"native-agent": nativeWorkflow("codex-worker", "P3-WRONG-MODEL")}, "")
	createAgentProfile(t, st, provider.Profile{Alias: "codex-worker", Backend: "codex", Model: "gpt-6.1-sol"})
	cordonBuiltIn(t, st)
	mac := startNativeRunner(t, st, nativeRunnerOpts{name: "mac-codex", codex: true})

	id, _, err := st.ds.eng.Trigger(adminCtxForLocalUser(t, st.ds), "native-agent", map[string]any{}, "normal")
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.ds.eng.Cancel(adminCtxForLocalUser(t, st.ds), id) })
	require.Eventually(t, func() bool { return strings.Contains(readLog(t, mac.codexLog), " exec ") }, 60*time.Second, 100*time.Millisecond)
	before := rrStrings(t, st.ds, `SELECT checkpoint_version || '|' || local_checkpoint FROM workspace_homes WHERE run_id = ?`, id.String())
	committed := rrStrings(t, st.ds, `SELECT version FROM workspace_checkpoints WHERE run_id = ? AND state = 'committed'`, id.String())
	time.Sleep(3 * time.Second)

	assert.Empty(t, rrStrings(t, st.ds, `SELECT node_id FROM node_executions WHERE instance_id = ? AND node_id = 'work' AND status = 'succeeded'`, id.String()), "no trace success")
	assert.Equal(t, before, rrStrings(t, st.ds, `SELECT checkpoint_version || '|' || local_checkpoint FROM workspace_homes WHERE run_id = ?`, id.String()), "SINK: checkpoint and local checkpoint unchanged")
	assert.Equal(t, committed, rrStrings(t, st.ds, `SELECT version FROM workspace_checkpoints WHERE run_id = ? AND state = 'committed'`, id.String()), "no candidate was committed")
	_, err = os.Stat(filepath.Join(st.workspaceDir, "instances", id.String(), "codex-output.txt"))
	assert.True(t, os.IsNotExist(err), "the wrong-model edit never reached the server")
}

// Error: the configured codex binary is the one used for version, login,
// rateLimits and exec.
func TestBuildAgent_CodexBinaryOverrideFeedsVersionAuthAndExecution(t *testing.T) {
	st := bootP3Stack(t, map[string]p3Workflow{"native-agent": nativeWorkflow("codex-worker", "P3-CODEX override")}, "")
	createAgentProfile(t, st, provider.Profile{Alias: "codex-worker", Backend: "codex", Model: "gpt-6.1-sol"})
	cordonBuiltIn(t, st)
	mac := startNativeRunner(t, st, nativeRunnerOpts{name: "mac-codex", codex: true})
	runAndWait(t, st, "native-agent")
	log := readLog(t, mac.codexLog)
	for _, call := range []string{" --version", " login status", " app-server", " exec "} {
		assert.Contains(t, log, mac.codexBin+call, "the override binary served %q", call)
	}
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		assert.True(t, strings.HasPrefix(line, mac.codexBin+" "), "no other codex was invoked: %s", line)
	}
}

// Edge: a node tool scope over an empty profile scope is the placed AND
// launched scope.
func TestNativeAgent_NodeToolScopeOverridesEmptyProfileForPlacement(t *testing.T) {
	wf := nativeWorkflow("claude-worker", "P3-SCOPE")
	wf.manifest = strings.Replace(wf.manifest, `"templateFile"`, `"toolScope": ["Grep"], "templateFile"`, 1)
	st := bootP3Stack(t, map[string]p3Workflow{"native-agent": wf}, "")
	createAgentProfile(t, st, provider.Profile{Alias: "claude-worker", Backend: "claude-code", Model: "claude-opus-5-5"})
	cordonBuiltIn(t, st)
	mac := startNativeRunner(t, st, nativeRunnerOpts{name: "mac-claude", claude: true})
	id := runAndWait(t, st, "native-agent")
	reqs := rrStrings(t, st.ds, `SELECT requirements FROM runner_jobs WHERE run_id = ? AND node_id = 'work'`, id)
	require.Len(t, reqs, 1)
	assert.Contains(t, reqs[0], `"Tools":["Grep"]`, "SINK: placement used the node scope")
	assert.Contains(t, readLog(t, mac.claudeLog), "--allowedTools Grep", "the same scope was launched")
}

// Edge: a refresh while a step runs keeps that step's spool; one job, one
// source, one result after the controlled reconnect.
func TestNativeInventory_RefreshRetainsInFlightSpool(t *testing.T) {
	st := bootP3Stack(t, map[string]p3Workflow{"native-agent": nativeWorkflow("codex-worker", "P3-HOLD")}, "")
	createAgentProfile(t, st, provider.Profile{Alias: "codex-worker", Backend: "codex", Model: "gpt-6.1-sol"})
	cordonBuiltIn(t, st)
	mac := startNativeRunner(t, st, nativeRunnerOpts{name: "mac-codex", codex: true})

	id, _, err := st.ds.eng.Trigger(adminCtxForLocalUser(t, st.ds), "native-agent", map[string]any{}, "normal")
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.ds.eng.Cancel(adminCtxForLocalUser(t, st.ds), id) })
	require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(mac.gate, "codex.started")); return err == nil }, 60*time.Second, 50*time.Millisecond)

	mac.installClaude(t, "2.1.400") // inventory changes mid-step → controlled reconnect
	require.Eventually(t, func() bool {
		return len(rrStrings(t, st.ds, `SELECT id FROM runner_sources WHERE runner_id = ? AND kind = 'anthropic_subscription'`, mac.id)) == 1
	}, 30*time.Second, 100*time.Millisecond, "the new source was re-advertised")
	require.NoError(t, os.WriteFile(filepath.Join(mac.gate, "codex.release"), nil, 0o600))
	p3RequireCompleted(t, st.ds, id, 90*time.Second)

	assert.Len(t, rrStrings(t, st.ds, `SELECT id FROM runner_jobs WHERE run_id = ? AND node_id = 'work'`, id.String()), 1, "no duplicate job")
	assert.Equal(t, []string{"1"}, rrStrings(t, st.ds, `SELECT generation FROM runner_jobs WHERE run_id = ? AND node_id = 'work'`, id.String()), "the in-flight attempt was retained, not re-dispatched")
	assert.Len(t, rrStrings(t, st.ds, `SELECT id FROM runner_sources WHERE runner_id = ? AND kind = 'openai_subscription'`, mac.id), 1, "no duplicate source")
	assert.Equal(t, 1, strings.Count(readLog(t, mac.codexLog), " exec "), "the step executed exactly once")
}
