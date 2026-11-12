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

func TestAgentEvidenceClaudeSubagentMissionInput(t *testing.T) {
	binding := liveBinding(captureFixtureKey, "glm-5.3", "claude-code")
	assert.Contains(t, binding.AllowedTools, "Agent")
	prompt := agentMissionPromptFor("claude-code")
	assert.Contains(t, prompt, "general-purpose")
	assert.Contains(t, prompt, "run_in_background=false")
	assert.Contains(t, prompt, "read and verify both files")
}

func TestAgentEvidenceClaudeSubagentSelection(t *testing.T) {
	spec, _ := claudeStartupFixture(t)
	for _, mode := range []string{"exact", "missing", "drift", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			actual := spec
			actual.Env = append([]string(nil), spec.Env...)
			if mode != "missing" {
				value := "glm-5.3"
				if mode == "drift" {
					value = "glm-5.3-flash"
				}
				actual.Env = append(actual.Env, "CLAUDE_CODE_SUBAGENT_MODEL="+value)
			}
			if mode == "duplicate" {
				actual.Env = append(actual.Env, "CLAUDE_CODE_SUBAGENT_MODEL=glm-5.3")
			}
			err := collectClaudeSubagentSelection(actual)
			if mode == "exact" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func claudeSubagentLines() []string {
	return []string{
		`{"type":"assistant","session_id":"ses_factory","message":{"content":[{"type":"tool_use","id":"tool_agent","name":"Agent","input":{"subagent_type":"general-purpose","prompt":"verify the files","run_in_background":false}}]}}`,
		`{"type":"system","subtype":"task_started","session_id":"ses_factory","task_id":"agent_one","run_id":"run_one","tool_use_id":"tool_agent","task_type":"local_agent","subagent_type":"general-purpose","is_backgrounded":false,"spawn_depth":1}`,
		`{"type":"user","session_id":"ses_factory","message":{"content":[{"type":"tool_result","tool_use_id":"tool_agent","content":"private report"}]},"tool_use_result":{"status":"completed","agentId":"agent_one","agentType":"general-purpose","resolvedModel":"glm-5.3","totalDurationMs":10,"totalTokens":12,"totalToolUseCount":2}}`,
	}
}

func TestAgentEvidenceClaudeSubagentLifecycle(t *testing.T) {
	for _, mode := range []string{"exact", "missing", "no-call", "no-start", "no-result", "reordered", "duplicate-start", "duplicate-result", "wrong-tool", "legacy-task", "foreign-session", "foreign-agent", "foreign-model", "model-swap", "background", "async-result", "failed-tool", "nested", "historical", "withheld", "unsafe-id", "fractional-usage", "malformed", "duplicate-json", "oversized", "background-transition", "failed-update", "foreign-run-update", "failed-notification", "missing-result-block"} {
		t.Run(mode, func(t *testing.T) {
			lines := claudeSubagentLines()
			switch mode {
			case "background-transition":
				lines = append(lines, `{"type":"system","subtype":"task_updated","session_id":"ses_factory","task_id":"agent_one","run_id":"run_one","patch":{"is_backgrounded":true}}`)
			case "failed-update":
				lines = append(lines, `{"type":"system","subtype":"task_updated","session_id":"ses_factory","task_id":"agent_one","run_id":"run_one","patch":{"status":"failed"}}`)
			case "foreign-run-update":
				lines = append(lines, `{"type":"system","subtype":"task_updated","session_id":"ses_factory","task_id":"agent_one","run_id":"run_other","patch":{"status":"completed"}}`)
			case "failed-notification":
				lines = append(lines, `{"type":"system","subtype":"task_notification","session_id":"ses_factory","task_id":"agent_one","run_id":"run_one","tool_use_id":"tool_agent","status":"failed"}`)
			case "missing-result-block":
				lines = append(lines, `{"type":"user","session_id":"ses_factory","tool_use_result":{"agentId":"agent_other","status":"completed"}}`)
			case "missing":
				lines = nil
			case "no-call":
				lines = lines[1:]
			case "no-start":
				lines = append(lines[:1], lines[2])
			case "no-result":
				lines = lines[:2]
			case "reordered":
				lines[0], lines[1] = lines[1], lines[0]
			case "duplicate-start":
				lines = append(lines[:2], lines[1], lines[2])
			case "duplicate-result":
				lines = append(lines, lines[2])
			case "legacy-task":
				lines[0] = strings.Replace(lines[0], `"Agent"`, `"Task"`, 1)
			case "wrong-tool":
				lines[0] = strings.Replace(lines[0], `"Agent"`, `"Bash"`, 1)
			case "foreign-session":
				lines[1] = strings.Replace(lines[1], "ses_factory", "ses_foreign", 1)
			case "foreign-agent":
				lines[2] = strings.Replace(lines[2], "agent_one", "agent_other", 1)
			case "foreign-model":
				lines[2] = strings.Replace(lines[2], "glm-5.3", "glm-5.3-flash", 1)
			case "model-swap":
				lines[2] = strings.Replace(lines[2], `"totalTokens":12`, `"modelsUsed":["glm-5.3","glm-5.3-flash"],"totalTokens":12`, 1)
			case "background":
				lines[1] = strings.Replace(lines[1], `"is_backgrounded":false`, `"is_backgrounded":true`, 1)
			case "async-result":
				lines[2] = strings.Replace(lines[2], `"completed"`, `"async_launched"`, 1)
			case "failed-tool":
				lines[2] = strings.Replace(lines[2], `"type":"tool_result"`, `"type":"tool_result","is_error":true`, 1)
			case "nested":
				lines[1] = strings.Replace(lines[1], `"spawn_depth":1`, `"spawn_depth":2,"parent_task_id":"parent_one"`, 1)
			case "historical":
				lines[0] = strings.Replace(lines[0], `"type":"assistant"`, `"type":"assistant","historical":true`, 1)
			case "withheld":
				lines[2] = strings.Replace(lines[2], `"status":"completed"`, `"status":"completed","handback":"withheld"`, 1)
			case "unsafe-id":
				lines[1] = strings.Replace(lines[1], "run_one", captureFixtureKey, 1)
			case "fractional-usage":
				lines[2] = strings.Replace(lines[2], `"totalTokens":12`, `"totalTokens":12.5`, 1)
			case "malformed":
				lines[1] = `{`
			case "duplicate-json":
				lines[1] = strings.Replace(lines[1], `"task_id":"agent_one"`, `"task_id":"other","task_id":"agent_one"`, 1)
			case "oversized":
				lines = []string{strings.Repeat("x", captureBodyLimit+1)}
			}
			proof, err := collectClaudeSubagentLifecycle(lines, "ses_factory", "glm-5.3")
			if mode != "exact" {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), captureFixtureKey)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, claudeSubagentSource, proof.Source)
			assert.Equal(t, claudeStartupVersion, proof.Version)
			assert.Equal(t, "ses_factory", proof.Session)
			assert.Equal(t, "agent_one", proof.TaskID)
			assert.Equal(t, "run_one", proof.RunID)
			assert.Equal(t, "tool_agent", proof.ToolUseID)
			assert.Equal(t, "glm-5.3", proof.HarnessReportedModel)
		})
	}
}

func TestAgentEvidenceClaudeSubagentFactorySink(t *testing.T) {
	for _, mode := range []string{"success", "subagent-absent", "subagent-background", "subagent-foreign-model", "subagent-failed", "subagent-foreign-session", "subagent-no-result-feed"} {
		t.Run(mode, func(t *testing.T) {
			root, bin, capture := agentFactoryFixture(t, "claude-code", mode)
			ws := filepath.Join(root, "mission")
			require.NoError(t, os.Mkdir(ws, 0700))
			isolation, err := seedClaudeIsolation(filepath.Join(root, "home"), ws)
			require.NoError(t, err)
			t.Setenv("HOME", isolation.home)
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			require.NoError(t, os.Unsetenv("CLAUDE_CONFIG_DIR"))
			binding := liveBinding(captureFixtureKey, "glm-5.3", "claude-code")
			client, calls := agentFactoryProbeClient(t)
			factory := liveClaudeIsolationFactoryWithClient(root, bin, agentFactoryRules(t), client, isolation, capture, binding.Effort)
			started := time.Now().UTC()
			result, runErr := executePreparedAgentMission(context.Background(), factory, binding, ws)
			proofFile, err := os.ReadFile(filepath.Join(ws, agentProofFile))
			require.NoError(t, err)
			assert.Equal(t, agentProofText, string(proofFile))
			records := capture.Records()
			require.Len(t, records, 1)
			assert.True(t, records[0].Completed && records[0].AuthMatchesSelected)
			assert.Zero(t, *calls)
			err = runErr
			if err == nil {
				if mode == "subagent-no-result-feed" {
					isolation.subagent = claudeSubagentProof{}
				}
				var observation claudeIsolationObservation
				observation, err = collectClaudeIsolationEvidence(isolation, records, result, started, time.Now().UTC())
				if err == nil {
					_, err = saveObservation(root, captureFixtureKey, "claude-subagent.json", observation)
				}
			}
			path := filepath.Join(root, evidencePath, "observations/claude-subagent.json")
			if mode != "success" {
				require.Error(t, err)
				_, statErr := os.Stat(path)
				assert.True(t, os.IsNotExist(statErr))
				return
			}
			require.NoError(t, err)
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			var saved claudeIsolationObservation
			require.NoError(t, json.Unmarshal(raw, &saved))
			assert.Equal(t, "agent_one", saved.Subagent.TaskID)
			assert.Equal(t, "tool_agent", saved.Subagent.ToolUseID)
			assert.Equal(t, "run_one", saved.Subagent.RunID)
			assert.Equal(t, "ses_factory", saved.Subagent.Session)
			assert.Equal(t, claudeStartupVersion, saved.Subagent.Version)
			assert.Equal(t, claudeSubagentSource, saved.Subagent.Source)
			assert.Equal(t, "glm-5.3", saved.Subagent.HarnessReportedModel)
			assert.Equal(t, "main", saved.Inference[0].Category, "lifecycle intent is not helper gateway evidence")
			for _, secret := range []string{root, bin, captureFixtureKey, agentMissionPrompt, "private report", claudeForeignCredential} {
				assert.NotContains(t, string(raw), secret)
			}
		})
	}
}
