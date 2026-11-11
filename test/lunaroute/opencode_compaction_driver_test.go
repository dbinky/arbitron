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

	"github.com/arbitron/arbitron/adapter/claudecli"
	adapterconfig "github.com/arbitron/arbitron/adapter/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"strconv"
)

const forcingSnapshot = `{"models":[{"id":"glm-5.3","context_window":202752,"max_input_tokens":202752,"max_output_tokens":32768}]}`

func TestAgentEvidenceOpenCodeCompactionProductionInputs(t *testing.T) {
	snapshot, err := readRepoFile(repoRoot(t), evidencePath+"/sources/lr02-live-models-2026-11-11.json")
	require.NoError(t, err)
	rules, err := adapterconfig.LoadCapabilityRules("")
	require.NoError(t, err)
	require.Len(t, rules.LunaRouteModels(), 3)
	for _, model := range rules.LunaRouteModels() {
		overlay := strings.NewReplacer("glm-5.3", model.ID, "202752", strconv.Itoa(model.ContextTokens.Tokens), "32768", strconv.Itoa(model.OutputTokens.Tokens)).Replace(controlsOverlay)
		spec := claudecli.RunSpec{Env: []string{"LUNAROUTE_API_KEY=" + captureFixtureKey, "OPENCODE_CONFIG_CONTENT=" + overlay}}
		forced, err := openCodeCompactionSpec(spec, snapshot, 2048)
		require.NoError(t, err)
		raw, err := openCodeForcingControls(forced)
		require.NoError(t, err)
		var cfg struct {
			Compaction struct {
				Reserved int `json:"reserved"`
			} `json:"compaction"`
		}
		require.NoError(t, json.Unmarshal(raw, &cfg))
		assert.Equal(t, model.ContextTokens.Tokens-2048, cfg.Compaction.Reserved, "R2 records input=context for every release id")
	}
}

func TestAgentEvidenceOpenCodeCompactionInstalledSchema(t *testing.T) {
	bin, err := exec.LookPath("opencode")
	if err != nil {
		t.Skip("installed compaction schema unavailable; verification remains pending")
	}
	root := t.TempDir()
	spec := claudecli.RunSpec{Bin: bin, Args: []string{"debug", "config", "--pure"}, Dir: root, Env: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "OPENCODE_DISABLE_MODELS_FETCH=true", "LUNAROUTE_API_KEY=" + captureFixtureKey, "OPENCODE_CONFIG_CONTENT=" + controlsOverlay}, MaxResponseBytes: openCodeControlsLimit}
	forced, err := openCodeCompactionSpec(spec, []byte(forcingSnapshot), 2048)
	require.NoError(t, err)
	lines, err := runCapturedLines(context.Background(), claudecli.NewExecRunner(), forced)
	require.NoError(t, err)
	emitted, err := openCodeForcingControls(forced)
	require.NoError(t, err)
	require.NoError(t, validateOpenCodeCompactionControls(emitted, []byte(strings.Join(lines, "\n"))))
}

func TestAgentEvidenceOpenCodeCompactionForcingControls(t *testing.T) {
	spec := claudecli.RunSpec{Bin: "/same/binary", Args: []string{"export", "ses_factory", "--pure"}, Dir: "/same/workspace", Env: []string{"LUNAROUTE_API_KEY=" + captureFixtureKey, "OPENCODE_CONFIG_CONTENT=" + controlsOverlay}, Stdin: "", MaxResponseBytes: openCodeControlsLimit}
	for _, mode := range []string{"success", "missing", "malformed", "duplicate", "unknown-model", "context-drift", "output-drift", "input-zero", "input-over-context", "duplicate-env"} {
		t.Run(mode, func(t *testing.T) {
			raw := forcingSnapshot
			launch := spec
			switch mode {
			case "missing":
				raw = "{}"
			case "malformed":
				raw = "{"
			case "duplicate":
				raw = strings.Replace(raw, `"models":`, `"models":[],"models":`, 1)
			case "unknown-model":
				raw = strings.ReplaceAll(raw, "glm-5.3", "glm-5.3-flash")
			case "context-drift":
				raw = strings.ReplaceAll(raw, "202752", "202753")
			case "output-drift":
				raw = strings.ReplaceAll(raw, "32768", "32769")
			case "input-zero":
				raw = strings.Replace(raw, `"max_input_tokens":202752`, `"max_input_tokens":0`, 1)
			case "input-over-context":
				raw = strings.Replace(raw, `"max_input_tokens":202752`, `"max_input_tokens":202753`, 1)
			case "duplicate-env":
				launch.Env = append(append([]string{}, launch.Env...), "OPENCODE_CONFIG_CONTENT="+controlsOverlay)
			}
			forced, err := openCodeCompactionSpec(launch, []byte(raw), 2048)
			if mode != "success" {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), captureFixtureKey)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, spec.Bin, forced.Bin)
			assert.Equal(t, spec.Dir, forced.Dir)
			assert.Equal(t, spec.Args, forced.Args)
			assert.Equal(t, spec.Env[0], forced.Env[0])
			var cfg map[string]any
			require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(forced.Env[1], "OPENCODE_CONFIG_CONTENT=")), &cfg))
			assert.Equal(t, map[string]any{"auto": true, "reserved": float64(200704)}, cfg["compaction"])
			model := cfg["provider"].(map[string]any)["lunaroute"].(map[string]any)["models"].(map[string]any)["glm-5.3"].(map[string]any)
			assert.Equal(t, map[string]any{"context": float64(202752), "input": float64(202752), "output": float64(32768)}, model["limit"])
			assert.Equal(t, "OPENCODE_CONFIG_CONTENT="+controlsOverlay, spec.Env[1], "forcing must not mutate the original emitted env")
		})
	}
}

// Both the warm-up and resumed requests are actual CONNECT/TLS exchanges. The
// protocol executable is deliberately not an installed-backend observation.
func TestAgentEvidenceOpenCodeCompactionDriverFactorySink(t *testing.T) {
	for _, mode := range []string{"success", "usage-total", "usage-missing", "usage-zero", "usage-negative", "usage-fraction", "usage-overflow", "usage-foreign", "usage-stale", "existing-summary", "controls-reserve", "controls-input", "controls-auto", "resume-exit", "resume-incomplete", "resume-missing", "no-traffic"} {
		t.Run(mode, func(t *testing.T) {
			started := time.Now().UTC()
			root, bin, capture := agentFactoryFixture(t, "opencode", "correlation-force-"+mode)
			client, _ := agentFactoryProbeClient(t)
			result, err := executeAgentMission(context.Background(), liveCompactingFactoryWithClient(root, bin, agentFactoryRules(t), client, capture, []byte(forcingSnapshot)), liveBinding(captureFixtureKey, "glm-5.3", "opencode"), root)
			require.FileExists(t, filepath.Join(root, agentProofFile))
			require.FileExists(t, filepath.Join(root, agentCommandFile))
			ref := evidencePath + "/observations/controls.json"
			require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, ref)), 0700))
			config, marshalErr := json.Marshal(agentConfigReference{RunID: "run-one", Backend: "opencode", Model: "glm-5.3", SessionID: "ses_factory", Source: openCodeCompactionSource})
			require.NoError(t, marshalErr)
			require.NoError(t, os.WriteFile(filepath.Join(root, ref), config, 0600))
			if err == nil {
				err = publishAgentEvidence(root, root, captureFixtureKey, "forced.json", agentEvidenceInput{RunID: "run-one", Backend: "opencode", Model: "glm-5.3", Version: "1.19.5", ConfigRef: ref, StartedAt: started, FinishedAt: time.Now().UTC(), RequiredCategories: []string{"main", "subagent", "title", "compaction"}, Records: capture.Records(), Result: result})
			}
			sink := filepath.Join(root, evidencePath, "observations/forced.json")
			if mode != "success" && mode != "usage-total" {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), captureFixtureKey)
				_, statErr := os.Stat(sink)
				require.True(t, os.IsNotExist(statErr))
				if strings.HasPrefix(mode, "usage-") || mode == "existing-summary" || strings.HasPrefix(mode, "controls-") {
					_, statErr = os.Stat(filepath.Join(root, "resume-marker"))
					require.True(t, os.IsNotExist(statErr), "bad preconditions must refuse before continuation")
				}
				return
			}
			require.NoError(t, err)
			require.FileExists(t, filepath.Join(root, "resume-marker"))
			raw, err := os.ReadFile(sink)
			require.NoError(t, err)
			var obs agentTaskObservation
			require.NoError(t, json.Unmarshal(raw, &obs))
			require.Len(t, obs.Inference, 4)
			assert.Equal(t, "compaction", obs.Inference[3].Category)
			assert.Equal(t, []string{"glm-5.3"}, obs.Inference[3].ReportedModels)
			assert.True(t, obs.Completed && obs.FileOK && obs.CommandOK)
			assert.NotContains(t, string(raw), captureFixtureKey)
			assert.NotContains(t, string(raw), "Private compaction summary")
		})
	}
}
