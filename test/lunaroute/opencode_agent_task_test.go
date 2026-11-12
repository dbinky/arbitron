package lunaroute_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentEvidenceOpenCodeAgentEntryPrerequisites(t *testing.T) {
	bin, err := os.Executable()
	require.NoError(t, err)
	for _, release := range []string{"0", "1"} {
		ctx, cancel := context.WithTimeout(context.Background(), agentVersionTimeout)
		cmd := exec.CommandContext(ctx, bin, "-test.run=^TestLiveOpencodeAgentTask$", "-test.v")
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "ARBITRON_LUNAROUTE_E2E_KEY=") && !strings.HasPrefix(entry, "ARBITRON_LUNAROUTE_RELEASE_GATE=") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, "ARBITRON_LUNAROUTE_RELEASE_GATE="+release)
		out, runErr := cmd.CombinedOutput()
		contextErr := ctx.Err()
		cancel()
		require.NoError(t, contextErr, "entry prerequisite check must be bounded without inference")
		if release == "0" {
			require.NoError(t, runErr)
			assert.Contains(t, string(out), "--- SKIP: TestLiveOpencodeAgentTask")
		} else {
			require.Error(t, runErr)
			assert.Contains(t, string(out), "--- FAIL: TestLiveOpencodeAgentTask")
		}
		assert.Contains(t, string(out), "release acceptance remains pending")
	}
}

// Protocol executables exercise the same finalization used by the operator
// entry point. They do not establish installed-backend support or readiness.
func TestAgentEvidenceOpenCodeCompletedAgentPublication(t *testing.T) {
	for _, mode := range []string{"success", "empty-capture", "missing-controls", "missing-isolation", "missing-tool", "missing-source", "unreadable-source", "source-limit-drift", "source-model-missing", "source-malformed", "source-tools-missing", "source-tools-null", "source-tools-false", "source-tools-string", "source-tools-duplicate", "controls-tools-missing", "foreign-source", "unsafe-model-source", "missing-file", "cleanup-failure"} {
		t.Run(mode, func(t *testing.T) {
			root, bin, capture := agentFactoryFixture(t, "opencode", "correlation-force-success")
			home := filepath.Join(root, "home")
			isolation, err := seedOpenCodeIsolation(home, root)
			require.NoError(t, err)
			t.Setenv("HOME", home)
			ws := filepath.Join(root, "mission")
			require.NoError(t, os.Mkdir(ws, 0700))
			started := time.Now().UTC()
			journal := &openCodeControlJournal{isolation: isolation}
			client, _ := agentFactoryProbeClient(t)
			factory := liveObservedCompactingFactoryWithClient(root, bin, agentFactoryRules(t), client, capture, []byte(forcingSnapshot), journal)
			result, err := executeAgentMission(context.Background(), factory, liveBinding(captureFixtureKey, "glm-5.3", "opencode"), ws)
			require.NoError(t, err)
			require.Len(t, capture.Records(), 4, "refusal cases must first complete the mission and real helper exchanges")
			finished := time.Now().UTC()
			repo := t.TempDir()
			for _, source := range []string{openCodeControlsSource, openCodeCompactionSource, openCodeSubagentSource, openCodeTitleSource, openCodeIsolationSource, evidencePath + "/sources/lr02-live-models-2026-11-11.json"} {
				raw, err := readRepoFile(repoRoot(t), source)
				require.NoError(t, err)
				if source == evidencePath+"/sources/lr02-live-models-2026-11-11.json" {
					raw = []byte(forcingSnapshot) // source fixture matches the actual Factory/driver inputs
				}
				path := filepath.Join(repo, source)
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
				require.NoError(t, os.WriteFile(path, raw, 0600))
			}
			switch mode {
			case "empty-capture":
				capture.mu.Lock()
				capture.records = nil
				capture.mu.Unlock()
			case "missing-controls":
				journal.entries = nil
			case "missing-isolation":
				journal.isolation = nil
			case "missing-tool":
				result.Steps = nil
			case "missing-source":
				require.NoError(t, os.Remove(filepath.Join(repo, openCodeTitleSource)))
			case "unreadable-source":
				require.NoError(t, os.Chmod(filepath.Join(repo, openCodeTitleSource), 0000))
			case "source-limit-drift", "source-model-missing", "source-malformed":
				data := strings.ReplaceAll(forcingSnapshot, "202752", "202753")
				if mode == "source-model-missing" {
					data = strings.ReplaceAll(forcingSnapshot, "glm-5.3", "glm-5.3-flash")
				} else if mode == "source-malformed" {
					data = "{"
				}
				require.NoError(t, os.WriteFile(filepath.Join(repo, evidencePath, "sources/lr02-live-models-2026-11-11.json"), []byte(data), 0600))
			case "source-tools-missing", "source-tools-null", "source-tools-false", "source-tools-string", "source-tools-duplicate":
				tools := map[string]string{
					"source-tools-missing":   `{}`,
					"source-tools-null":      `{"tools":null}`,
					"source-tools-false":     `{"tools":false}`,
					"source-tools-string":    `{"tools":"true"}`,
					"source-tools-duplicate": `{"tools":false,"tools":true}`,
				}
				data := strings.Replace(forcingSnapshot, `"capabilities":{"tools":true}`, `"capabilities":`+tools[mode], 1)
				require.NotEqual(t, forcingSnapshot, data, "negative source fixture must actually remove or contradict the required fact")
				require.NoError(t, os.WriteFile(filepath.Join(repo, evidencePath, "sources/lr02-live-models-2026-11-11.json"), []byte(data), 0600))
			case "controls-tools-missing":
				// Simulate losing only this validated control on its way to the sink.
				for i := range journal.entries {
					journal.entries[i].Controls.ToolCall = false
				}
			case "cleanup-failure":
				require.NoError(t, os.Chmod(root, 0500))
				t.Cleanup(func() { require.NoError(t, os.Chmod(root, 0700)) })
			case "foreign-source":
				capture.mu.Lock()
				capture.records[1].Operation.Source = openCodeTitleSource
				capture.mu.Unlock()
			case "unsafe-model-source":
				result.ModelEvidence[0].Source = captureFixtureKey
			case "missing-file":
				require.NoError(t, os.Remove(filepath.Join(ws, agentProofFile)))
			}
			err = finishOpenCodeAgentTask(repo, root, ws, capture, journal, result, started, finished)
			supportPath := filepath.Join(repo, evidencePath, "observations/lr06-opencode-compaction.json")
			agentPath := filepath.Join(repo, evidencePath, "observations/lr06-opencode-agent.json")
			if mode != "success" {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), captureFixtureKey)
				for _, path := range []string{supportPath, agentPath} {
					_, statErr := os.Stat(path)
					require.True(t, os.IsNotExist(statErr), "invalid inputs must publish neither artifact")
				}
				return
			}
			require.NoError(t, err)
			_, err = os.Stat(root)
			require.True(t, os.IsNotExist(err), "publication requires actual experiment cleanup")
			_, err = os.Stat(capture.dir)
			require.True(t, os.IsNotExist(err), "publication requires actual proxy/CA cleanup")
			raw, err := os.ReadFile(agentPath)
			require.NoError(t, err)
			var agent agentTaskObservation
			require.NoError(t, json.Unmarshal(raw, &agent))
			assert.True(t, agent.Completed && agent.FileOK && agent.CommandOK)
			assert.Equal(t, "glm-5.3", agent.Model)
			assert.Equal(t, "1.19.5", agent.Version)
			assert.Equal(t, "opencode", agent.Backend)
			assert.Equal(t, "ses_factory", agent.SessionID)
			assert.Contains(t, agent.StepKinds, "tool_call")
			require.NotEmpty(t, agent.ModelEvidence)
			assert.Equal(t, "glm-5.3", agent.ModelEvidence[0].Model)
			assert.Equal(t, evidencePath+"/observations/lr06-opencode-compaction.json", agent.ConfigRef)
			supportRaw, err := os.ReadFile(filepath.Join(repo, agent.ConfigRef))
			require.NoError(t, err)
			var support openCodeSupportingObservation
			require.NoError(t, json.Unmarshal(supportRaw, &support))
			assert.Equal(t, agent.RunID, support.RunID)
			assert.Equal(t, agent.SessionID, support.SessionID)
			assert.Equal(t, agent.Inference, support.Inference)
			assert.Equal(t, []string{"main", "subagent", "title", "compaction"}, agent.RequiredCategories)
			require.Len(t, support.Controls, 2)
			var persisted struct {
				Controls []struct {
					Values map[string]any `json:"controls"`
				} `json:"effective_controls"`
			}
			require.NoError(t, json.Unmarshal(supportRaw, &persisted))
			require.Len(t, persisted.Controls, 2)
			for _, check := range persisted.Controls {
				assert.Equal(t, true, check.Values["tool_call"], "actual resolved tool support must reach the final artifact")
			}
			assert.True(t, support.Isolation.ConfigUnchanged && support.Isolation.AuthUnchanged)
			for _, record := range agent.Inference {
				assert.Equal(t, "glm-5.3", record.RequestedModel)
				assert.Equal(t, []string{"glm-5.3"}, record.ReportedModels)
				assert.True(t, record.Completed && record.AuthMatchesSelected)
			}
			for _, private := range []string{captureFixtureKey, root, home, bin, "Private compaction summary", agentMissionPromptFor("opencode")} {
				assert.NotContains(t, string(raw)+string(supportRaw), private)
			}
		})
	}
}
