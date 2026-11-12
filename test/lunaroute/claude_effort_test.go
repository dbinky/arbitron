package lunaroute_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adapterconfig "github.com/arbitron/arbitron/adapter/config"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentEvidenceClaudeEffortWire(t *testing.T) {
	for _, tc := range []struct {
		name, raw, level string
		valid            bool
	}{
		{"absent", `{"messages":[{"role":"user","content":"private prompt"}]}`, "", true},
		{"format-only", `{"output_config":{"format":{"type":"json_schema"}}}`, "", true},
		{"low", `{"output_config":{"effort":"low"}}`, "low", true},
		{"medium", `{"output_config":{"effort":"medium"}}`, "medium", true},
		{"high", `{"output_config":{"effort":"high"}}`, "high", true},
		{"xhigh", `{"output_config":{"effort":"xhigh"}}`, "xhigh", true},
		{"max", `{"output_config":{"effort":"max"}}`, "max", true},
		{"null-config", `{"output_config":null}`, "", false},
		{"null-body", `null`, "", false},
		{"multiple-values", `{} {}`, "", false},
		{"array-config", `{"output_config":[]}`, "", false},
		{"null-effort", `{"output_config":{"effort":null}}`, "", false},
		{"numeric", `{"output_config":{"effort":100}}`, "", false},
		{"alias", `{"output_config":{"effort":"med"}}`, "", false},
		{"empty", `{"output_config":{"effort":""}}`, "", false},
		{"secret", `{"output_config":{"effort":"` + captureFixtureKey + `"}}`, "", false},
		{"per-message", `{"output_config":{"effort":"high"},"messages":[{"role":"user","output_config":{"effort":"low"}}]}`, "", false},
		{"per-message-null", `{"output_config":{"effort":"high"},"messages":[{"role":"user","output_config":{"effort":null}}]}`, "", false},
		{"duplicate", `{"output_config":{"effort":"low","effort":"high"}}`, "", false},
		{"malformed", `{"output_config":`, "", false},
		{"oversized", `{"padding":"` + strings.Repeat("x", captureBodyLimit) + `"}`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proof, err := parseClaudeRequestEffort([]byte(tc.raw))
			if !tc.valid {
				require.Error(t, err)
				assert.Nil(t, proof)
				assert.NotContains(t, err.Error(), captureFixtureKey)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, proof, "absence must be observed, never filled from emitted intent")
			assert.Equal(t, tc.level, proof.Level)
		})
	}
}

func claudeEffortRules(t *testing.T) provider.CapabilityRules {
	t.Helper()
	path := filepath.Join(t.TempDir(), "effort.yaml")
	require.NoError(t, os.WriteFile(path, []byte("lunaroute_models:\n  glm-5.3:\n    context_tokens: {tokens: 202752, evidence: fixture#context}\n    output_tokens: {tokens: 32768, evidence: fixture#output}\n    efforts:\n      claude-code: {levels: [low, medium, high, xhigh, max], evidence: fixture#effort}\n"), 0600))
	rules, err := adapterconfig.LoadCapabilityRules(path)
	require.NoError(t, err)
	return rules
}

func TestAgentEvidenceClaudeEffortLaunch(t *testing.T) {
	spec, _ := claudeStartupFixture(t)
	rules := claudeEffortRules(t)
	for _, level := range []provider.Effort{provider.EffortDefault, provider.EffortLow, provider.EffortMedium, provider.EffortHigh, provider.EffortXHigh, provider.EffortMax} {
		t.Run(string(level), func(t *testing.T) {
			actual := spec
			actual.Env = append([]string(nil), spec.Env...)
			if level != provider.EffortDefault {
				actual.Env = append(actual.Env, "CLAUDE_CODE_EFFORT_LEVEL="+string(level))
			}
			proof, err := collectClaudeEffortSelection(actual, rules, level)
			require.NoError(t, err)
			assert.Equal(t, level, proof.Selected)
			assert.Equal(t, "glm-5.3", proof.Model)
			assert.Equal(t, claudeEffortSource, proof.Source)
		})
	}
	for _, mode := range []string{"missing-env", "drift", "duplicate", "unknown-fact", "thinking-override", "argv-override", "default-with-env", "none", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			actual := spec
			actual.Env = append(append([]string(nil), spec.Env...), "CLAUDE_CODE_EFFORT_LEVEL=high")
			actual.Args = append([]string(nil), spec.Args...)
			table, selected := rules, provider.EffortHigh
			switch mode {
			case "missing-env":
				actual.Env = spec.Env
			case "drift":
				actual.Env[len(actual.Env)-1] = "CLAUDE_CODE_EFFORT_LEVEL=medium"
			case "duplicate":
				actual.Env = append(actual.Env, "CLAUDE_CODE_EFFORT_LEVEL=high")
			case "unknown-fact":
				table = agentFactoryRules(t)
			case "thinking-override":
				actual.Env = append(actual.Env, "MAX_THINKING_TOKENS=0")
			case "argv-override":
				actual.Args = append(actual.Args, "--effort", "low")
			case "default-with-env":
				selected = provider.EffortDefault
			case "none":
				selected = provider.EffortNone
			case "invalid":
				selected = "turbo"
			}
			_, err := collectClaudeEffortSelection(actual, table, selected)
			require.Error(t, err)
		})
	}
}

// Actual Factory -> emitted effort env -> subprocess -> CONNECT/TLS -> private
// JSON and command-created workspace sinks. These are collection fixtures only.
func TestAgentEvidenceClaudeEffortFactorySink(t *testing.T) {
	for _, tc := range []struct {
		mode     string
		selected provider.Effort
		valid    bool
	}{
		{"success", provider.EffortDefault, true},
		{"effort-exact", provider.EffortLow, true},
		{"effort-exact", provider.EffortMedium, true},
		{"effort-exact", provider.EffortHigh, true},
		{"effort-exact", provider.EffortXHigh, true},
		{"effort-exact", provider.EffortMax, true},
		{"effort-absent", provider.EffortHigh, false},
		{"effort-drift", provider.EffortHigh, false},
		{"effort-missing-capture", provider.EffortHigh, false},
		{"effort-missing-selection", provider.EffortHigh, false},
		{"effort-selection-session", provider.EffortHigh, false},
		{"effort-selection-model", provider.EffortHigh, false},
		{"effort-unsafe-record", provider.EffortHigh, false},
	} {
		t.Run(tc.mode+"/"+string(tc.selected), func(t *testing.T) {
			root, bin, capture := agentFactoryFixtureWithEffort(t, "claude-code", tc.mode, tc.selected)
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
			factory := liveClaudeIsolationFactoryWithClient(root, bin, claudeEffortRules(t), client, isolation, capture, binding.Effort)
			started := time.Now().UTC()
			result, err := executePreparedAgentMission(context.Background(), factory, binding, ws)
			require.NoError(t, err)
			records := capture.Records()
			require.Len(t, records, 1)
			assert.True(t, records[0].Completed && records[0].AuthMatchesSelected)
			assert.Equal(t, []string{"glm-5.3"}, records[0].ReportedModels)
			file, err := os.ReadFile(filepath.Join(ws, agentProofFile))
			require.NoError(t, err)
			assert.Equal(t, agentProofText, string(file))
			assert.Zero(t, *calls)
			switch tc.mode {
			case "effort-missing-capture":
				records[0].ClaudeEffort = nil
			case "effort-missing-selection":
				isolation.effort = claudeMainEffortProof{}
			case "effort-selection-session":
				isolation.effort.Session = "ses_other"
			case "effort-selection-model":
				isolation.effort.Model = "glm-5.3-flash"
			case "effort-unsafe-record":
				records[0].ClaudeEffort.Level = captureFixtureKey
			}
			obs, err := collectClaudeIsolationEvidence(isolation, records, result, started, time.Now().UTC())
			if err == nil {
				_, err = saveObservation(root, captureFixtureKey, "claude-effort.json", obs)
			}
			path := filepath.Join(root, evidencePath, "observations/claude-effort.json")
			if !tc.valid {
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
			assert.Equal(t, tc.selected, saved.Effort.Selected)
			assert.Equal(t, claudeEffortSource, saved.Effort.Source)
			assert.Equal(t, claudeStartupVersion, saved.Effort.Version)
			assert.Equal(t, "ses_factory", saved.Effort.Session)
			assert.Equal(t, "glm-5.3", saved.Effort.Model)
			assert.Equal(t, []string{records[0].RequestID}, saved.Effort.RequestIDs)
			expected := string(tc.selected)
			if tc.selected == provider.EffortDefault {
				expected = ""
			}
			assert.Equal(t, []string{expected}, saved.Effort.ObservedLevels)
			require.NotNil(t, saved.Inference[0].ClaudeEffort)
			assert.Equal(t, expected, saved.Inference[0].ClaudeEffort.Level)
			for _, secret := range []string{root, bin, captureFixtureKey, agentMissionPrompt, claudeForeignCredential} {
				assert.NotContains(t, string(raw), secret)
			}
		})
	}
}
