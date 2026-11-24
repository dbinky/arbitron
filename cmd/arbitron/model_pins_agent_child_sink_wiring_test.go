package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClaudeBinary writes a stand-in for the `claude` CLI that records every
// invocation's argv (one line per call) to argvLog and, on its FIRST call only,
// blocks until goFile exists. It then answers with a minimal stream-json result.
// The argv is the agent consumer's sink: adapter/claudecli buildArgs `--model`.
func fakeClaudeBinary(t *testing.T, dir, argvLog, goFile string) string {
	t.Helper()
	bin := filepath.Join(dir, "fake-claude.sh")
	script := "#!/bin/sh\n" +
		"case \"$1\" in --version) echo '2.1.330 (Claude Code)'; exit 0 ;; auth) echo '{\"loggedIn\":true}'; exit 0 ;; esac\n" +
		"echo \"$*\" >> '" + argvLog + "'\n" +
		"n=$(wc -l < '" + argvLog + "' | tr -d ' ')\n" +
		"if [ \"$n\" -eq 1 ]; then while [ ! -f '" + goFile + "' ]; do sleep 0.05; done; fi\n" +
		"cat > /dev/null\n" +
		// Like the real CLI, report the model that served the turn: the requested
		// --model, unless an "executed-model" file simulates a silent fallback.
		"m=$(echo \"$*\" | sed -n 's/.*--model \\([^ ]*\\).*/\\1/p')\n" +
		"if [ -f '" + filepath.Join(dir, "executed-model") + "' ]; then m=$(cat '" + filepath.Join(dir, "executed-model") + "'); fi\n" +
		"echo '{\"type\":\"result\",\"is_error\":false,\"result\":\"ok\",\"session_id\":\"s\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1},\"modelUsage\":{\"'\"$m\"'\":{}}}'\n"
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o700))
	return bin
}

func readArgvLog(t *testing.T, argvLog string) []string {
	t.Helper()
	b, err := os.ReadFile(argvLog)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func waitArgvLines(t *testing.T, argvLog string, n int) {
	t.Helper()
	require.Eventually(t, func() bool {
		return len(readArgvLog(t, argvLog)) >= n
	}, 20*time.Second, 25*time.Millisecond, "the harness was never invoked %d time(s)", n)
}

// TestModelPinsWiringAgentCallSink drives the real initDomain end to end for the
// agent_call consumer: the `claude` argv of a run triggered on X still carries
// `--model X` for its second node after the live agent profile is edited to Y,
// while a fresh run carries `--model Y`. Replacing the PinningResolver injected
// into NewAgentCallExecutor with the live resolver makes the old run's second
// call carry Y and fails the test.
func TestModelPinsWiringAgentCallSink(t *testing.T) {
	scratch := t.TempDir()
	argvLog := filepath.Join(scratch, "argv.log")
	goFile := filepath.Join(scratch, "go")
	bin := fakeClaudeBinary(t, scratch, argvLog, goFile)

	ds := bootSinkRoot(t, "llm:\n  binarypath: \""+bin+"\"\n", func(workspaceDir string) {
		writeWorkflow(t, workspaceDir, "pin-agent", `{
  "name": "pin-agent",
  "description": "two agent calls",
  "inputs": [],
  "nodes": [
    {"id": "work1", "type": "agent_call", "entrypoint": true,
     "config": {"model_profile": "agent-alias", "templateFile": "templates/p.md"}},
    {"id": "work2", "type": "agent_call",
     "config": {"model_profile": "agent-alias", "templateFile": "templates/p.md"}}
  ],
  "edges": [{"source": "work1", "target": "work2"}]
}`)
		writePinTemplate(t, workspaceDir, "pin-agent")
	})
	ctx := adminCtxForLocalUser(t, ds)

	agent := func(model string) provider.Profile {
		return provider.Profile{Alias: "agent-alias", Kind: provider.ProfileKindAgent, Backend: "claude-code", Model: model}
	}
	_, _, err := ds.profileSvc.Create(ctx, agent("claude-opus-4-1"))
	require.NoError(t, err)

	id, _, err := ds.eng.Trigger(ctx, "pin-agent", map[string]any{}, "normal")
	require.NoError(t, err)
	waitArgvLines(t, argvLog, 1) // first node is in the harness, blocked
	_, _, err = ds.profileSvc.Update(ctx, agent("claude-opus-4-5"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(goFile, nil, 0o600))

	inst := waitTerminal(t, ds, id)
	if inst.State != instance.StateCompleted {
		var nerr string
		_ = ds.db.QueryRow(`SELECT COALESCE(error,'') FROM node_executions WHERE instance_id = ? AND error IS NOT NULL`, id.String()).Scan(&nerr)
		t.Fatalf("run ended %s: node error: %s", inst.State, nerr)
	}
	argv := readArgvLog(t, argvLog)
	require.Len(t, argv, 2)
	for i, line := range argv {
		assert.Contains(t, line, "--model claude-opus-4-1", "call %d of the old run must carry the pinned model: %s", i+1, line)
		assert.NotContains(t, line, "claude-opus-4-5", "call %d of the old run leaked the live edit: %s", i+1, line)
	}

	id2, _, err := ds.eng.Trigger(ctx, "pin-agent", map[string]any{}, "normal")
	require.NoError(t, err)
	require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, id2).State)
	fresh := readArgvLog(t, argvLog)[2:]
	require.Len(t, fresh, 2)
	for _, line := range fresh {
		assert.Contains(t, line, "--model claude-opus-4-5", "a fresh run pins the edited model: %s", line)
	}
}

// TestModelPinsWiringChildInputSink covers the third consumer: the parent's
// spawn_workflow child-input preparation chat call. The parent runs an llm_call
// on alias X (blocked at the sink), the live profile is edited to Y, and the
// preparer's HTTP request body — issued afterwards, under the PARENT's pins —
// must still name X. A fresh parent run names Y in both. Replacing the
// PinningResolver in NewChildInputPreparer with the live resolver makes the old
// run's second body name Y and fails the test.
func TestModelPinsWiringChildInputSink(t *testing.T) {
	ds := bootSinkRoot(t, "", func(workspaceDir string) {
		writeWorkflow(t, workspaceDir, "pin-child", `{
  "name": "pin-child",
  "description": "child with one input",
  "inputs": [{"name": "topic", "type": "string", "description": "topic", "required": true}],
  "nodes": [
    {"id": "c1", "type": "transform", "entrypoint": true,
     "config": {"operations": [{"variable": "done", "operation": "set", "value": true}]}}
  ],
  "edges": []
}`)
		writeWorkflow(t, workspaceDir, "pin-parent", `{
  "name": "pin-parent",
  "description": "llm call then spawn with prepared inputs",
  "inputs": [],
  "nodes": [
    {"id": "ask", "type": "llm_call", "entrypoint": true,
     "config": {"model_profile": "pin-alias", "templateFile": "templates/p.md"}},
    {"id": "spawn-1", "type": "spawn_workflow",
     "config": {"childWorkflow": "pin-child", "spawnMode": "fire_and_forget", "model_profile": "pin-alias"}}
  ],
  "edges": [{"source": "ask", "target": "spawn-1"}]
}`)
		writePinTemplate(t, workspaceDir, "pin-parent")
	})
	sink := newModelSink(t)
	sink.mu.Lock()
	sink.content = `{"topic":"x"}`
	sink.mu.Unlock()
	ctx := adminCtxForLocalUser(t, ds)

	crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
	require.NoError(t, err)
	fixtureConns := adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy)
	require.NoError(t, fixtureConns.Create(ctx, provider.Connection{Name: "sink", Provider: "openrouter", BaseURL: sink.srv.URL}, "k"))
	chat := func(model string) provider.Profile {
		return provider.Profile{Alias: "pin-alias", Kind: provider.ProfileKindChat, ConnectionRef: "sink", Model: model}
	}
	_, _, err = ds.profileSvc.Create(ctx, chat("deepseek/deepseek-v4-flash"))
	require.NoError(t, err)

	gate := make(chan struct{})
	sink.mu.Lock()
	sink.release = gate
	sink.mu.Unlock()
	id, _, err := ds.eng.Trigger(ctx, "pin-parent", map[string]any{}, "normal")
	require.NoError(t, err)
	select {
	case <-sink.arrived:
	case <-time.After(20 * time.Second):
		t.Fatal("parent llm_call never reached the sink")
	}
	_, _, err = ds.profileSvc.Update(ctx, chat("deepseek/deepseek-v4.1-flash"))
	require.NoError(t, err)
	close(gate)

	inst := waitTerminal(t, ds, id)
	if inst.State != instance.StateCompleted {
		var nerr string
		_ = ds.db.QueryRow(`SELECT COALESCE(error,'') FROM node_executions WHERE instance_id = ? AND error IS NOT NULL`, id.String()).Scan(&nerr)
		t.Fatalf("run ended %s: node error: %s", inst.State, nerr)
	}
	assert.Equal(t, []string{"deepseek/deepseek-v4-flash", "deepseek/deepseek-v4-flash"}, sink.seen(),
		"the old run's child-input preparation must send the parent's pinned model")
	require.Len(t, childrenOf(t, ds, id), 1, "the preparer's reply must have produced the spawned child")

	sink.mu.Lock()
	sink.release = nil
	sink.mu.Unlock()
	id2, _, err := ds.eng.Trigger(ctx, "pin-parent", map[string]any{}, "normal")
	require.NoError(t, err)
	require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, id2).State)
	assert.Equal(t, []string{"deepseek/deepseek-v4.1-flash", "deepseek/deepseek-v4.1-flash"}, sink.seen()[2:])
}
