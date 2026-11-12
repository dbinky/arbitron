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

// Synthetic source facts match the distinctive actual Factory input. These
// protocol processes prove publication plumbing, never installed LR-05 support.
const claudePublicationSnapshot = `{"models":[{"id":"glm-5.3","context_window":202752,"max_output_tokens":32768}]}`

func TestAgentEvidenceClaudeSourceBoundPublication(t *testing.T) {
	for _, mode := range []string{"success", "missing-source", "unreadable-source", "source-context-drift", "source-output-drift", "source-model-missing", "source-malformed", "empty-capture", "missing-limits", "missing-lifecycle", "missing-tool", "missing-file", "unsafe-record", "cleanup-failure", "destination-in-experiment", "home-outside-experiment"} {
		t.Run(mode, func(t *testing.T) {
			root, bin, capture := agentFactoryFixture(t, "claude-code", "claude-route-controls")
			ws := filepath.Join(root, "mission")
			require.NoError(t, os.Mkdir(ws, 0700))
			home := filepath.Join(root, "home")
			if mode == "home-outside-experiment" {
				home = filepath.Join(t.TempDir(), "home")
			}
			isolation, err := seedClaudeIsolation(home, ws)
			require.NoError(t, err)
			t.Setenv("HOME", isolation.home)
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			require.NoError(t, os.Unsetenv("CLAUDE_CONFIG_DIR"))
			client, calls := agentFactoryProbeClient(t)
			factory := liveClaudeIsolationFactoryWithClient(root, bin, agentFactoryRules(t), client, isolation, capture, "")
			started := time.Now().UTC()
			result, err := executePreparedAgentMission(context.Background(), factory, liveBinding(captureFixtureKey, "glm-5.3", "claude-code"), ws)
			require.NoError(t, err)
			finished := time.Now().UTC()
			require.Len(t, capture.Records(), 2)
			proof, err := os.ReadFile(filepath.Join(ws, agentProofFile))
			require.NoError(t, err)
			require.Equal(t, agentProofText, string(proof))
			assert.Zero(t, *calls)

			repo := t.TempDir()
			if mode == "destination-in-experiment" {
				repo = root
			}
			for _, source := range []string{claudeIsolationSource, claudeLimitsSource, claudeMemorySource, claudeEffortSource, claudeThinkingSource, claudeSubagentSource, claudeSubagentRoutingSource, claudeInputSource} {
				raw, err := readRepoFile(repoRoot(t), source)
				require.NoError(t, err)
				if source == claudeInputSource {
					raw = []byte(claudePublicationSnapshot)
				}
				path := filepath.Join(repo, source)
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
				require.NoError(t, os.WriteFile(path, raw, 0600))
			}
			switch mode {
			case "missing-source":
				require.NoError(t, os.Remove(filepath.Join(repo, claudeLimitsSource)))
			case "unreadable-source":
				require.NoError(t, os.Chmod(filepath.Join(repo, claudeInputSource), 0000))
			case "source-context-drift", "source-output-drift", "source-model-missing", "source-malformed":
				raw := strings.ReplaceAll(claudePublicationSnapshot, "202752", "202753")
				if mode == "source-output-drift" {
					raw = strings.ReplaceAll(claudePublicationSnapshot, "32768", "32769")
				} else if mode == "source-model-missing" {
					raw = strings.ReplaceAll(claudePublicationSnapshot, "glm-5.3", "glm-5.3-flash")
				} else if mode == "source-malformed" {
					raw = "{"
				}
				require.NoError(t, os.WriteFile(filepath.Join(repo, claudeInputSource), []byte(raw), 0600))
			case "empty-capture":
				capture.mu.Lock()
				capture.records = nil
				capture.mu.Unlock()
			case "missing-limits":
				isolation.limits = claudeMainLimitProof{}
			case "missing-lifecycle":
				isolation.subagent = claudeSubagentProof{}
			case "missing-tool":
				result.Steps = nil
			case "missing-file":
				require.NoError(t, os.Remove(filepath.Join(ws, agentCommandFile)))
			case "unsafe-record":
				capture.mu.Lock()
				capture.records[0].Connection = captureFixtureKey
				capture.mu.Unlock()
			case "cleanup-failure":
				require.NoError(t, os.Chmod(root, 0500))
				t.Cleanup(func() { require.NoError(t, os.Chmod(root, 0700)) })
			}
			err = finishClaudeIsolation(repo, root, isolation, capture, result, started, finished)
			sink := filepath.Join(repo, evidencePath, "observations/lr05-isolation.json")
			if mode != "success" {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), captureFixtureKey)
				_, statErr := os.Stat(sink)
				require.True(t, os.IsNotExist(statErr), "refusal must not publish an observation")
				return
			}
			require.NoError(t, err)
			for _, dir := range []string{root, capture.dir} {
				_, err := os.Stat(dir)
				require.True(t, os.IsNotExist(err), "cleanup must precede publication")
			}
			raw, err := os.ReadFile(sink)
			require.NoError(t, err)
			var saved claudeIsolationObservation
			require.NoError(t, json.Unmarshal(raw, &saved))
			assert.Equal(t, claudeInputSource, saved.InputSource)
			assert.Equal(t, 202752, saved.Limits.ContextTokens)
			assert.Equal(t, 32768, saved.Limits.RequestedOutputTokens)
			assert.Equal(t, "ses_factory", saved.Session)
			assert.Equal(t, claudeStartupVersion, saved.Version)
			require.Len(t, saved.Inference, 2)
			assert.Equal(t, "main", saved.Inference[0].Category)
			assert.Equal(t, "subagent", saved.Inference[1].Category)
			mainIDs := []string{saved.Inference[0].RequestID}
			assert.Equal(t, mainIDs, saved.Limits.RequestIDs)
			assert.Equal(t, mainIDs, saved.Effort.RequestIDs)
			assert.Equal(t, mainIDs, saved.Thinking.RequestIDs)
			require.NotNil(t, saved.SubagentControls)
			require.Len(t, saved.SubagentControls.Requests, 1)
			request := saved.SubagentControls.Requests[0]
			assert.Equal(t, saved.Inference[1].RequestID, request.RequestID)
			assert.Equal(t, 32768, request.MaxTokens)
			assert.Equal(t, "high", request.Effort)
			assert.Equal(t, claudeRequestThinking{Type: "enabled", BudgetTokens: 1024}, request.Thinking)
			assert.Equal(t, saved.Subagent.TaskID, saved.SubagentControls.TaskID)
			assert.Equal(t, saved.Subagent.RunID, saved.SubagentControls.RunID)
			assert.Equal(t, saved.Subagent.ToolUseID, saved.SubagentControls.ToolUseID)
			for _, record := range saved.Inference {
				assert.Equal(t, "glm-5.3", record.RequestedModel)
				assert.Equal(t, []string{"glm-5.3"}, record.ReportedModels)
				assert.Equal(t, 32768, record.MaxTokens)
				assert.True(t, record.Completed && record.AuthMatchesSelected)
			}
			for _, private := range []string{root, bin, captureFixtureKey, agentMissionPrompt, claudeForeignCredential} {
				assert.NotContains(t, string(raw), private)
			}
		})
	}
}

func TestAgentEvidenceClaudeInputSourceValidation(t *testing.T) {
	proof := claudeMainLimitProof{Model: "glm-5.3", ContextTokens: 202752, RequestedOutputTokens: 32768}
	for _, raw := range []string{
		"", "{", "null", `[]`, `{}`, `{"models":null}`, `{"models":[]}`,
		strings.ReplaceAll(claudePublicationSnapshot, "glm-5.3", "glm-5.3-flash"),
		strings.ReplaceAll(claudePublicationSnapshot, "202752", "0"),
		strings.ReplaceAll(claudePublicationSnapshot, "32768", "-1"),
		strings.ReplaceAll(claudePublicationSnapshot, "202752", `"202752"`),
		strings.ReplaceAll(claudePublicationSnapshot, "32768", "null"),
		strings.Replace(claudePublicationSnapshot, `"context_window":`, `"context_window":1,"context_window":`, 1),
		strings.Replace(claudePublicationSnapshot, `}]`, `},{"id":"glm-5.3","context_window":202752,"max_output_tokens":32768}]`, 1),
		strings.Repeat(" ", captureBodyLimit+1) + claudePublicationSnapshot,
	} {
		require.Error(t, validateClaudeIsolationInputSource([]byte(raw), proof), "invalid source must not be accepted")
	}
	require.NoError(t, validateClaudeIsolationInputSource([]byte(claudePublicationSnapshot), proof))
	for _, missing := range []claudeMainLimitProof{
		{ContextTokens: 202752, RequestedOutputTokens: 32768},
		{Model: "glm-5.3", RequestedOutputTokens: 32768},
		{Model: "glm-5.3", ContextTokens: 202752},
	} {
		require.Error(t, validateClaudeIsolationInputSource([]byte(claudePublicationSnapshot), missing))
	}
	actual, err := readRepoFile(repoRoot(t), claudeInputSource)
	require.NoError(t, err)
	proof.ContextTokens, proof.RequestedOutputTokens = 524288, 131072
	require.NoError(t, validateClaudeIsolationInputSource(actual, proof), "the genuine operator source schema must be consumed")
}
