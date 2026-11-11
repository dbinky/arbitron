package lunaroute_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Reduced v1.19.5 compaction/create/process/export schema, not live evidence.
func openCodeCompactionFixture(start, end int64) []any {
	return []any{
		map[string]any{"info": map[string]any{"id": "msg_compact_user", "sessionID": "ses_factory", "role": "user", "model": map[string]any{"providerID": "lunaroute", "modelID": "glm-5.3"}, "time": map[string]any{"created": start}}, "parts": []any{map[string]any{"id": "prt_compact", "messageID": "msg_compact_user", "sessionID": "ses_factory", "type": "compaction", "auto": true}}},
		map[string]any{"info": map[string]any{"id": "msg_compact_answer", "sessionID": "ses_factory", "parentID": "msg_compact_user", "role": "assistant", "providerID": "lunaroute", "modelID": "glm-5.3", "mode": "compaction", "agent": "compaction", "summary": true, "finish": "stop", "time": map[string]any{"created": start, "completed": end}}, "parts": []any{map[string]any{"id": "prt_summary", "messageID": "msg_compact_answer", "sessionID": "ses_factory", "type": "text", "text": "Private compaction summary"}}},
	}
}

func writeFactoryCompactionExport(root, mode string, started time.Time) error {
	path := filepath.Join(root, "parent-export.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var parent map[string]any
	if err := json.Unmarshal(raw, &parent); err != nil {
		return err
	}
	messages := openCodeCompactionFixture(started.UnixMilli(), time.Now().UnixMilli()+500)
	info := messages[1].(map[string]any)["info"].(map[string]any)
	part := messages[1].(map[string]any)["parts"].([]any)[0].(map[string]any)
	switch mode {
	case "correlation-compaction-drift":
		part["text"] = "Unrelated exported summary"
	case "correlation-compaction-secret":
		part["text"] = captureFixtureKey
	case "correlation-compaction-stale":
		info["time"] = map[string]any{"created": started.UnixMilli() - 60000, "completed": started.UnixMilli() - 59000}
	case "correlation-compaction-incomplete":
		info["finish"] = "length"
	}
	parent["messages"] = append(parent["messages"].([]any), messages...)
	raw, err = json.Marshal(parent)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0600)
}

func TestAgentEvidenceOpenCodeCompactionLifecycle(t *testing.T) {
	for _, mode := range []string{"success", "missing", "parent", "model", "provider", "summary", "agent", "finish", "error", "stale", "duplicate", "part", "secret", "manual"} {
		t.Run(mode, func(t *testing.T) {
			started := time.UnixMilli(1000)
			messages := openCodeCompactionFixture(1100, 1900)
			user := messages[0].(map[string]any)
			answer := messages[1].(map[string]any)
			info := answer["info"].(map[string]any)
			part := answer["parts"].([]any)[0].(map[string]any)
			switch mode {
			case "missing":
				messages = nil
			case "parent":
				info["parentID"] = "msg_foreign"
			case "model":
				info["modelID"] = "glm-5.3-flash"
			case "provider":
				user["info"].(map[string]any)["model"] = map[string]any{"providerID": "foreign", "modelID": "glm-5.3"}
			case "summary":
				info["summary"] = false
			case "agent":
				info["agent"] = "build"
			case "finish":
				info["finish"] = "length"
			case "error":
				info["error"] = map[string]any{"message": captureFixtureKey}
			case "stale":
				info["time"] = map[string]any{"created": 900, "completed": 1900}
			case "duplicate":
				messages = append(messages, answer)
			case "part":
				part["messageID"] = "msg_foreign"
			case "secret":
				part["text"] = captureFixtureKey
			case "manual":
				user["parts"].([]any)[0].(map[string]any)["auto"] = false
			}
			raw := subagentJSON(t, map[string]any{"info": map[string]any{"id": "ses_factory"}, "messages": messages})
			proof, err := parseOpenCodeCompaction(raw, "ses_factory", "glm-5.3", captureFixtureKey, started, time.UnixMilli(2000))
			if mode != "success" {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), captureFixtureKey)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "msg_compact_answer", proof.MessageID)
			assert.Equal(t, "prt_compact", proof.PartID)
			assert.Equal(t, "Private compaction summary", proof.text)
		})
	}
	for _, raw := range [][]byte{nil, []byte("{"), []byte(`{"info":{},"info":{}}`), []byte(strings.Repeat("x", openCodeControlsLimit+1))} {
		_, err := parseOpenCodeCompaction(raw, "ses_factory", "glm-5.3", captureFixtureKey, time.Now(), time.Now())
		require.Error(t, err)
	}
}

func TestAgentEvidenceOpenCodeCompactionWireSignature(t *testing.T) {
	system, err := os.ReadFile(filepath.Join(repoRoot(t), evidencePath, "sources/lr06-compaction-prompt.txt"))
	require.NoError(t, err)
	template, err := os.ReadFile(filepath.Join(repoRoot(t), evidencePath, "sources/lr06-compaction-template.txt"))
	require.NoError(t, err)
	prompt := openCodeCompactionPrefix + "[User]: " + agentMissionPromptFor("opencode") + "\n</conversation>\n\n" + openCodeCompactionInstruction + "\n\n" + string(template)
	for _, mode := range []string{"success", "text-parts", "template", "history", "system", "tools", "instruction"} {
		t.Run(mode, func(t *testing.T) {
			wirePrompt := prompt
			sys := string(system)
			if mode == "template" {
				wirePrompt = strings.TrimSuffix(prompt, string(template)) + "unrelated template"
			}
			if mode == "history" {
				wirePrompt = strings.Replace(prompt, agentMissionPromptFor("opencode"), "foreign mission", 1)
			}
			if mode == "system" {
				sys = "different agent"
			}
			if mode == "instruction" {
				wirePrompt = strings.Replace(prompt, openCodeCompactionInstruction, "different instruction", 1)
			}
			var content any = wirePrompt
			if mode == "text-parts" {
				content = []map[string]string{{"type": "text", "text": wirePrompt}}
			}
			body := map[string]any{"messages": []map[string]any{{"role": "system", "content": sys}, {"role": "user", "content": content}}}
			if mode == "tools" {
				body["tools"] = []any{map[string]string{"type": "function"}}
			}
			assert.Equal(t, mode == "success" || mode == "text-parts", isOpenCodeCompactionRequest(subagentJSON(t, body)))
		})
	}
}

func TestAgentEvidenceOpenCodeCompactionFactoryPublication(t *testing.T) {
	for _, mode := range []string{"success", "stream", "missing", "export-missing", "drift", "session", "parent", "duplicate", "prompt", "secret", "model", "stale", "incomplete"} {
		t.Run(mode, func(t *testing.T) {
			started := time.Now().UTC()
			root, bin, capture := agentFactoryFixture(t, "opencode", "correlation-compaction-"+mode)
			client, _ := agentFactoryProbeClient(t)
			result, err := executeAgentMission(context.Background(), liveCapturedFactoryWithClient(root, bin, agentFactoryRules(t), client, capture), liveBinding(captureFixtureKey, "glm-5.3", "opencode"), root)
			// Real command-created proofs alone cannot substitute for a helper exchange.
			require.FileExists(t, filepath.Join(root, agentProofFile))
			require.FileExists(t, filepath.Join(root, agentCommandFile))
			ref := evidencePath + "/observations/controls.json"
			require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, ref)), 0700))
			config, marshalErr := json.Marshal(agentConfigReference{RunID: "run-one", Backend: "opencode", Model: "glm-5.3", SessionID: "ses_factory", Source: "fixture effective controls"})
			require.NoError(t, marshalErr)
			require.NoError(t, os.WriteFile(filepath.Join(root, ref), config, 0600))
			if err == nil {
				err = publishAgentEvidence(root, root, captureFixtureKey, "compaction.json", agentEvidenceInput{RunID: "run-one", Backend: "opencode", Model: "glm-5.3", Version: "1.19.5", ConfigRef: ref, StartedAt: started, FinishedAt: time.Now().UTC(), RequiredCategories: []string{"main", "subagent", "title", "compaction"}, Records: capture.Records(), Result: result})
			}
			path := filepath.Join(root, evidencePath, "observations/compaction.json")
			if mode != "success" && mode != "stream" {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), captureFixtureKey)
				_, statErr := os.Stat(path)
				require.True(t, os.IsNotExist(statErr))
				return
			}
			require.NoError(t, err)
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			var obs agentTaskObservation
			require.NoError(t, json.Unmarshal(raw, &obs))
			require.Len(t, obs.Inference, 4)
			assert.True(t, obs.Completed && obs.FileOK && obs.CommandOK)
			compact := obs.Inference[3]
			require.Equal(t, "compaction", compact.Category)
			require.NotNil(t, compact.Operation)
			assert.Equal(t, "ses_factory", compact.SessionID)
			assert.Equal(t, "msg_compact_answer", compact.Operation.ID)
			assert.Equal(t, "auto-compaction", compact.Operation.Trigger)
			assert.Equal(t, openCodeCompactionSource, compact.Operation.Source)
			assert.Equal(t, "1.19.5", compact.Operation.Version)
			assert.Equal(t, []string{compact.RequestID}, compact.Operation.RequestIDs)
			assert.Equal(t, []string{"glm-5.3"}, compact.ReportedModels)
			assert.True(t, compact.AuthMatchesSelected && compact.Completed)
			for _, private := range []string{captureFixtureKey, agentMissionPromptFor("opencode"), "Private compaction summary", "Here is the conversation so far:"} {
				assert.NotContains(t, string(raw), private)
			}
			require.NoError(t, capture.Close())
			capture.mu.Lock()
			retained := len(capture.openCodeCompactions)
			capture.mu.Unlock()
			assert.Zero(t, retained)
		})
	}
}
