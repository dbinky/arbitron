package lunaroute_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const claudeTitleFixtureBudget = 1024

// Protocol processes prove the experiment's data path, not installed helper
// behavior. A title exchange must survive to the cleanup-before-publication sink.
func TestAgentEvidenceClaudeTitlePublication(t *testing.T) {
	for _, mode := range []string{"success", "stream", "late", "missing", "duplicate", "session", "class", "model", "prompt", "system", "empty", "secret", "malformed", "stale", "agent", "memory-on-helper", "none-title-absent", "none-title-disabled", "missing-feed", "missing-source"} {
		t.Run(mode, func(t *testing.T) {
			selected := provider.EffortDefault
			if strings.HasPrefix(mode, "none-title-") {
				selected = provider.EffortNone
			}
			root, bin, capture := agentFactoryFixtureWithEffort(t, "claude-code", "claude-route-title-"+mode, selected)
			ws := filepath.Join(root, "mission")
			require.NoError(t, os.Mkdir(ws, 0700))
			isolation, err := seedClaudeIsolation(filepath.Join(root, "home"), ws)
			require.NoError(t, err)
			t.Setenv("HOME", isolation.home)
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			require.NoError(t, os.Unsetenv("CLAUDE_CONFIG_DIR"))
			client, calls := agentFactoryProbeClient(t)
			rules := agentFactoryRules(t)
			if selected == provider.EffortNone {
				rules = claudeThinkingRules(t)
			}
			binding := liveBinding(captureFixtureKey, "glm-5.3", "claude-code")
			binding.Effort = selected
			factory := liveClaudeIsolationFactoryWithClient(root, bin, rules, client, isolation, capture, selected)
			started := time.Now().UTC()
			result, missionErr := executePreparedAgentMission(context.Background(), factory, binding, ws)
			finished := time.Now().UTC()
			require.NoError(t, missionErr)
			file, err := os.ReadFile(filepath.Join(ws, agentProofFile))
			require.NoError(t, err)
			require.Equal(t, agentProofText, string(file))
			require.Zero(t, *calls)
			expectedRequests := 3
			if mode == "missing" {
				expectedRequests = 2
			}
			if mode == "duplicate" {
				expectedRequests = 4
			}
			actualRequests, failedMarkers := 0, 0
			for _, record := range capture.Records() {
				if record.RequestID != "" {
					actualRequests++
				} else if record.Failure == "capture_failed" {
					failedMarkers++
				}
			}
			require.Equal(t, expectedRequests, actualRequests, "negative must exercise its actual exchange")
			if mode == "empty" || mode == "secret" || mode == "malformed" {
				require.Equal(t, 1, failedMarkers)
			} else {
				require.Zero(t, failedMarkers)
			}
			if mode == "stale" || mode == "agent" {
				capture.mu.Lock()
				if mode == "stale" {
					capture.records[0].StartedAt = started.Add(-time.Second)
				} else {
					capture.records[0].ClaudeAgentID = "agent_other"
				}
				capture.mu.Unlock()
			}
			if mode == "memory-on-helper" {
				capture.mu.Lock()
				capture.records[1].ClaudeProjectMemory = false
				capture.records[2].ClaudeProjectMemory = true
				capture.mu.Unlock()
			}
			if mode == "missing-feed" {
				capture.mu.Lock()
				for i := range capture.records {
					capture.records[i].ClaudeSessionTitle = false
				}
				capture.mu.Unlock()
			}
			repo := t.TempDir()
			for _, source := range []string{claudeIsolationSource, claudeLimitsSource, claudeMemorySource, claudeEffortSource, claudeThinkingSource, claudeSubagentSource, claudeSubagentRoutingSource, claudeSelectorsSource, claudeInputSource, claudeTitleSource} {
				raw, err := readRepoFile(repoRoot(t), source)
				require.NoError(t, err)
				if source == claudeInputSource {
					raw = []byte(claudePublicationSnapshot)
				}
				path := filepath.Join(repo, source)
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
				require.NoError(t, os.WriteFile(path, raw, 0600))
			}
			if mode == "missing-source" {
				require.NoError(t, os.Remove(filepath.Join(repo, claudeTitleSource)))
			}
			err = finishClaudeIsolation(repo, root, isolation, capture, result, started, finished)
			sink := filepath.Join(repo, evidencePath, "observations/lr05-isolation.json")
			if mode != "success" && mode != "stream" && mode != "late" && mode != "none-title-disabled" {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), captureFixtureKey)
				_, statErr := os.Stat(sink)
				require.True(t, os.IsNotExist(statErr), "incomplete helper evidence must not publish")
				return
			}
			require.NoError(t, err)
			raw, err := os.ReadFile(sink)
			require.NoError(t, err)
			var saved struct {
				Title     claudeTitleProof         `json:"session_title"`
				Inference []capturedInference      `json:"inference"`
				Limits    claudeMainLimitProof     `json:"main_limits"`
				Memory    claudeProjectMemoryProof `json:"project_memory"`
			}
			require.NoError(t, json.Unmarshal(raw, &saved))
			require.Len(t, saved.Inference, 3)
			titleIndex, mainIndex, helperIndex := 0, 1, 2
			if mode == "late" {
				titleIndex, mainIndex, helperIndex = 1, 0, 2
			}
			assert.Equal(t, "title", saved.Inference[titleIndex].Category, "asynchronous title must not consume the main memory slot")
			assert.Equal(t, claudeTitleFixtureBudget, saved.Inference[titleIndex].MaxTokens)
			if selected == provider.EffortNone {
				require.NotNil(t, saved.Inference[titleIndex].ClaudeThinking)
				assert.Equal(t, "disabled", saved.Inference[titleIndex].ClaudeThinking.Type)
			}
			assert.Equal(t, "main", saved.Inference[mainIndex].Category)
			assert.Equal(t, "subagent", saved.Inference[helperIndex].Category)
			assert.Equal(t, claudeTitleSource, saved.Title.Source)
			assert.Equal(t, claudeStartupVersion, saved.Title.Version)
			assert.Equal(t, "ses_factory", saved.Title.Session)
			assert.Equal(t, "glm-5.3", saved.Title.Model)
			assert.Equal(t, []string{saved.Inference[titleIndex].RequestID}, saved.Title.RequestIDs)
			assert.Equal(t, []string{saved.Inference[mainIndex].RequestID}, saved.Limits.RequestIDs)
			assert.Equal(t, []string{saved.Inference[mainIndex].RequestID}, saved.Memory.RequestIDs)
			for _, record := range saved.Inference {
				assert.Equal(t, "glm-5.3", record.RequestedModel)
				assert.Equal(t, []string{"glm-5.3"}, record.ReportedModels)
				assert.True(t, record.Completed && record.AuthMatchesSelected)
			}
			for _, dir := range []string{root, capture.dir} {
				_, err := os.Stat(dir)
				require.True(t, os.IsNotExist(err))
			}
			for _, private := range []string{root, bin, captureFixtureKey, agentMissionPromptFor("claude-code"), claudeForeignCredential, "Private fixture title"} {
				assert.NotContains(t, string(raw), private)
			}
		})
	}
}

func TestAgentEvidenceClaudeTitleWireSignature(t *testing.T) {
	raw := claudeTitleFixtureRequest(t, false)
	require.True(t, isClaudeTitleRequest(raw))
	for _, term := range []string{claudeTitleSystem, agentMissionPromptFor("claude-code"), "</session>", `"role":"user"`} {
		encoded, err := json.Marshal(term)
		require.NoError(t, err)
		needle := string(encoded[1 : len(encoded)-1])
		if strings.HasPrefix(term, `"role"`) {
			needle = term
		}
		require.Contains(t, string(raw), needle)
		replaced := strings.Replace(string(raw), needle, "substituted", 1)
		require.False(t, isClaudeTitleRequest([]byte(replaced)), "foreign source/history must not be attributed")
	}
	for _, bad := range []string{"", "null", "{", strings.Repeat(" ", captureBodyLimit+1), strings.Replace(string(raw), `"model":`, `"model":"other","model":`, 1)} {
		require.False(t, isClaudeTitleRequest([]byte(bad)))
	}
}

func TestAgentEvidenceClaudeTitleResponseSchema(t *testing.T) {
	response := func(text string) []byte {
		raw, err := json.Marshal(map[string]any{"type": "message", "model": "glm-5.3", "stop_reason": "end_turn", "content": []map[string]string{{"type": "text", "text": text}}})
		require.NoError(t, err)
		return raw
	}
	require.True(t, validClaudeTitleResponse(response(`{"title":"Private fixture title"}`), false, captureFixtureKey))
	for _, text := range []string{"", "null", `[]`, `{}`, `{"title":null}`, `{"title":1}`, `{"title":" "}`, `{"title":"a","title":"b"}`, `{"title":"a","extra":"data"}`, `{"title":"` + captureFixtureKey + `"}`, `{"title":"two\nlines"}`, strings.Repeat("x", captureBodyLimit+1)} {
		require.False(t, validClaudeTitleResponse(response(text), false, captureFixtureKey))
	}
	for _, raw := range [][]byte{nil, []byte("{"), []byte(`{"type":"message","content":[{"type":"tool_use","text":"{\"title\":\"wrong\"}"}]}`)} {
		require.False(t, validClaudeTitleResponse(raw, false, captureFixtureKey))
	}
}

func claudeTitleFixtureRequest(t *testing.T, stream bool) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"model": "glm-5.3", "max_tokens": claudeTitleFixtureBudget, "stream": stream, "system": []map[string]string{{"type": "text", "text": claudeTitleSystem}}, "messages": []map[string]string{{"role": "user", "content": claudeTitleUserPrompt()}}, "tools": []any{}})
	require.NoError(t, err)
	return raw
}
