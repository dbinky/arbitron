package lunaroute_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Reduced v1.19.5 TaskTool/export envelopes, not installed-binary evidence.
func openCodeSubagentFixture() (map[string]any, map[string]any) {
	parent := map[string]any{"info": map[string]any{"id": "ses_parent"}, "messages": []any{map[string]any{
		"info": map[string]any{"id": "msg_parent", "sessionID": "ses_parent", "role": "assistant", "providerID": "lunaroute", "modelID": "glm-5.3"},
		"parts": []any{map[string]any{"id": "prt_task", "sessionID": "ses_parent", "messageID": "msg_parent", "type": "tool", "tool": "task", "state": map[string]any{
			"status": "completed", "input": map[string]any{"prompt": "private prompt"}, "output": "private response",
			"time":     map[string]any{"start": 1000, "end": 2000},
			"metadata": map[string]any{"parentSessionId": "ses_parent", "sessionId": "ses_child", "model": map[string]any{"providerID": "lunaroute", "modelID": "glm-5.3"}},
		}}},
	}}}
	child := map[string]any{"info": map[string]any{"id": "ses_child", "parentID": "ses_parent"}, "messages": []any{
		map[string]any{"info": map[string]any{"id": "msg_user", "sessionID": "ses_child", "role": "user", "time": map[string]any{"created": 1100}}, "parts": []any{}},
		map[string]any{"info": map[string]any{"id": "msg_child", "parentID": "msg_user", "sessionID": "ses_child", "role": "assistant", "providerID": "lunaroute", "modelID": "glm-5.3", "finish": "stop", "time": map[string]any{"created": 1200, "completed": 1900}}, "parts": []any{}},
	}}
	return parent, child
}
func subagentJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return raw
}
func subagentTaskState(parent map[string]any) map[string]any {
	return parent["messages"].([]any)[0].(map[string]any)["parts"].([]any)[0].(map[string]any)["state"].(map[string]any)
}
func subagentChildInfo(child map[string]any) map[string]any {
	return child["messages"].([]any)[1].(map[string]any)["info"].(map[string]any)
}

// Called by the authenticated inference fixture, not a lifecycle observation.
func writeFactorySubagentExports(root, mode string, started time.Time) error {
	parent, child := openCodeSubagentFixture()
	now := time.Now().UnixMilli()
	state := subagentTaskState(parent)
	state["time"] = map[string]any{"start": now - 3, "end": now}
	child["messages"].([]any)[0].(map[string]any)["info"].(map[string]any)["time"] = map[string]any{"created": now - 2}
	subagentChildInfo(child)["time"] = map[string]any{"created": now - 2, "completed": now - 1}
	parent["messages"].([]any)[0].(map[string]any)["info"].(map[string]any)["time"] = map[string]any{"created": now - 4, "completed": now + 1}
	if strings.HasPrefix(mode, "correlation-") {
		parent["info"].(map[string]any)["title"] = "Private fixture title"
		if mode == "correlation-title-drift" {
			parent["info"].(map[string]any)["title"] = "Unrelated exported title"
		}
		if mode == "correlation-title-secret" {
			parent["info"].(map[string]any)["title"] = captureFixtureKey
		}
		// The protocol executable waits until this synthetic task end before
		// exporting. Its main/child requests fall inside this fixture interval.
		state["time"] = map[string]any{"start": started.UnixMilli(), "end": now + 500}
		child["messages"].([]any)[0].(map[string]any)["info"].(map[string]any)["time"] = map[string]any{"created": started.UnixMilli()}
		subagentChildInfo(child)["time"] = map[string]any{"created": started.UnixMilli(), "completed": now}
	}
	switch mode {
	case "subagent-missing":
		parent["messages"].([]any)[0].(map[string]any)["parts"] = []any{}
	case "subagent-parent":
		child["info"].(map[string]any)["parentID"] = "ses_other"
	case "subagent-model":
		subagentChildInfo(child)["modelID"] = "glm-5.3-flash"
	case "subagent-error":
		subagentChildInfo(child)["error"] = map[string]any{"message": captureFixtureKey}
	case "subagent-stale":
		state["time"] = map[string]any{"start": now - 60003, "end": now - 60000}
		child["messages"].([]any)[0].(map[string]any)["info"].(map[string]any)["time"] = map[string]any{"created": now - 60002}
		subagentChildInfo(child)["time"] = map[string]any{"created": now - 60002, "completed": now - 60001}
	}
	if strings.HasPrefix(mode, "correlation-force-") {
		info := parent["messages"].([]any)[0].(map[string]any)["info"].(map[string]any)
		info["finish"] = "stop"
		info["tokens"] = map[string]any{"input": 1024, "output": 1024, "cache": map[string]any{"read": 0, "write": 0}}
		switch mode {
		case "correlation-force-usage-total":
			info["tokens"].(map[string]any)["total"] = 4096
		case "correlation-force-usage-missing":
			delete(info, "tokens")
		case "correlation-force-usage-zero":
			info["tokens"] = map[string]any{"total": 0}
		case "correlation-force-usage-negative":
			info["tokens"] = map[string]any{"total": -1}
		case "correlation-force-usage-fraction":
			info["tokens"] = map[string]any{"input": 1.5}
		case "correlation-force-usage-overflow":
			info["tokens"] = map[string]any{"input": int64(9223372036854775807), "output": 1}
		case "correlation-force-usage-foreign":
			info["modelID"] = "glm-5.3-flash"
		case "correlation-force-usage-stale":
			info["time"] = map[string]any{"created": started.UnixMilli() - 60000, "completed": started.UnixMilli() - 59000}
		case "correlation-force-existing-summary":
			info["summary"] = true
		}
	}
	if mode == "correlation-compaction-missing" {
		// A valid exported lifecycle without an actual exchange must still fail.
		parent["messages"] = append(parent["messages"].([]any), openCodeCompactionFixture(started.UnixMilli(), now+500)...)
	}
	for name, value := range map[string]any{"parent-export.json": parent, "child-export.json": child} {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		raw = []byte(strings.NewReplacer("ses_parent", "ses_factory", "msg_parent", "msg_answer").Replace(string(raw)))
		if err := os.WriteFile(filepath.Join(root, name), raw, 0600); err != nil {
			return err
		}
	}
	return nil
}

func TestAgentEvidenceOpenCodeSubagentLifecycle(t *testing.T) {
	parent, child := openCodeSubagentFixture()
	tasks, err := parseOpenCodeSubagentTasks(subagentJSON(t, parent), "ses_parent", "glm-5.3")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	proof, err := validateOpenCodeSubagentChild(subagentJSON(t, child), tasks[0])
	require.NoError(t, err)
	assert.Equal(t, "ses_child", proof.ChildSession)
	assert.Equal(t, []string{"msg_child"}, proof.MessageIDs)
	assert.Equal(t, "prt_task", proof.ToolID)
	raw := string(subagentJSON(t, proof))
	assert.NotContains(t, raw, "private prompt")
	assert.NotContains(t, raw, "private response")
}
func TestAgentEvidenceOpenCodeSubagentToolContinuation(t *testing.T) {
	parent, child := openCodeSubagentFixture()
	info := subagentChildInfo(child)
	raw := subagentJSON(t, child["messages"].([]any)[1])
	var final map[string]any
	require.NoError(t, json.Unmarshal(raw, &final))
	final["info"].(map[string]any)["id"] = "msg_final"
	final["info"].(map[string]any)["time"] = map[string]any{"created": 1600, "completed": 1900}
	info["finish"] = "tool-calls"
	info["time"] = map[string]any{"created": 1200, "completed": 1500}
	child["messages"] = append(child["messages"].([]any), final)
	tasks, err := parseOpenCodeSubagentTasks(subagentJSON(t, parent), "ses_parent", "glm-5.3")
	require.NoError(t, err)
	proof, err := validateOpenCodeSubagentChild(subagentJSON(t, child), tasks[0])
	require.NoError(t, err)
	require.Equal(t, []string{"msg_child", "msg_final"}, proof.MessageIDs)
}

func TestAgentEvidenceOpenCodeSubagentRejectsFalseLifecycle(t *testing.T) {
	cases := map[string]func(map[string]any, map[string]any){
		"no task":          func(p, c map[string]any) { p["messages"] = []any{} },
		"wrong session":    func(p, c map[string]any) { p["info"].(map[string]any)["id"] = "ses_stale" },
		"missing metadata": func(p, c map[string]any) { delete(subagentTaskState(p), "metadata") },
		"redacted metadata": func(p, c map[string]any) {
			subagentTaskState(p)["metadata"] = map[string]any{"redacted": "tool-state-metadata:prt_task"}
		},
		"failed task":     func(p, c map[string]any) { subagentTaskState(p)["status"] = "error" },
		"background task": func(p, c map[string]any) { subagentTaskState(p)["metadata"].(map[string]any)["background"] = true },
		"wrong task model": func(p, c map[string]any) {
			subagentTaskState(p)["metadata"].(map[string]any)["model"].(map[string]any)["modelID"] = "glm-5.3-flash"
		},
		"secret child id": func(p, c map[string]any) {
			subagentTaskState(p)["metadata"].(map[string]any)["sessionId"] = "ses_" + captureFixtureKey
		},
		"secret tool id": func(p, c map[string]any) {
			p["messages"].([]any)[0].(map[string]any)["parts"].([]any)[0].(map[string]any)["id"] = "prt_" + captureFixtureKey
		},
		"unsafe child id":  func(p, c map[string]any) { subagentTaskState(p)["metadata"].(map[string]any)["sessionId"] = "--help" },
		"wrong parent":     func(p, c map[string]any) { c["info"].(map[string]any)["parentID"] = "ses_other" },
		"empty child":      func(p, c map[string]any) { c["messages"] = []any{} },
		"foreign model":    func(p, c map[string]any) { subagentChildInfo(c)["modelID"] = "glm-5.3-flash" },
		"foreign provider": func(p, c map[string]any) { subagentChildInfo(c)["providerID"] = "openai" },
		"failed child": func(p, c map[string]any) {
			subagentChildInfo(c)["error"] = map[string]any{"message": captureFixtureKey}
		},
		"incomplete child":    func(p, c map[string]any) { delete(subagentChildInfo(c), "finish") },
		"truncated child":     func(p, c map[string]any) { subagentChildInfo(c)["finish"] = "length" },
		"child only tools":    func(p, c map[string]any) { subagentChildInfo(c)["finish"] = "tool-calls" },
		"no completed time":   func(p, c map[string]any) { delete(subagentChildInfo(c)["time"].(map[string]any), "completed") },
		"outside task":        func(p, c map[string]any) { subagentChildInfo(c)["time"].(map[string]any)["completed"] = 2001 },
		"missing user parent": func(p, c map[string]any) { subagentChildInfo(c)["parentID"] = "msg_missing" },
		"duplicate task": func(p, c map[string]any) {
			m := p["messages"].([]any)[0].(map[string]any)
			m["parts"] = append(m["parts"].([]any), m["parts"].([]any)[0])
		},
		"duplicate child message": func(p, c map[string]any) { c["messages"] = append(c["messages"].([]any), c["messages"].([]any)[1]) },
		"after terminal": func(p, c map[string]any) {
			raw := subagentJSON(t, c["messages"].([]any)[1])
			var next map[string]any
			require.NoError(t, json.Unmarshal(raw, &next))
			next["info"].(map[string]any)["id"] = "msg_late"
			next["info"].(map[string]any)["time"] = map[string]any{"created": 1950, "completed": 1990}
			c["messages"] = append(c["messages"].([]any), next)
		},
		"failed child tool": func(p, c map[string]any) {
			c["messages"].([]any)[1].(map[string]any)["parts"] = []any{map[string]any{"type": "tool", "tool": "bash", "state": map[string]any{"status": "error"}}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			parent, child := openCodeSubagentFixture()
			mutate(parent, child)
			tasks, err := parseOpenCodeSubagentTasks(subagentJSON(t, parent), "ses_parent", "glm-5.3")
			if err == nil {
				_, err = validateOpenCodeSubagentChild(subagentJSON(t, child), tasks[0])
			}
			require.Error(t, err)
			assert.NotContains(t, err.Error(), captureFixtureKey)
		})
	}
}
func TestAgentEvidenceOpenCodeSubagentMalformed(t *testing.T) {
	for _, raw := range []string{"", "{", `{"info":{},"info":{},"messages":[]}`, strings.Repeat(" ", openCodeControlsLimit+1)} {
		_, err := parseOpenCodeSubagentTasks([]byte(raw), "ses_parent", "glm-5.3")
		require.Error(t, err)
		parent, _ := openCodeSubagentFixture()
		tasks, err := parseOpenCodeSubagentTasks(subagentJSON(t, parent), "ses_parent", "glm-5.3")
		require.NoError(t, err)
		_, err = validateOpenCodeSubagentChild([]byte(raw), tasks[0])
		require.Error(t, err)
	}
}

func TestAgentEvidenceOpenCodeSubagentQueryBoundary(t *testing.T) {
	base := claudecli.RunSpec{Bin: "exact-binary", Args: []string{"export", "ses_parent", "--pure"}, Env: []string{"LUNAROUTE_API_KEY=" + captureFixtureKey, "OPENCODE_CONFIG_CONTENT=" + controlsOverlay}, Dir: t.TempDir(), Stdin: "not a query prompt", MaxResponseBytes: captureBodyLimit}
	for _, mode := range []string{"success", "child exit", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			parent, child := openCodeSubagentFixture()
			calls := 0
			next := controlsRunnerFunc(func(ctx context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
				calls++
				if spec.Bin != base.Bin || spec.Dir != base.Dir || !reflect.DeepEqual(spec.Env, base.Env) || spec.Stdin != "" || spec.MaxResponseBytes != openCodeControlsLimit {
					t.Fatal("export query lost exact emitted executable/env/cwd or bounds")
				}
				deadline, ok := ctx.Deadline()
				require.True(t, ok)
				require.LessOrEqual(t, time.Until(deadline), openCodeControlsTimeout)
				value := parent
				expected := []string{"export", "ses_parent", "--pure"}
				if calls == 2 {
					value = child
					expected[1] = "ses_child"
				}
				require.Equal(t, expected, spec.Args)
				if mode == "child exit" && calls == 2 {
					return nil, nil, errAgentEvidence
				}
				out := make(chan string, 1)
				out <- string(subagentJSON(t, value))
				close(out)
				return out, func() error { return nil }, nil
			})
			runner := openCodeSubagentsRunner{next: next, startedAt: time.UnixMilli(900)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "canceled" {
				cancel()
			}
			lines, err := runCapturedLines(ctx, runner, base)
			if mode != "success" {
				require.Error(t, err)
				require.Empty(t, lines)
				return
			}
			require.NoError(t, err)
			require.Equal(t, 2, calls)
			require.JSONEq(t, string(subagentJSON(t, parent)), strings.Join(lines, "\n"))
			require.Equal(t, "ses_parent", base.Args[1], "query must not mutate the mission/export argv")
		})
	}
}

func TestAgentEvidenceOpenCodeSubagentMissionInputs(t *testing.T) {
	binding := liveBinding(captureFixtureKey, "glm-5.3", "opencode")
	require.Contains(t, binding.AllowedTools, "Task")
	require.Contains(t, agentMissionPromptFor("opencode"), "synchronous general subagent")
	require.Equal(t, agentMissionPrompt+" Also use the Agent tool with subagent_type=general-purpose, model=haiku and run_in_background=false to read and verify both files. Your next action depends on its result; wait for successful completion. Do not use remote isolation or resume an old agent.", agentMissionPromptFor("claude-code"))
}

func TestAgentEvidenceOpenCodeSubagentFactorySink(t *testing.T) {
	for _, mode := range []string{"success", "subagent-missing", "subagent-parent", "subagent-model", "subagent-error", "subagent-stale", "subagent-export-exit", "subagent-export-overflow"} {
		t.Run(mode, func(t *testing.T) {
			root, bin, capture := agentFactoryFixture(t, "opencode", mode)
			client, _ := agentFactoryProbeClient(t)
			result, err := executeAgentMission(context.Background(), liveFactoryWithClient(root, bin, agentFactoryRules(t), client), liveBinding(captureFixtureKey, "glm-5.3", "opencode"), root)
			// Both command-created files and actual authenticated inference exist even
			// on lifecycle refusal. They cannot substitute for the missing helper proof.
			proof, readErr := os.ReadFile(filepath.Join(root, agentProofFile))
			require.NoError(t, readErr)
			require.Equal(t, agentProofText, string(proof))
			require.NotEmpty(t, capture.Records())
			if mode != "success" {
				require.Error(t, err)
				assert.Nil(t, result)
				assert.NotContains(t, err.Error(), captureFixtureKey)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			marker, err := os.ReadFile(filepath.Join(root, "subagent-export-marker"))
			require.NoError(t, err)
			assert.Equal(t, "ses_child", string(marker))
			assert.Equal(t, "ses_factory", result.SessionID)
		})
	}
}
