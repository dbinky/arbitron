package lunaroute_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Protocol processes exercise the actual experiment Factory and capture. They
// are not observations of installed Claude controls or gateway effort support.
func TestAgentEvidenceClaudeSubagentControlsSink(t *testing.T) {
	for _, tc := range []struct {
		name     string
		selected provider.Effort
		mutate   string
	}{
		{"default-distinct-helper", provider.EffortDefault, ""},
		{"explicit-high", provider.EffortHigh, ""},
		{"explicit-none", provider.EffortNone, ""},
		{"missing-helper-effort", provider.EffortDefault, "effort"},
		{"missing-helper-thinking", provider.EffortDefault, "thinking"},
		{"missing-helper-budget", provider.EffortDefault, "budget"},
		{"helper-effort-drift", provider.EffortHigh, "drift"},
		{"helper-none-absent", provider.EffortNone, "absent"},
		{"unsafe-helper-effort", provider.EffortDefault, "unsafe"},
		{"missing-main-controls", provider.EffortDefault, "main"},
		{"missing-helper-routing", provider.EffortDefault, "routing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, bin, capture := agentFactoryFixtureWithEffort(t, "claude-code", "claude-route-controls", tc.selected)
			ws := filepath.Join(root, "mission")
			require.NoError(t, os.Mkdir(ws, 0700))
			isolation, err := seedClaudeIsolation(filepath.Join(root, "home"), ws)
			require.NoError(t, err)
			t.Setenv("HOME", isolation.home)
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			require.NoError(t, os.Unsetenv("CLAUDE_CONFIG_DIR"))
			binding := liveBinding(captureFixtureKey, "glm-5.3", "claude-code")
			binding.Effort = tc.selected
			client, calls := agentFactoryProbeClient(t)
			rules := claudeThinkingRules(t)
			if tc.selected == provider.EffortHigh {
				rules = claudeEffortRules(t)
			}
			factory := liveClaudeIsolationFactoryWithClient(root, bin, rules, client, isolation, capture, binding.Effort)
			started := time.Now().UTC()
			result, err := executePreparedAgentMission(context.Background(), factory, binding, ws)
			require.NoError(t, err)
			finished := time.Now().UTC()
			require.Zero(t, *calls)
			file, err := os.ReadFile(filepath.Join(ws, agentProofFile))
			require.NoError(t, err)
			require.Equal(t, agentProofText, string(file))
			records := capture.Records()
			require.Len(t, records, 2)
			for _, record := range records {
				require.True(t, record.Completed && record.AuthMatchesSelected)
			}
			capture.mu.Lock()
			switch tc.mutate {
			case "effort":
				capture.records[1].ClaudeEffort = nil
			case "thinking":
				capture.records[1].ClaudeThinking = nil
			case "budget":
				capture.records[1].MaxTokens = 0
			case "drift":
				capture.records[1].ClaudeEffort = &claudeRequestEffort{Level: "medium"}
			case "absent":
				capture.records[1].ClaudeThinking = &claudeRequestThinking{}
			case "unsafe":
				capture.records[1].ClaudeEffort = &claudeRequestEffort{Level: captureFixtureKey}
			case "main":
				capture.records[0].ClaudeThinking = nil
			case "routing":
				capture.records[1].ClaudeAgentID = ""
			}
			capture.mu.Unlock()
			observation, err := collectClaudeSubagentRoutingEvidence(isolation, capture, result, started, finished)
			if err == nil {
				_, err = saveObservation(root, captureFixtureKey, "claude-controls.json", observation)
			}
			path := filepath.Join(root, evidencePath, "observations/claude-controls.json")
			if tc.mutate != "" {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), captureFixtureKey)
				_, statErr := os.Stat(path)
				assert.True(t, os.IsNotExist(statErr))
				return
			}
			require.NoError(t, err)
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			var saved struct {
				Limits   claudeMainLimitProof    `json:"main_limits"`
				Effort   claudeMainEffortProof   `json:"main_effort"`
				Thinking claudeMainThinkingProof `json:"main_thinking"`
				Helper   struct {
					Sources   []string        `json:"sources"`
					Version   string          `json:"version"`
					Session   string          `json:"session"`
					Model     string          `json:"model"`
					TaskID    string          `json:"task_id"`
					RunID     string          `json:"run_id"`
					ToolUseID string          `json:"tool_use_id"`
					Selected  provider.Effort `json:"selected"`
					Requests  []struct {
						ID        string                `json:"request_id"`
						MaxTokens int                   `json:"max_tokens"`
						Effort    string                `json:"effort"`
						Thinking  claudeRequestThinking `json:"thinking"`
					} `json:"requests"`
				} `json:"subagent_controls"`
			}
			require.NoError(t, json.Unmarshal(raw, &saved))
			// Main proofs must not silently include helper controls or context claims.
			mainIDs := []string{records[0].RequestID}
			assert.Equal(t, mainIDs, saved.Limits.RequestIDs)
			assert.Equal(t, mainIDs, saved.Effort.RequestIDs)
			assert.Equal(t, mainIDs, saved.Thinking.RequestIDs)
			assert.Equal(t, []string{records[0].ClaudeEffort.Level}, saved.Effort.ObservedLevels)
			assert.Equal(t, []claudeRequestThinking{*records[0].ClaudeThinking}, saved.Thinking.Observed)
			helper := saved.Helper
			assert.Equal(t, []string{claudeLimitsSource, claudeEffortSource, claudeThinkingSource, claudeSubagentRoutingSource}, helper.Sources)
			assert.Equal(t, claudeStartupVersion, helper.Version)
			assert.Equal(t, "ses_factory", helper.Session)
			assert.Equal(t, "glm-5.3", helper.Model)
			assert.Equal(t, "agent_one", helper.TaskID)
			assert.Equal(t, "run_one", helper.RunID)
			assert.Equal(t, "tool_agent", helper.ToolUseID)
			assert.Equal(t, tc.selected, helper.Selected)
			require.Len(t, helper.Requests, 1)
			assert.Equal(t, records[1].RequestID, helper.Requests[0].ID)
			assert.Equal(t, 32768, helper.Requests[0].MaxTokens)
			assert.Equal(t, records[1].ClaudeEffort.Level, helper.Requests[0].Effort)
			assert.Equal(t, *records[1].ClaudeThinking, helper.Requests[0].Thinking)
			if tc.selected == provider.EffortDefault {
				assert.Empty(t, saved.Effort.ObservedLevels[0])
				assert.Empty(t, saved.Thinking.Observed[0].Type)
				assert.Equal(t, "high", helper.Requests[0].Effort)
				assert.Equal(t, claudeRequestThinking{Type: "enabled", BudgetTokens: 1024}, helper.Requests[0].Thinking)
			}
			for _, secret := range []string{root, bin, captureFixtureKey, agentMissionPrompt, "verify the files", claudeForeignCredential} {
				assert.NotContains(t, string(raw), secret)
			}
		})
	}
}
