package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClaudeGatedEnvBinary logs every call's argv plus the injected connection
// env. Call n blocks until dir/go-n exists when dir/gate-n exists, and exits 1
// once when dir/fail-n exists, so a test can edit the profile between calls and
// force a node retry.
func fakeClaudeGatedEnvBinary(t *testing.T, dir, argvLog string) string {
	t.Helper()
	bin := filepath.Join(dir, "fake-claude-gated.sh")
	script := "#!/bin/sh\n" +
		"echo \"$* BASE=$ANTHROPIC_BASE_URL KEY=$ANTHROPIC_API_KEY\" >> '" + argvLog + "'\n" +
		"n=$(wc -l < '" + argvLog + "' | tr -d ' ')\n" +
		"if [ -f '" + dir + "'/gate-$n ]; then while [ ! -f '" + dir + "'/go-$n ]; do sleep 0.05; done; fi\n" +
		"cat > /dev/null\n" +
		"if [ -f '" + dir + "'/fail-$n ]; then echo 'transient failure' >&2; exit 1; fi\n" +
		"m=$(echo \"$*\" | sed -n 's/.*--model \\([^ ]*\\).*/\\1/p')\n" +
		"echo '{\"type\":\"result\",\"is_error\":false,\"result\":\"ok\",\"session_id\":\"s\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1},\"modelUsage\":{\"'\"$m\"'\":{}}}'\n"
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o700))
	return bin
}

func toolScopeOf(t *testing.T, line string) string {
	t.Helper()
	_, after, ok := strings.Cut(line, "--allowedTools ")
	require.True(t, ok, "no --allowedTools in %s", line)
	return strings.Fields(after)[0]
}

// TestModelPinsWiringBatchAgentAdminEditThroughMCP (paired:3, Area 3 ↔ Area 7):
// R1 is triggered on X with Read/Grep over conn-a. While its first agent_call
// is in the harness, MCP profile_update moves the alias to Y, tools Glob and
// conn-b. The next node, which fails once and is retried by errorBehavior, runs
// pinned X with Glob and conn-b's endpoint and key on both attempts. An explicit
// empty tool list then falls back to the node's toolScope and, without one, to
// the configured adapter scope (executor_agent.go precedence), exactly as for
// a fresh run R2, which runs Y.
func TestModelPinsWiringBatchAgentAdminEditThroughMCP(t *testing.T) {
	const modelX, modelY = "claude-opus-4-1", "claude-opus-4-5"
	scratch := t.TempDir()
	argvLog := filepath.Join(scratch, "argv.log")
	bin := fakeClaudeGatedEnvBinary(t, scratch, argvLog)
	touch := func(name string) {
		t.Helper()
		require.NoError(t, os.WriteFile(filepath.Join(scratch, name), nil, 0o600))
	}
	touch("gate-1")
	touch("fail-2")
	touch("gate-3")

	ds := bootSinkRoot(t, "llm:\n  binarypath: \""+bin+"\"\n  allowedtools: [Bash]\n", func(dir string) {
		writeWorkflow(t, dir, "pin-batch-admin", `{
  "name":"pin-batch-admin","inputs":[],"nodes":[
    {"id":"work1","type":"agent_call","entrypoint":true,
     "config":{"model_profile":"agent-alias","templateFile":"templates/p.md"}},
    {"id":"work2","type":"agent_call","errorBehavior":{"type":"retry","retries":1},
     "config":{"model_profile":"agent-alias","templateFile":"templates/p.md"}},
    {"id":"work3","type":"agent_call",
     "config":{"model_profile":"agent-alias","templateFile":"templates/p.md","toolScope":["LS"]}}
  ],"edges":[{"source":"work1","target":"work2"},{"source":"work2","target":"work3"}]}`)
		writePinTemplate(t, dir, "pin-batch-admin")
	})
	ctx := adminCtxForLocalUser(t, ds)
	crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
	require.NoError(t, err)
	conns := adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy)
	require.NoError(t, conns.Create(ctx, provider.Connection{Name: "conn-a", Provider: "anthropic", BaseURL: "https://a.gateway.test"}, "key-a"))
	require.NoError(t, conns.Create(ctx, provider.Connection{Name: "conn-b", Provider: "anthropic", BaseURL: "https://b.gateway.test"}, "key-b"))

	call := mcpToolCaller(t, ctx, ds)
	res, text := call("profile_create", map[string]any{
		"alias": "agent-alias", "kind": "agent", "backend": "claude-code",
		"connection_ref": "conn-a", "model": modelX, "allowed_tools": []any{"Read", "Grep"},
	})
	require.False(t, res.IsError, text)

	id, _, err := ds.eng.Trigger(ctx, "pin-batch-admin", map[string]any{}, "normal")
	require.NoError(t, err)
	waitArgvLines(t, argvLog, 1)
	res, text = call("profile_update", map[string]any{
		"alias": "agent-alias", "model": modelY, "allowed_tools": []any{"Glob"}, "connection_ref": "conn-b",
	})
	require.False(t, res.IsError, text)
	touch("go-1")

	waitArgvLines(t, argvLog, 3) // work2 failed once; its retry is in the harness
	res, text = call("profile_update", map[string]any{"alias": "agent-alias", "allowed_tools": []any{}})
	require.False(t, res.IsError, text)
	stored, err := ds.profileSvc.Get(ctx, "agent-alias")
	require.NoError(t, err)
	require.Empty(t, stored.AllowedTools)
	touch("go-3")

	inst := waitTerminal(t, ds, id)
	require.Equal(t, instance.StateCompleted, inst.State)
	assert.Equal(t, map[string]string{"agent-alias": modelX}, inst.ModelPins, "admin edits never rewrite the run's pin")
	argv := readArgvLog(t, argvLog)
	require.Len(t, argv, 4, "work1, work2 attempt, work2 retry, work3")

	first := argv[0]
	assert.Contains(t, first, "--model "+modelX)
	assert.Equal(t, "Read,Grep", toolScopeOf(t, first))
	assert.Contains(t, first, "BASE=https://a.gateway.test KEY=key-a")

	for i, line := range argv[1:3] {
		assert.Contains(t, line, "--model "+modelX, "work2 attempt %d keeps the pin", i+1)
		assert.NotContains(t, line, modelY)
		assert.Equal(t, "Glob", toolScopeOf(t, line), "work2 attempt %d uses the narrowed live scope", i+1)
		assert.Contains(t, line, "BASE=https://b.gateway.test KEY=key-b", "work2 attempt %d uses the edited connection", i+1)
		assert.NotContains(t, line, "a.gateway.test")
		assert.NotContains(t, line, "key-a")
	}

	cleared := argv[3]
	assert.Contains(t, cleared, "--model "+modelX, "the pin survives a tools-only edit")
	assert.Equal(t, "LS", toolScopeOf(t, cleared), "a cleared profile scope falls back to the node's toolScope")
	assert.Contains(t, cleared, "BASE=https://b.gateway.test KEY=key-b")

	id2, _, err := ds.eng.Trigger(ctx, "pin-batch-admin", map[string]any{}, "normal")
	require.NoError(t, err)
	inst2 := waitTerminal(t, ds, id2)
	require.Equal(t, instance.StateCompleted, inst2.State)
	assert.Equal(t, map[string]string{"agent-alias": modelY}, inst2.ModelPins)
	fresh := readArgvLog(t, argvLog)[4:]
	require.Len(t, fresh, 3)
	for _, line := range fresh {
		assert.Contains(t, line, "--model "+modelY, "a fresh run pins the edited model: %s", line)
		assert.Contains(t, line, "BASE=https://b.gateway.test KEY=key-b")
	}
	assert.Equal(t, "Bash", toolScopeOf(t, fresh[0]), "no profile or node scope: the configured adapter scope")
	assert.Equal(t, "Bash", toolScopeOf(t, fresh[1]))
	assert.Equal(t, "LS", toolScopeOf(t, fresh[2]))
}

// authSink is an OpenAI-compatible endpoint recording each request's model
// and bearer key; its first request blocks until release is closed when set.
type authSink struct {
	mu      sync.Mutex
	calls   []string // "model|key"
	srv     *httptest.Server
	arrived chan struct{}
	release chan struct{}
	gated   sync.Once
	content string
}

func newAuthSink(t *testing.T, content string) *authSink {
	t.Helper()
	s := &authSink{arrived: make(chan struct{}, 16), content: content}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		s.calls = append(s.calls, body.Model+"|"+strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		gate := s.release
		s.mu.Unlock()
		s.arrived <- struct{}{}
		if gate != nil {
			s.gated.Do(func() { <-gate })
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":%q},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`, body.Model, s.content)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *authSink) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// TestModelPinsWiringBatchChatAdminEditThroughMCP (paired:3) covers the chat
// consumers: R1's first llm_call is held at endpoint A; MCP profile_update
// moves the alias to Y on conn-b (a compatible gateway). R1's second llm_call
// and the spawn_workflow child-input preparation reach B with B's key and the
// pinned X; endpoint A sees nothing more. A fresh run sends Y to B.
func TestModelPinsWiringBatchChatAdminEditThroughMCP(t *testing.T) {
	const modelX, modelY = "deepseek/deepseek-v4-flash", "deepseek/deepseek-v4.1-flash"
	ds := bootSinkRoot(t, "", func(dir string) {
		writeWorkflow(t, dir, "pin-chat-child", `{
  "name":"pin-chat-child","inputs":[{"name":"topic","type":"string","description":"topic","required":true}],
  "nodes":[{"id":"c1","type":"transform","entrypoint":true,
    "config":{"operations":[{"variable":"done","operation":"set","value":true}]}}],"edges":[]}`)
		writeWorkflow(t, dir, "pin-chat-admin", `{
  "name":"pin-chat-admin","inputs":[],"nodes":[
    {"id":"ask1","type":"llm_call","entrypoint":true,
     "config":{"model_profile":"chat-alias","templateFile":"templates/p.md"}},
    {"id":"ask2","type":"llm_call",
     "config":{"model_profile":"chat-alias","templateFile":"templates/p.md"}},
    {"id":"spawn-1","type":"spawn_workflow",
     "config":{"childWorkflow":"pin-chat-child","spawnMode":"fire_and_forget","model_profile":"chat-alias"}}
  ],"edges":[{"source":"ask1","target":"ask2"},{"source":"ask2","target":"spawn-1"}]}`)
		writePinTemplate(t, dir, "pin-chat-admin")
	})
	ctx := adminCtxForLocalUser(t, ds)
	sinkA, sinkB := newAuthSink(t, `{"topic":"a"}`), newAuthSink(t, `{"topic":"b"}`)
	crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
	require.NoError(t, err)
	conns := adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy)
	require.NoError(t, conns.Create(ctx, provider.Connection{Name: "conn-a", Provider: "openrouter", BaseURL: sinkA.srv.URL}, "key-a"))
	require.NoError(t, conns.Create(ctx, provider.Connection{Name: "conn-b", Provider: "openrouter", BaseURL: sinkB.srv.URL}, "key-b"))

	call := mcpToolCaller(t, ctx, ds)
	res, text := call("profile_create", map[string]any{
		"alias": "chat-alias", "kind": "chat", "connection_ref": "conn-a", "model": modelX,
	})
	require.False(t, res.IsError, text)

	gate := make(chan struct{})
	sinkA.mu.Lock()
	sinkA.release = gate
	sinkA.mu.Unlock()
	id, _, err := ds.eng.Trigger(ctx, "pin-chat-admin", map[string]any{}, "normal")
	require.NoError(t, err)
	select {
	case <-sinkA.arrived:
	case <-time.After(20 * time.Second):
		t.Fatal("ask1 never reached endpoint A")
	}
	res, text = call("profile_update", map[string]any{"alias": "chat-alias", "model": modelY, "connection_ref": "conn-b"})
	require.False(t, res.IsError, text)
	close(gate)

	inst := waitTerminal(t, ds, id)
	if inst.State != instance.StateCompleted {
		var nerr string
		_ = ds.db.QueryRow(`SELECT COALESCE(error,'') FROM node_executions WHERE instance_id = ? AND error IS NOT NULL`, id.String()).Scan(&nerr)
		t.Fatalf("run ended %s: node error: %s", inst.State, nerr)
	}
	assert.Equal(t, map[string]string{"chat-alias": modelX}, inst.ModelPins)
	assert.Equal(t, []string{modelX + "|key-a"}, sinkA.seen(), "nothing after the edit reaches the superseded connection")
	assert.Equal(t, []string{modelX + "|key-b", modelX + "|key-b"}, sinkB.seen(),
		"ask2 and the child-input preparation use conn-b with the pinned model")
	require.Len(t, childrenOf(t, ds, id), 1, "the preparer's reply from B produced the spawned child")

	id2, _, err := ds.eng.Trigger(ctx, "pin-chat-admin", map[string]any{}, "normal")
	require.NoError(t, err)
	require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, id2).State)
	assert.Len(t, sinkA.seen(), 1)
	assert.Equal(t, []string{modelY + "|key-b", modelY + "|key-b", modelY + "|key-b"}, sinkB.seen()[2:])
}
