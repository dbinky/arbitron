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

// Source-grounded protocol fixtures, not installed-binary observations. The
// exported title must be the result of the actual captured title exchange.
func TestAgentEvidenceOpenCodeTitleFactoryPublication(t *testing.T) {
	for _, mode := range []string{"success", "stream", "missing", "drift", "session", "prompt", "empty", "duplicate", "malformed", "model", "secret"} {
		t.Run(mode, func(t *testing.T) {
			started := time.Now().UTC()
			root, bin, capture := agentFactoryFixture(t, "opencode", "correlation-title-"+mode)
			client, _ := agentFactoryProbeClient(t)
			result, err := executeAgentMission(context.Background(), liveCapturedFactoryWithClient(root, bin, agentFactoryRules(t), client, capture), liveBinding(captureFixtureKey, "glm-5.3", "opencode"), root)
			require.FileExists(t, filepath.Join(root, agentProofFile))
			require.FileExists(t, filepath.Join(root, agentCommandFile))
			ref := evidencePath + "/observations/controls.json"
			require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, ref)), 0700))
			config, marshalErr := json.Marshal(agentConfigReference{RunID: "run-one", Backend: "opencode", Model: "glm-5.3", SessionID: "ses_factory", Source: "fixture effective controls"})
			require.NoError(t, marshalErr)
			require.NoError(t, os.WriteFile(filepath.Join(root, ref), config, 0600))
			input := agentEvidenceInput{RunID: "run-one", Backend: "opencode", Model: "glm-5.3", Version: "1.19.5", ConfigRef: ref, StartedAt: started, FinishedAt: time.Now().UTC(), RequiredCategories: []string{"main", "subagent", "title", "compaction"}, Records: capture.Records(), Result: result}
			if err == nil {
				err = publishAgentEvidence(root, root, captureFixtureKey, "title.json", input)
			}
			path := filepath.Join(root, evidencePath, "observations/title.json")
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
			assert.Equal(t, "compaction", obs.Inference[3].Category)
			assert.Equal(t, openCodeCompactionSource, obs.Inference[3].Operation.Source)
			assert.True(t, obs.Completed && obs.FileOK && obs.CommandOK)
			title := obs.Inference[2]
			require.Equal(t, "title", title.Category)
			require.NotNil(t, title.Operation)
			assert.Equal(t, "ses_factory", title.SessionID)
			assert.Equal(t, "first-user-title", title.Operation.Trigger)
			assert.Equal(t, openCodeTitleSource, title.Operation.Source)
			assert.Equal(t, "1.19.5", title.Operation.Version)
			assert.Equal(t, []string{title.RequestID}, title.Operation.RequestIDs)
			assert.Equal(t, []string{"glm-5.3"}, title.ReportedModels)
			assert.Equal(t, "main", obs.Inference[0].Category)
			assert.Equal(t, "subagent", obs.Inference[1].Category)
			for _, private := range []string{captureFixtureKey, agentMissionPromptFor("opencode"), "Private fixture title", openCodeTitlePrefix} {
				assert.NotContains(t, string(raw), private)
			}
			require.NoError(t, capture.Close())
			capture.mu.Lock()
			retained := len(capture.openCodeTitles)
			capture.mu.Unlock()
			assert.Zero(t, retained, "private title comparison material must be discarded on cleanup")
		})
	}
}

func TestAgentEvidenceOpenCodeTitleWireSignature(t *testing.T) {
	for _, content := range []any{agentMissionPromptFor("opencode"), []map[string]string{{"type": "text", "text": agentMissionPromptFor("opencode")}}} {
		raw, err := json.Marshal(map[string]any{"messages": []map[string]any{{"role": "system", "content": "You are a title generator. You output ONLY a thread title. Nothing else.\nsource prompt"}, {"role": "user", "content": "Generate a title for this conversation:\n"}, {"role": "user", "content": content}}})
		require.NoError(t, err)
		require.True(t, isOpenCodeTitleRequest(raw))
	}
	for _, messages := range []any{
		[]map[string]string{{"role": "user", "content": openCodeTitlePrefix}, {"role": "user", "content": agentMissionPromptFor("opencode")}},
		[]map[string]string{{"role": "system", "content": "different agent"}, {"role": "user", "content": openCodeTitlePrefix}, {"role": "user", "content": agentMissionPromptFor("opencode")}},
	} {
		raw, err := json.Marshal(map[string]any{"messages": messages})
		require.NoError(t, err)
		require.False(t, isOpenCodeTitleRequest(raw))
	}
}

func TestAgentEvidenceOpenCodeTitleNormalization(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"<think>private reasoning</think>\n  Private fixture title  \nignored", "Private fixture title"},
		{strings.Repeat("a", 101), strings.Repeat("a", 97) + "..."},
		{strings.Repeat("界", 101), strings.Repeat("界", 97) + "..."},
		{"  \n\t", ""},
	} {
		assert.Equal(t, tc.want, normalizeOpenCodeTitle(tc.input))
	}
}
