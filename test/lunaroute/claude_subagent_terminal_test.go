package lunaroute_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func claudeTaskStatus(status string) string {
	return `{"type":"system","subtype":"task_updated","session_id":"ses_factory","task_id":"agent_one","run_id":"run_one","patch":{"status":"` + status + `"}}`
}

func TestAgentEvidenceClaudeSubagentTerminalLifecycle(t *testing.T) {
	for _, mode := range []string{"running-before-result", "completed-before-result", "completed-after-result", "metadata-after-result", "running-after-result", "running-after-completed-update", "running-after-notification"} {
		t.Run(mode, func(t *testing.T) {
			lines := claudeSubagentLines()
			notification := `{"type":"system","subtype":"task_notification","session_id":"ses_factory","task_id":"agent_one","run_id":"run_one","tool_use_id":"tool_agent","status":"completed"}`
			valid := true
			switch mode {
			case "running-before-result":
				lines = append(lines[:2], claudeTaskStatus("running"), lines[2])
			case "completed-before-result":
				lines = append(lines[:2], claudeTaskStatus("completed"), lines[2])
			case "completed-after-result":
				lines = append(lines, claudeTaskStatus("completed"), notification)
			case "metadata-after-result":
				lines = append(lines, `{"type":"system","subtype":"task_updated","session_id":"ses_factory","task_id":"agent_one","run_id":"run_one","patch":{"description":"private progress"}}`)
			case "running-after-result":
				lines = append(lines, claudeTaskStatus("running"))
				valid = false
			case "running-after-completed-update":
				lines = append(lines[:2], claudeTaskStatus("completed"), claudeTaskStatus("running"), lines[2])
				valid = false
			case "running-after-notification":
				lines = append(lines[:2], notification, claudeTaskStatus("running"), lines[2])
				valid = false
			}
			proof, err := collectClaudeSubagentLifecycle(lines, "ses_factory", "glm-5.3")
			if !valid {
				require.Error(t, err, "a later success must not erase contradictory task state")
				assert.Empty(t, proof)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "agent_one", proof.TaskID)
			assert.Equal(t, "glm-5.3", proof.HarnessReportedModel)
		})
	}
}

// All launches complete actual authenticated main/helper/title exchanges and
// command-created files. Only the same-launch task-state history differs.
func TestAgentEvidenceClaudeSubagentTerminalPublication(t *testing.T) {
	for _, mode := range []string{"task-completed", "task-running-after-result", "task-running-after-update", "task-running-after-notification"} {
		t.Run(mode, func(t *testing.T) {
			root, bin, capture := agentFactoryFixture(t, "claude-code", "claude-route-title-"+mode)
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
			finished := time.Now().UTC()
			proof, err := os.ReadFile(filepath.Join(ws, agentProofFile))
			require.NoError(t, err)
			require.Equal(t, agentProofText, string(proof))
			command, err := os.ReadFile(filepath.Join(ws, agentCommandFile))
			require.NoError(t, err)
			require.Equal(t, strconv.Itoa(len(agentProofText)), strings.TrimSpace(string(command)))
			require.Zero(t, *calls)
			records := capture.Records()
			require.Len(t, records, 3)
			for _, record := range records {
				require.True(t, record.Completed && record.AuthMatchesSelected)
				require.Equal(t, "glm-5.3", record.RequestedModel)
				require.Equal(t, []string{"glm-5.3"}, record.ReportedModels)
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
			err = runErr
			if err == nil {
				err = finishClaudeIsolation(repo, root, isolation, capture, result, started, finished)
			}
			sink := filepath.Join(repo, evidencePath, "observations/lr05-isolation.json")
			if mode != "task-completed" {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), captureFixtureKey)
				_, statErr := os.Stat(sink)
				require.True(t, os.IsNotExist(statErr), "contradictory lifecycle must write no evidence")
				return
			}
			require.NoError(t, err)
			raw, err := os.ReadFile(sink)
			require.NoError(t, err)
			var saved claudeIsolationObservation
			require.NoError(t, json.Unmarshal(raw, &saved))
			require.Equal(t, "agent_one", saved.Subagent.TaskID)
			require.Equal(t, "run_one", saved.Subagent.RunID)
			require.Equal(t, "glm-5.3", saved.Subagent.HarnessReportedModel)
			require.Len(t, saved.Inference, 3)
			assert.Equal(t, "title", saved.Inference[0].Category)
			assert.Equal(t, "main", saved.Inference[1].Category)
			assert.Equal(t, "subagent", saved.Inference[2].Category)
			for _, dir := range []string{root, capture.dir} {
				_, statErr := os.Stat(dir)
				require.True(t, os.IsNotExist(statErr))
			}
			for _, private := range []string{captureFixtureKey, root, "private progress", "private report"} {
				assert.NotContains(t, string(raw), private)
			}
		})
	}
}
