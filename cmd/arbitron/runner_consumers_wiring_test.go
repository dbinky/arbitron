package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rcuFakeClaudeScript stands in for the claude CLI on the agent path. It answers
// --version and `auth status` for inventory, logs each model call, and returns
// a JSON summary so the agent_call output is typed (parsed), not wrapped text.
const rcuFakeClaudeScript = `#!/bin/sh
case "$1" in
  --version) echo "2.1.330 (Claude Code)"; exit 0 ;;
  auth) echo '{"loggedIn":true}'; exit 0 ;;
esac
cat > /dev/null
echo "$*" >> '@DIR@/calls.log'
m=$(echo "$*" | sed -n 's/.*--model \([^ ]*\).*/\1/p')
cat <<EOF
{"type":"result","is_error":false,"result":"{\"verdict\":\"pass\",\"score\":3}","session_id":"s","usage":{"input_tokens":1,"output_tokens":1},"modelUsage":{"$m":{}}}
EOF
`

// rcExecs returns a run's node executions grouped by node, in recorded order.
func rcExecs(t *testing.T, ds *domainStack, id uuid.UUID) map[string][]instance.NodeExecution {
	t.Helper()
	execs, err := ds.nodeExecRepo.ListByInstance(context.Background(), id)
	require.NoError(t, err)
	out := map[string][]instance.NodeExecution{}
	for _, e := range execs {
		out[e.NodeID] = append(out[e.NodeID], e)
	}
	return out
}

// TestRunnerWiring_AllExecutionConsumersUsePool (P1 T16 alignment acceptance:
// routed agent, tool_call and child-input preparation consumers). Through the
// REAL composition root, with only the claude executable and an HTTP upstream
// replaced: agent_call and tool_call each leave a completed runner_jobs row on
// the built-in runner with their op, a builtin-placed trace and a typed output;
// a spawn with one unmapped required child input makes exactly one chat
// preparation job and the child receives the prepared value; a fully mapped
// spawn makes zero preparation jobs and zero upstream calls.
func TestRunnerWiring_AllExecutionConsumersUsePool(t *testing.T) {
	setenvForTest(t, "PATH", "/usr/bin:/bin") // no host codex/opencode/pi leak into the inventory
	setenvForTest(t, "HOME", t.TempDir())
	fake := t.TempDir()
	bin := filepath.Join(fake, "claude")
	require.NoError(t, os.WriteFile(bin, []byte(strings.ReplaceAll(rcuFakeClaudeScript, "@DIR@", fake)), 0o700))

	var upstreamCalls atomic.Int32
	const prepModel = "deepseek/deepseek-v4-flash"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":%q}}]}`, prepModel, `{"n": 7}`)
	}))
	t.Cleanup(upstream.Close)

	ds := bootSinkRoot(t, "llm:\n  binarypath: "+yamlQuote(bin)+"\n", func(ws string) {
		writeWorkflow(t, ws, "rc-work", `{"name": "rc-work", "inputs": [], "nodes": [
    {"id": "work", "type": "agent_call", "entrypoint": true, "config": {"model_profile": "rc-agent", "templateFile": "templates/p.md"}},
    {"id": "tool", "type": "tool_call", "config": {"command": "sh", "args": ["-c", "printf '{\"count\":2,\"ok\":true}'"]}}
  ], "edges": [{"source": "work", "target": "tool"}]}`)
		writePinTemplate(t, ws, "rc-work")
		writeWorkflow(t, ws, "rc-child", `{"name": "rc-child", "inputs": [
    {"name": "branch", "type": "string", "description": "b", "required": true},
    {"name": "n", "type": "number", "description": "n", "required": true}
  ], "nodes": [
    {"id": "echo", "type": "transform", "entrypoint": true,
     "config": {"operations": [{"variable": "echoed", "operation": "set", "value": "{{n}}"}]}}
  ], "edges": []}`)
		for name, inputs := range map[string]string{
			"rc-prep":   `{"branch": "{{branch}}"}`,
			"rc-mapped": `{"branch": "{{branch}}", "n": 3}`,
		} {
			writeWorkflow(t, ws, name, `{"name": "`+name+`", "inputs": [{"name": "branch", "type": "string", "description": "b", "required": true}],
  "nodes": [
    {"id": "spawn", "type": "spawn_workflow", "entrypoint": true,
     "config": {"childWorkflow": "rc-child", "spawnMode": "wait_for_child", "model_profile": "rc-prep-chat", "inputs": `+inputs+`}},
    {"id": "onSuccess", "type": "transform", "config": {"operations": [{"variable": "childN", "operation": "set", "value": "{{spawn.childOutput.n}}"}]}},
    {"id": "onFailure", "type": "transform", "config": {"operations": [{"variable": "failed", "operation": "set", "value": true}]}}
  ],
  "edges": [{"source": "spawn", "target": "onSuccess", "output": "success"}, {"source": "spawn", "target": "onFailure", "output": "failure"}]}`)
		}
		writeWorkflow(t, ws, "rc-prep-ff", `{"name": "rc-prep-ff", "inputs": [{"name": "branch", "type": "string", "description": "b", "required": true}],
  "nodes": [
    {"id": "spawn", "type": "spawn_workflow", "entrypoint": true,
     "config": {"childWorkflow": "rc-child", "spawnMode": "fire_and_forget", "model_profile": "rc-prep-chat", "inputs": {"branch": "{{branch}}"}}},
    {"id": "after", "type": "transform", "config": {"operations": [{"variable": "src", "operation": "set", "value": "{{spawn.inputSources.n}}"}]}}
  ],
  "edges": [{"source": "spawn", "target": "after"}]}`)
	})
	ctx := adminCtxForLocalUser(t, ds)
	_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "rc-agent", Kind: provider.ProfileKindAgent, Backend: "claude-code", Model: "claude-opus-4-1"})
	require.NoError(t, err)
	crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
	require.NoError(t, err)
	conns := newSyncingConnStore(adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy), ds.runners.refreshInventory)
	require.NoError(t, conns.Create(ctx, provider.Connection{Name: "rc-api", Provider: "openrouter", BaseURL: upstream.URL}, "rc-secret"))
	_, _, err = ds.profileSvc.Create(ctx, provider.Profile{Alias: "rc-prep-chat", Kind: provider.ProfileKindChat, ConnectionRef: "rc-api", Model: prepModel})
	require.NoError(t, err)
	connSource := string(runner.MakeSourceID(runner.BuiltInRunnerID, runner.CredServerConnection, "rc-api"))

	// (a)+(b) agent_call and tool_call: one completed row per node on builtin.
	t.Run("agent_and_tool_call", func(t *testing.T) {
		id, _, err := ds.eng.Trigger(ctx, "rc-work", map[string]any{}, "normal")
		require.NoError(t, err)
		require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, id).State)

		jobs := rwJobs(t, ds.db, id)
		assert.True(t, rwEveryOpPlacedOnBuiltin(jobs, map[string]string{
			"work": string(runner.OpAgent), "tool": string(runner.OpProcess),
		}), "%+v", jobs)
		for _, j := range jobs {
			switch j.NodeID {
			case "work":
				assert.Equal(t, string(rwAnthropic), j.SourceID, "the claude-code agent spends the subscription source")
			case "tool":
				assert.Empty(t, j.SourceID, "process assignments reserve a slot without a credential source")
			}
		}
		assert.Equal(t, 1, rwCalls(fake), "one harness call for one agent_call")

		byNode := rcExecs(t, ds, id)
		require.Len(t, byNode["work"], 1)
		require.Len(t, byNode["tool"], 1)
		assert.Equal(t, map[string]any{"verdict": "pass", "score": float64(3)}, byNode["work"][0].Output, "the agent summary is parsed into a typed output")
		assert.Equal(t, map[string]any{"count": float64(2), "ok": true}, byNode["tool"][0].Output, "the tool stdout is parsed into a typed output")
		for _, n := range []string{"work", "tool"} {
			require.NotNil(t, byNode[n][0].RunnerID, "%s trace names its placement", n)
			assert.Equal(t, string(runner.BuiltInRunnerID), *byNode[n][0].RunnerID, n)
		}
	})

	// (c) one unmapped required child input: exactly one chat preparation job.
	t.Run("unmapped_child_input_prepared_on_pool", func(t *testing.T) {
		before := upstreamCalls.Load()
		id, _, err := ds.eng.Trigger(ctx, "rc-prep", map[string]any{"branch": "feat/x"}, "normal")
		require.NoError(t, err)
		require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, id).State)

		jobs := rwJobs(t, ds.db, id)
		assert.True(t, rwEveryOpPlacedOnBuiltin(jobs, map[string]string{"spawn": string(runner.OpChat)}), "%+v", jobs)
		if assert.Len(t, jobs, 1) {
			assert.Equal(t, connSource, jobs[0].SourceID, "preparation spends the profile's connection source")
		}
		assert.Equal(t, int32(1), upstreamCalls.Load()-before, "exactly one upstream call for the one unmapped input")

		children := childrenOf(t, ds, id)
		require.Len(t, children, 1)
		child, err := ds.instanceRepo.Get(ctx, children[0].ID)
		require.NoError(t, err)
		assert.Equal(t, "feat/x", child.Variables["branch"])
		assert.Equal(t, 7.0, child.Variables["n"], "the child receives the prepared, typed value")

		spawns := rcExecs(t, ds, id)["spawn"]
		require.Len(t, spawns, 2, "an awaited spawn records its suspension and its resumption")
		childOutput, _ := spawns[1].Output["childOutput"].(map[string]any)
		assert.Equal(t, 7.0, childOutput["n"], "the prepared value round-trips typed through childOutput")
		assert.Equal(t, map[string]any{"branch": "mapped", "n": "prepared"}, spawns[0].Output["inputSources"], "first spawn execution carries the trace-only input sources")
		require.NotNil(t, spawns[0].RunnerID, "the preparation placement is on the spawn trace")
		assert.Equal(t, string(runner.BuiltInRunnerID), *spawns[0].RunnerID)
	})

	// (c) the same preparation on a fire-and-forget spawn, whose execution record
	// is written by the engine's generic attempt path.
	t.Run("unmapped_child_input_prepared_fire_and_forget", func(t *testing.T) {
		before := upstreamCalls.Load()
		id, _, err := ds.eng.Trigger(ctx, "rc-prep-ff", map[string]any{"branch": "feat/z"}, "normal")
		require.NoError(t, err)
		require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, id).State)

		jobs := rwJobs(t, ds.db, id)
		assert.True(t, rwEveryOpPlacedOnBuiltin(jobs, map[string]string{"spawn": string(runner.OpChat)}), "%+v", jobs)
		assert.Equal(t, int32(1), upstreamCalls.Load()-before)
		children := childrenOf(t, ds, id)
		require.Len(t, children, 1)
		waitForState(t, ds, children[0].ID, instance.StateCompleted)
		child, err := ds.instanceRepo.Get(ctx, children[0].ID)
		require.NoError(t, err)
		assert.Equal(t, 7.0, child.Variables["n"])
		spawns := rcExecs(t, ds, id)["spawn"]
		require.Len(t, spawns, 1)
		assert.Equal(t, map[string]any{"branch": "mapped", "n": "prepared"}, spawns[0].Output["inputSources"])
		require.NotNil(t, spawns[0].RunnerID, "the preparation placement is on the spawn trace")
		assert.Equal(t, string(runner.BuiltInRunnerID), *spawns[0].RunnerID)
		require.NotNil(t, spawns[0].SourceID)
		assert.Equal(t, connSource, *spawns[0].SourceID)
	})

	// (c) fully mapped: zero preparation jobs and zero upstream calls.
	t.Run("fully_mapped_spawn_makes_no_preparation", func(t *testing.T) {
		before := upstreamCalls.Load()
		id, _, err := ds.eng.Trigger(ctx, "rc-mapped", map[string]any{"branch": "feat/y"}, "normal")
		require.NoError(t, err)
		require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, id).State)

		assert.Empty(t, rwJobs(t, ds.db, id), "a fully mapped spawn places no preparation job")
		assert.Equal(t, int32(0), upstreamCalls.Load()-before, "a fully mapped spawn makes zero upstream calls")
		children := childrenOf(t, ds, id)
		require.Len(t, children, 1)
		child, err := ds.instanceRepo.Get(ctx, children[0].ID)
		require.NoError(t, err)
		assert.Equal(t, 3.0, child.Variables["n"])
		spawns := rcExecs(t, ds, id)["spawn"]
		require.NotEmpty(t, spawns)
		assert.Equal(t, map[string]any{"branch": "mapped", "n": "mapped"}, spawns[0].Output["inputSources"])
	})
}
