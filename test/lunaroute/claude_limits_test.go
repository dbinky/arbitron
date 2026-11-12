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

func TestAgentEvidenceClaudeMainLimitValidation(t *testing.T) {
	spec, lines := claudeStartupFixture(t)
	spec.Env = append(spec.Env, "CLAUDE_CODE_MAX_CONTEXT_TOKENS=202752", "CLAUDE_CODE_MAX_OUTPUT_TOKENS=32768")
	lines[2] = strings.Replace(lines[2], `"glm-5.3":{}`, `"glm-5.3":{"contextWindow":202752,"maxOutputTokens":8192}`, 1)
	rules := agentFactoryRules(t)
	proof, err := collectClaudeMainLimits(spec, rules, lines)
	require.NoError(t, err)
	assert.Equal(t, 202752, proof.ContextTokens)
	assert.Equal(t, 32768, proof.RequestedOutputTokens, "usage maxOutputTokens is a catalog default, not the actual request budget")
	assert.Equal(t, "ses_factory", proof.Session)
	assert.Equal(t, "glm-5.3", proof.Model)
	assert.Equal(t, claudeLimitsSource, proof.Source)
	for _, mode := range []string{"missing-rules", "missing-context-env", "context-env-drift", "output-env-drift", "duplicate-env", "missing-usage", "empty-usage", "foreign-usage", "context-drift", "context-null", "context-string", "duplicate-json", "failed-result", "unbounded"} {
		t.Run(mode, func(t *testing.T) {
			actual := spec
			actual.Env = append([]string(nil), spec.Env...)
			events := append([]string(nil), lines...)
			table := rules
			switch mode {
			case "missing-rules":
				table = provider.CapabilityRules{}
			case "missing-context-env":
				actual.Env = actual.Env[:len(actual.Env)-2]
			case "context-env-drift":
				actual.Env[len(actual.Env)-2] = "CLAUDE_CODE_MAX_CONTEXT_TOKENS=202753"
			case "output-env-drift":
				actual.Env[len(actual.Env)-1] = "CLAUDE_CODE_MAX_OUTPUT_TOKENS=32767"
			case "duplicate-env":
				actual.Env = append(actual.Env, "CLAUDE_CODE_MAX_CONTEXT_TOKENS=202752")
			case "missing-usage":
				events[2] = `{"type":"result","is_error":false,"session_id":"ses_factory"}`
			case "empty-usage":
				events[2] = strings.Replace(events[2], `{"glm-5.3":{"contextWindow":202752,"maxOutputTokens":8192}}`, `{}`, 1)
			case "foreign-usage":
				events[2] = strings.ReplaceAll(events[2], "glm-5.3", "glm-5.3-flash")
			case "context-drift":
				events[2] = strings.ReplaceAll(events[2], "202752", "202753")
			case "context-null":
				events[2] = strings.ReplaceAll(events[2], "202752", "null")
			case "context-string":
				events[2] = strings.ReplaceAll(events[2], "202752", `"202752"`)
			case "duplicate-json":
				events[2] = strings.Replace(events[2], `"contextWindow":`, `"contextWindow":1,"contextWindow":`, 1)
			case "failed-result":
				events[2] = strings.ReplaceAll(events[2], `"is_error":false`, `"is_error":true`)
			case "unbounded":
				actual.MaxResponseBytes = 0
			}
			_, err := collectClaudeMainLimits(actual, table, events)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), captureFixtureKey)
		})
	}
}

// Real Factory/env/runner, authenticated CONNECT/TLS, command-created files and
// the private JSON sink. Protocol processes are not installed control evidence.
func TestAgentEvidenceClaudeLimitsFactorySink(t *testing.T) {
	for _, mode := range []string{"success", "limits-context-absent", "limits-context-drift", "limits-context-secret", "limits-output-absent", "limits-output-drift", "missing-context-feed", "missing-budget-feed"} {
		t.Run(mode, func(t *testing.T) {
			root, bin, capture := agentFactoryFixture(t, "claude-code", mode)
			ws := filepath.Join(root, "mission")
			require.NoError(t, os.Mkdir(ws, 0700))
			isolation, err := seedClaudeIsolation(filepath.Join(root, "home"), ws)
			require.NoError(t, err)
			t.Setenv("HOME", isolation.home)
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			require.NoError(t, os.Unsetenv("CLAUDE_CONFIG_DIR"))
			client, calls := agentFactoryProbeClient(t)
			factory := liveClaudeIsolationFactoryWithClient(root, bin, agentFactoryRules(t), client, isolation)
			started := time.Now().UTC()
			result, missionErr := executePreparedAgentMission(context.Background(), factory, liveBinding(captureFixtureKey, "glm-5.3", "claude-code"), ws)
			records := capture.Records()
			require.Len(t, records, 1)
			assert.True(t, records[0].Completed && records[0].AuthMatchesSelected)
			assert.Equal(t, []string{"glm-5.3"}, records[0].ReportedModels)
			actualFile, err := os.ReadFile(filepath.Join(ws, agentProofFile))
			require.NoError(t, err)
			assert.Equal(t, agentProofText, string(actualFile), "refusal must not be an accidental failure before actual mission execution")
			assert.Zero(t, *calls)
			if strings.HasPrefix(mode, "limits-context-") {
				require.Error(t, missionErr)
			} else {
				require.NoError(t, missionErr)
			}
			switch mode {
			case "missing-context-feed":
				isolation.limits = claudeMainLimitProof{}
			case "missing-budget-feed":
				records[0].MaxTokens = 0
			}
			observation, err := collectClaudeIsolationEvidence(isolation, records, result, started, time.Now().UTC())
			if err == nil {
				_, err = saveObservation(root, captureFixtureKey, "claude-limits.json", observation)
			}
			path := filepath.Join(root, evidencePath, "observations/claude-limits.json")
			if mode != "success" {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), captureFixtureKey)
				_, statErr := os.Stat(path)
				require.True(t, os.IsNotExist(statErr))
				return
			}
			require.NoError(t, err)
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			var saved claudeIsolationObservation
			require.NoError(t, json.Unmarshal(raw, &saved))
			assert.Equal(t, 202752, saved.Limits.ContextTokens)
			assert.Equal(t, 32768, saved.Limits.RequestedOutputTokens)
			assert.Equal(t, "ses_factory", saved.Limits.Session)
			assert.Equal(t, claudeLimitsSource, saved.Limits.Source)
			assert.Equal(t, []string{records[0].RequestID}, saved.Limits.RequestIDs)
			assert.Equal(t, 32768, saved.Inference[0].MaxTokens)
			for _, private := range []string{root, bin, captureFixtureKey, agentMissionPrompt, claudeForeignCredential} {
				assert.NotContains(t, string(raw), private)
			}
		})
	}
}
