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

func TestAgentEvidenceClaudeSubagentPatchSchema(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		valid       bool
	}{
		{"empty", `,"patch":{}`, true},
		{"running", `,"patch":{"status":"running"}`, true},
		{"foreground", `,"patch":{"is_backgrounded":false}`, true},
		{"metadata", `,"patch":{"description":"private progress"}`, true},
		{"missing", ``, false},
		{"null", `,"patch":null`, false},
		{"array", `,"patch":[]`, false},
		{"string", `,"patch":"completed"`, false},
		{"null-status", `,"patch":{"status":null}`, false},
		{"empty-status", `,"patch":{"status":""}`, false},
		{"numeric-status", `,"patch":{"status":1}`, false},
		{"null-background", `,"patch":{"is_backgrounded":null}`, false},
		{"string-background", `,"patch":{"is_backgrounded":"false"}`, false},
		{"duplicate-status", `,"patch":{"status":"failed","status":"running"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines := claudeSubagentLines()
			update := `{"type":"system","subtype":"task_updated","session_id":"ses_factory","task_id":"agent_one","run_id":"run_one"` + tc.field + `}`
			lines = append(lines[:2], update, lines[2])
			proof, err := collectClaudeSubagentLifecycle(lines, "ses_factory", "glm-5.3")
			if !tc.valid {
				require.Error(t, err, "malformed patch must not become an empty successful update")
				assert.Empty(t, proof)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "agent_one", proof.TaskID)
			assert.Equal(t, "glm-5.3", proof.HarnessReportedModel)
		})
	}
}

// The executable completes authenticated main/helper/title inference and creates
// both files before emitting the patch. Refusal must reach the private sink.
func TestAgentEvidenceClaudeSubagentPatchPublication(t *testing.T) {
	for _, mode := range []string{"empty", "running", "foreground", "missing", "null", "null-status", "empty-status", "null-background"} {
		t.Run(mode, func(t *testing.T) {
			root, bin, capture := agentFactoryFixture(t, "claude-code", "claude-route-title-patch-"+mode)
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
			command, err := workspaceProof(ws, agentCommandFile, agentProofLimit, started, finished)
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
			if mode != "empty" && mode != "running" && mode != "foreground" {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), captureFixtureKey)
				_, statErr := os.Stat(sink)
				require.True(t, os.IsNotExist(statErr), "malformed lifecycle must write no evidence")
				return
			}
			require.NoError(t, err)
			raw, err := os.ReadFile(sink)
			require.NoError(t, err)
			var saved claudeIsolationObservation
			require.NoError(t, json.Unmarshal(raw, &saved))
			assert.Equal(t, "agent_one", saved.Subagent.TaskID)
			assert.Equal(t, "run_one", saved.Subagent.RunID)
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
