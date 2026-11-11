package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const acceptanceFixtureKey = "lr_acceptance_private_fixture"

func TestLunaRouteAcceptanceEvidence_TriggerSchema(t *testing.T) {
	id := uuid.New()
	got, err := parseAcceptanceTrigger(`{"id":"` + id.String() + `","queued":false}`)
	require.NoError(t, err)
	assert.Equal(t, id, got)
	for _, raw := range []string{
		`{"message":"` + id.String() + `"}`, `{"id":"wrong","queued":true}`,
		`{"id":"00000000-0000-0000-0000-000000000000","queued":false}`,
		`{"id":"` + id.String() + `"}`, `{"id":"` + id.String() + `","queued":"yes"}`,
		`{"id":"wrong","id":"` + id.String() + `","queued":false}`,
		`{"id":"` + id.String() + `","queued":false} {}`, `broken ` + acceptanceFixtureKey,
	} {
		got, err := parseAcceptanceTrigger(raw)
		require.Error(t, err)
		assert.Equal(t, uuid.Nil, got)
		assert.NotContains(t, err.Error(), acceptanceFixtureKey)
	}
}

func TestLunaRouteAcceptanceEvidence_Polling(t *testing.T) {
	id := uuid.New()
	calls := 0
	got, err := pollAcceptance(context.Background(), id, time.Second, time.Millisecond, func(ctx context.Context) (*instance.Instance, error) {
		require.NoError(t, ctx.Err())
		calls++
		state := instance.StateRunning
		if calls == 2 {
			state = instance.StateCompleted
		}
		return &instance.Instance{ID: id, State: state}, nil
	})
	require.NoError(t, err)
	assert.Equal(t, instance.StateCompleted, got.State)
	assert.Equal(t, 2, calls)
	for _, state := range []instance.State{instance.StateFailed, instance.StateCancelled} {
		_, err := pollAcceptance(context.Background(), id, time.Second, time.Millisecond, func(context.Context) (*instance.Instance, error) {
			return &instance.Instance{ID: id, State: state}, nil
		})
		require.Error(t, err)
	}
	_, err = pollAcceptance(context.Background(), id, time.Second, time.Millisecond, func(context.Context) (*instance.Instance, error) {
		return &instance.Instance{ID: uuid.New(), State: instance.StateCompleted}, nil
	})
	require.Error(t, err, "a different run cannot satisfy polling")
	_, err = pollAcceptance(context.Background(), id, time.Second, time.Millisecond, func(context.Context) (*instance.Instance, error) { return nil, nil })
	require.Error(t, err)
	for _, limit := range []time.Duration{0, -time.Second, 16 * time.Minute} {
		_, err = pollAcceptance(context.Background(), id, limit, time.Millisecond, func(context.Context) (*instance.Instance, error) {
			t.Fatal("invalid budget fetched a run")
			return nil, nil
		})
		require.Error(t, err)
	}
	_, err = pollAcceptance(context.Background(), id, time.Second, time.Millisecond, func(context.Context) (*instance.Instance, error) {
		return nil, errors.New("transport echoed " + acceptanceFixtureKey)
	})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), acceptanceFixtureKey)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = pollAcceptance(cancelled, id, time.Second, time.Millisecond, func(context.Context) (*instance.Instance, error) {
		t.Fatal("cancelled polling fetched a run")
		return nil, nil
	})
	require.Error(t, err)
	start := time.Now()
	_, err = pollAcceptance(context.Background(), id, 20*time.Millisecond, time.Millisecond, func(ctx context.Context) (*instance.Instance, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	require.Error(t, err)
	assert.Less(t, time.Since(start), time.Second)
}

func acceptanceNodeFixture(t *testing.T, backend, model, reported string) acceptanceNodeInput {
	t.Helper()
	id := uuid.New()
	start := time.Now().Add(-time.Second)
	end := time.Now().Add(time.Second)
	nonce := "acceptance-nonce-" + id.String()
	ws := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(ws, "lunaroute-ok.txt"), []byte(nonce), 0600))
	// A real command derives the second artifact from the first. The node
	// trace remains a parser fixture, not evidence of an installed agent run.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", "wc -c < lunaroute-ok.txt > listing.txt")
	cmd.Dir = ws
	require.NoError(t, cmd.Run())
	source := map[string]string{"codex": "codex_turn_context", "pi": "pi_message_end", "claude-code": "claude_model_usage", "opencode": "opencode_export"}[backend]
	return acceptanceNodeInput{RunID: id, Backend: backend, Model: model, ExpectedOutput: "done", Nonce: nonce, Workspace: ws,
		Node: instance.NodeExecution{InstanceID: id, NodeID: "work", Status: instance.NodeStatusSucceeded, Output: map[string]any{"response": "done"}, LLMModel: &model, StartedAt: start, CompletedAt: &end,
			LLMConversation: map[string]any{"steps": []engine.AgentStep{{Kind: "tool_call", Summary: "write proof"}, {Kind: "tool_call", Summary: "derive proof"}}, "model_evidence": []engine.ModelEvidence{{Model: reported, Source: source}}}},
	}
}

func TestLunaRouteAcceptanceEvidence_NodeAndWorkspace(t *testing.T) {
	for _, backend := range []string{"claude-code", "codex", "opencode", "pi"} {
		t.Run(backend, func(t *testing.T) {
			in := acceptanceNodeFixture(t, backend, "glm-5.3", "glm-5.3")
			proof, err := collectAcceptanceNode(in, acceptanceFixtureKey)
			require.NoError(t, err)
			assert.Equal(t, in.RunID, proof.RunID)
			require.Len(t, proof.Models, 1)
			assert.Equal(t, "glm-5.3", proof.Models[0].Model)
			assert.True(t, proof.FileOK && proof.CommandOK)
		})
	}
	for _, selected := range []string{"claude-sonnet-5-lr1", "glm-999.9", "deepseek-4.1-flash-1017"} {
		in := acceptanceNodeFixture(t, "pi", selected, selected)
		proof, err := collectAcceptanceNode(in, acceptanceFixtureKey)
		require.Error(t, err, "an exact echo cannot authorize an unreviewed or revoked selection")
		assert.Empty(t, proof.Models)
	}
}

func TestLunaRouteAcceptanceEvidence_SQLiteTimestampPrecision(t *testing.T) {
	in := acceptanceNodeFixture(t, "pi", "glm-5.3", "glm-5.3")
	// SQLite stores node timestamps at second precision. A fast real task can
	// start and finish in the same second; both workspace mtimes lie inside it.
	stamp := time.Now().Truncate(time.Second)
	in.Node.StartedAt, in.Node.CompletedAt = stamp, &stamp
	for _, name := range []string{"lunaroute-ok.txt", "listing.txt"} {
		mtime := stamp.Add(500 * time.Millisecond)
		require.NoError(t, os.Chtimes(filepath.Join(in.Workspace, name), mtime, mtime))
	}
	_, err := collectAcceptanceNode(in, acceptanceFixtureKey)
	require.NoError(t, err)
	late := stamp.Add(time.Second)
	require.NoError(t, os.Chtimes(filepath.Join(in.Workspace, "listing.txt"), late, late))
	_, err = collectAcceptanceNode(in, acceptanceFixtureKey)
	require.Error(t, err, "the timestamp allowance is strictly bounded to the stored second")
}

func TestLunaRouteAcceptanceEvidence_RefusesFalseCompletion(t *testing.T) {
	cases := map[string]func(*acceptanceNodeInput){
		"failed Pi turn":         func(in *acceptanceNodeInput) { in.Node.Status = instance.NodeStatusFailed },
		"saved profile only":     func(in *acceptanceNodeInput) { in.Node = instance.NodeExecution{} },
		"missing terminal":       func(in *acceptanceNodeInput) { in.Node.CompletedAt = nil },
		"wrong run":              func(in *acceptanceNodeInput) { in.Node.InstanceID = uuid.New() },
		"wrong output":           func(in *acceptanceNodeInput) { in.Node.Output["response"] = "not done" },
		"wrong configured model": func(in *acceptanceNodeInput) { m := "glm-5.4"; in.Node.LLMModel = &m },
		"no tool trace":          func(in *acceptanceNodeInput) { delete(in.Node.LLMConversation, "steps") },
		"no reported models":     func(in *acceptanceNodeInput) { delete(in.Node.LLMConversation, "model_evidence") },
		"foreign model": func(in *acceptanceNodeInput) {
			in.Node.LLMConversation["model_evidence"] = []engine.ModelEvidence{{Model: "gpt-6.1", Source: "pi_message_end"}}
		},
		"foreign provider": func(in *acceptanceNodeInput) {
			in.Node.LLMConversation["model_evidence"] = []engine.ModelEvidence{{Model: "glm-5.3", Provider: "openai", Source: "pi_message_end"}}
		},
		"wrong source": func(in *acceptanceNodeInput) {
			in.Node.LLMConversation["model_evidence"] = []engine.ModelEvidence{{Model: "glm-5.3", Source: "argv"}}
		},
		"credential model": func(in *acceptanceNodeInput) {
			in.Node.LLMConversation["model_evidence"] = []engine.ModelEvidence{{Model: "claude-" + acceptanceFixtureKey, Source: "pi_message_end"}}
		},
		"malformed trace": func(in *acceptanceNodeInput) { in.Node.LLMConversation["steps"] = "broken" },
		"missing file": func(in *acceptanceNodeInput) {
			require.NoError(t, os.Remove(filepath.Join(in.Workspace, "lunaroute-ok.txt")))
		},
		"wrong nonce": func(in *acceptanceNodeInput) { in.Nonce = "different" },
		"wrong derived artifact": func(in *acceptanceNodeInput) {
			require.NoError(t, os.WriteFile(filepath.Join(in.Workspace, "listing.txt"), []byte("0\n"), 0600))
		},
		"symlink file": func(in *acceptanceNodeInput) {
			p := filepath.Join(in.Workspace, "lunaroute-ok.txt")
			require.NoError(t, os.Rename(p, p+"-outside"))
			require.NoError(t, os.Symlink(p+"-outside", p))
		},
		"unreadable file": func(in *acceptanceNodeInput) {
			require.NoError(t, os.Chmod(filepath.Join(in.Workspace, "listing.txt"), 0000))
		},
		"stale file": func(in *acceptanceNodeInput) {
			old := in.Node.StartedAt.Add(-time.Hour)
			require.NoError(t, os.Chtimes(filepath.Join(in.Workspace, "listing.txt"), old, old))
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			in := acceptanceNodeFixture(t, "pi", "glm-5.3", "glm-5.3")
			change(&in)
			proof, err := collectAcceptanceNode(in, acceptanceFixtureKey)
			require.Error(t, err)
			assert.Equal(t, uuid.Nil, proof.RunID)
			assert.NotContains(t, err.Error(), acceptanceFixtureKey)
		})
	}
}

func TestLunaRouteAcceptanceEvidence_UnsafeAliasIdentities(t *testing.T) {
	for _, reported := range []string{"claude-" + acceptanceFixtureKey, "claude-sonnet-5-6 ", "claude-sonnet-5-6\x00", "claude-" + strings.Repeat("a", 201)} {
		in := acceptanceNodeFixture(t, "pi", "claude-sonnet-5-lr1", reported)
		proof, err := collectAcceptanceNode(in, acceptanceFixtureKey)
		require.Error(t, err)
		assert.Empty(t, proof.Models, "untrusted vendor-shaped identity must not reach the proof sink")
		assert.NotContains(t, err.Error(), acceptanceFixtureKey)
	}
}

func TestLunaRouteAcceptanceEvidence_LogReadsAreNotVacuous(t *testing.T) {
	p := filepath.Join(t.TempDir(), "server.log")
	require.Error(t, checkAcceptanceLog(p, acceptanceFixtureKey))
	for _, body := range []string{"safe diagnostic\n", ""} {
		require.NoError(t, os.WriteFile(p, []byte(body), 0600))
		require.NoError(t, checkAcceptanceLog(p, acceptanceFixtureKey))
	}
	for _, body := range []string{acceptanceFixtureKey, strings.Repeat("x", 4096) + acceptanceFixtureKey, `{"note":"\u006cr_acceptance_private_fixture"}`, "Bearer foreign-token", "sk-foreign-token"} {
		require.NoError(t, os.WriteFile(p, []byte(body), 0600))
		err := checkAcceptanceLog(p, acceptanceFixtureKey)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), acceptanceFixtureKey)
		assert.NotContains(t, err.Error(), "foreign-token")
	}
	require.NoError(t, os.Chmod(p, 0000))
	require.Error(t, checkAcceptanceLog(p, acceptanceFixtureKey))
	require.NoError(t, os.Chmod(p, 0600))
	require.NoError(t, os.WriteFile(p, []byte(strings.Repeat("x", acceptanceReadLimit+1)), 0600))
	require.Error(t, checkAcceptanceLog(p, acceptanceFixtureKey))
}

func TestLunaRouteAcceptanceEvidence_ChatUsesResponseModel(t *testing.T) {
	in := acceptanceNodeFixture(t, "pi", "glm-5.3", "glm-5.3")
	in.Backend = ""
	in.ExpectedOutput = "PONG"
	in.Node.Output["response"] = "PONG"
	in.Node.LLMConversation = nil
	proof, err := collectAcceptanceNode(in, acceptanceFixtureKey)
	require.NoError(t, err)
	assert.False(t, proof.FileOK || proof.CommandOK)
	require.Len(t, proof.Models, 1)
	assert.Equal(t, "chat_response", proof.Models[0].Source)
	for _, reported := range []string{"glm-5.3-flash", "claude-sonnet-5-lr1", "claude-sonnet-5-6", ""} {
		in.Node.LLMModel = &reported
		proof, err = collectAcceptanceNode(in, acceptanceFixtureKey)
		require.Error(t, err, "response evidence must equal the reviewed selection, not merely its family")
		assert.Empty(t, proof.Models)
	}
	in.Model = "claude-sonnet-5-lr1"
	reported := in.Model
	in.Node.LLMModel = &reported
	proof, err = collectAcceptanceNode(in, acceptanceFixtureKey)
	require.Error(t, err, "revoked selections cannot be authorized by a matching response")
	assert.Empty(t, proof.Models)
	in.Node.LLMModel = nil
	_, err = collectAcceptanceNode(in, acceptanceFixtureKey)
	require.Error(t, err)
}

// Offline production seam: the actual registered MCP handlers, capability
// loader, engine and HTTP adapter feed a persisted node into the collector.
// This test does not authorize live release acceptance or agent enablement.
func TestLunaRouteAcceptanceEvidence_RealMCPChatSink(t *testing.T) {
	for _, model := range []string{"glm-5.3", "glm-5.3-flash", "deepseek-4.1-flash"} {
		t.Run(model, func(t *testing.T) { testLunaRouteAcceptanceChatSink(t, model) })
	}
}

func testLunaRouteAcceptanceChatSink(t *testing.T, model string) {
	t.Helper()
	gw := newFakeLunaRouteGateway(t, acceptanceFixtureKey, model)
	gw.ExpectChat(model, lrRootPrompt, 1234, "")
	gw.Reply("POST /v1/chat/completions", lunaRouteGatewayReply{Status: 200, Body: fmt.Sprintf(`{"model":%q,"choices":[{"message":{"content":"PONG"},"finish_reason":"stop"}]}`, model)})
	caps := fmt.Sprintf("lunaroute_models:\n  %s:\n    tasks:\n      chat: {support: supported, evidence: 'ev#chat'}\n    output_tokens: {tokens: 32768, evidence: 'ev#out'}\n", model)
	ds := bootLunaRouteRoot(t, "capabilitiespath: "+lunaRouteCapsFile(t, caps)+"\n", gw, func(ws string) {
		writeLunaRouteChatWorkflow(t, ws, "acceptance-chat", "acceptance-chat", 1234, false)
	})
	call := lunaRouteToolCaller(t, ds)
	isErr, text := call("connection_create", map[string]any{"name": "lr", "provider": "lunaroute", "api_key": acceptanceFixtureKey})
	require.False(t, isErr, "connection_create failed")
	assert.NotContains(t, text, acceptanceFixtureKey)
	isErr, text = call("connection_models", map[string]any{"name": "lr"})
	require.False(t, isErr, "connection_models failed")
	var discovery provider.DiscoveryView
	require.NoError(t, json.Unmarshal([]byte(text), &discovery))
	require.Len(t, discovery.Models, 1)
	assert.Equal(t, model, discovery.Models[0].ID)
	assert.Equal(t, provider.FactView{Support: "supported", Evidence: "ev#chat"}, discovery.Models[0].Tasks["chat"])
	isErr, text = call("profile_create", map[string]any{"alias": "acceptance-chat", "kind": "chat", "connection_ref": "lr", "model": model})
	require.False(t, isErr, "profile_create failed")
	id := triggerViaMCP(t, call, "acceptance-chat")
	inst := waitLunaRouteTerminal(t, ds, id)
	assert.Equal(t, map[string]string{"acceptance-chat": model}, inst.ModelPins)
	assert.Equal(t, instance.ModelPinsVersionSnapshot, inst.ModelPinsVersion)
	node, err := ds.nodeExecRepo.GetByInstanceAndNode(adminCtxForLocalUser(t, ds), id, "ask")
	require.NoError(t, err)
	require.NotNil(t, node)
	proof, err := collectAcceptanceNode(acceptanceNodeInput{RunID: id, Model: model, ExpectedOutput: "PONG", Node: *node}, acceptanceFixtureKey)
	require.NoError(t, err)
	require.Len(t, proof.Models, 1)
	assert.Equal(t, model, proof.Models[0].Model)
	require.NoError(t, checkAcceptanceLog(ds.cfg.Logging.File, acceptanceFixtureKey), "actual server log must be readable and safe")
	reqs := gw.Requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, "https://gw.lunaroute.com/v1/models", reqs[0].URL)
	assert.Equal(t, "https://gw.lunaroute.com/v1/chat/completions", reqs[1].URL)
	for _, r := range reqs {
		assert.Equal(t, "Bearer "+acceptanceFixtureKey, r.Authorization)
	}
	// Removing the response-model producer must invalidate the final proof,
	// even though output, profile and pins still look correct.
	node.LLMModel = nil
	_, err = collectAcceptanceNode(acceptanceNodeInput{RunID: id, Model: model, ExpectedOutput: "PONG", Node: *node}, acceptanceFixtureKey)
	require.Error(t, err)
}

func TestLunaRouteAcceptanceEvidence_JSONTraceRoundTrip(t *testing.T) {
	in := acceptanceNodeFixture(t, "codex", "glm-5.3", "glm-5.3")
	raw, err := json.Marshal(in.Node.LLMConversation)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &in.Node.LLMConversation))
	proof, err := collectAcceptanceNode(in, acceptanceFixtureKey)
	require.NoError(t, err)
	assert.Equal(t, "codex_turn_context", proof.Models[0].Source, "harness intent is not gateway response evidence")
}
