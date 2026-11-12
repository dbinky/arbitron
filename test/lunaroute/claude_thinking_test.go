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

func TestAgentEvidenceClaudeThinkingWire(t *testing.T) {
	for _, tc := range []struct {
		name, raw, state string
		budget           int
		valid            bool
	}{
		{"absent", `{}`, "", 0, true},
		{"disabled", `{"thinking":{"type":"disabled"}}`, "disabled", 0, true},
		{"adaptive", `{"thinking":{"type":"adaptive","display":"omitted"}}`, "adaptive", 0, true},
		{"enabled", `{"thinking":{"type":"enabled","budget_tokens":1024},"max_tokens":2048}`, "enabled", 1024, true},
		{"null", `{"thinking":null}`, "", 0, false},
		{"array", `{"thinking":[]}`, "", 0, false},
		{"missing-type", `{"thinking":{}}`, "", 0, false},
		{"unknown", `{"thinking":{"type":"off"}}`, "", 0, false},
		{"secret", `{"thinking":{"type":"` + captureFixtureKey + `"}}`, "", 0, false},
		{"disabled-budget", `{"thinking":{"type":"disabled","budget_tokens":0}}`, "", 0, false},
		{"adaptive-budget", `{"thinking":{"type":"adaptive","budget_tokens":1024}}`, "", 0, false},
		{"missing-budget", `{"thinking":{"type":"enabled"}}`, "", 0, false},
		{"fraction", `{"thinking":{"type":"enabled","budget_tokens":1024.5},"max_tokens":2048}`, "", 0, false},
		{"small-budget", `{"thinking":{"type":"enabled","budget_tokens":1023},"max_tokens":2048}`, "", 0, false},
		{"output-ceiling", `{"thinking":{"type":"enabled","budget_tokens":2048},"max_tokens":2048}`, "", 0, false},
		{"null-budget", `{"thinking":{"type":"enabled","budget_tokens":null},"max_tokens":2048}`, "", 0, false},
		{"override", `{"thinking":{"type":"disabled"},"messages":[{"role":"user","thinking":{"type":"adaptive"}}]}`, "", 0, false},
		{"duplicate", `{"thinking":{"type":"disabled","type":"enabled"}}`, "", 0, false},
		{"malformed", `{"thinking":`, "", 0, false},
		{"null-body", `null`, "", 0, false},
		{"multiple-values", `{} {}`, "", 0, false},
		{"oversized", `{"padding":"` + strings.Repeat("x", captureBodyLimit) + `"}`, "", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proof, err := parseClaudeRequestThinking([]byte(tc.raw))
			if !tc.valid {
				require.Error(t, err)
				assert.Nil(t, proof)
				assert.NotContains(t, err.Error(), captureFixtureKey)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, proof)
			assert.Equal(t, tc.state, proof.Type)
			assert.Equal(t, tc.budget, proof.BudgetTokens)
		})
	}
}

func TestAgentEvidenceClaudeThinkingSnapshot(t *testing.T) {
	original := capturedInference{ClaudeThinking: &claudeRequestThinking{Type: "disabled"}}
	copy := cloneCaptureRecord(original)
	copy.ClaudeThinking.Type = "adaptive"
	assert.Equal(t, "disabled", original.ClaudeThinking.Type, "a caller cannot change the capture's private record")
}

func claudeThinkingRules(t *testing.T) provider.CapabilityRules {
	t.Helper()
	path := filepath.Join(t.TempDir(), "thinking.yaml")
	require.NoError(t, os.WriteFile(path, []byte("lunaroute_models:\n  glm-5.3:\n    context_tokens: {tokens: 202752, evidence: fixture#context}\n    output_tokens: {tokens: 32768, evidence: fixture#output}\n    efforts:\n      claude-code: {levels: [none], evidence: fixture#thinking}\n"), 0600))
	rules, err := adapterconfig.LoadCapabilityRules(path)
	require.NoError(t, err)
	return rules
}

func TestAgentEvidenceClaudeThinkingSelection(t *testing.T) {
	spec, _ := claudeStartupFixture(t)
	rules := claudeThinkingRules(t)
	for _, mode := range []string{"exact", "missing", "positive", "negative", "duplicate", "graded-conflict", "unknown-fact", "argv-conflict"} {
		t.Run(mode, func(t *testing.T) {
			actual := spec
			actual.Env = append(append([]string(nil), spec.Env...), "MAX_THINKING_TOKENS=0")
			actual.Args = append([]string(nil), spec.Args...)
			table := rules
			switch mode {
			case "missing":
				actual.Env = spec.Env
			case "positive":
				actual.Env[len(actual.Env)-1] = "MAX_THINKING_TOKENS=1024"
			case "negative":
				actual.Env[len(actual.Env)-1] = "MAX_THINKING_TOKENS=-1"
			case "duplicate":
				actual.Env = append(actual.Env, "MAX_THINKING_TOKENS=0")
			case "graded-conflict":
				actual.Env = append(actual.Env, "CLAUDE_CODE_EFFORT_LEVEL=high")
			case "unknown-fact":
				table = agentFactoryRules(t)
			case "argv-conflict":
				actual.Args = append(actual.Args, "--thinking=adaptive")
			}
			proof, err := collectClaudeEffortSelection(actual, table, provider.EffortNone)
			if mode != "exact" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, provider.EffortNone, proof.Selected)
		})
	}
}

// These protocol processes prove collection only, never installed thinking support.
func TestAgentEvidenceClaudeThinkingFactorySink(t *testing.T) {
	for _, tc := range []struct {
		mode     string
		selected provider.Effort
		state    string
		budget   int
		valid    bool
	}{
		{"thinking-disabled", provider.EffortNone, "disabled", 0, true},
		{"thinking-adaptive", provider.EffortDefault, "adaptive", 0, true},
		{"thinking-enabled", provider.EffortDefault, "enabled", 1024, true},
		{"success", provider.EffortDefault, "", 0, true},
		{"thinking-absent", provider.EffortNone, "", 0, false},
		{"thinking-adaptive", provider.EffortNone, "adaptive", 0, false},
		{"thinking-enabled", provider.EffortNone, "enabled", 1024, false},
		{"thinking-missing-feed", provider.EffortNone, "disabled", 0, false},
		{"thinking-unsafe-record", provider.EffortNone, "disabled", 0, false},
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
			factory := liveClaudeIsolationFactoryWithClient(root, bin, claudeThinkingRules(t), client, isolation, capture, binding.Effort)
			started := time.Now().UTC()
			result, err := executePreparedAgentMission(context.Background(), factory, binding, ws)
			require.NoError(t, err)
			records := capture.Records()
			require.Len(t, records, 1)
			assert.True(t, records[0].Completed && records[0].AuthMatchesSelected)
			assert.Zero(t, *calls)
			file, err := os.ReadFile(filepath.Join(ws, agentProofFile))
			require.NoError(t, err)
			assert.Equal(t, agentProofText, string(file))
			switch tc.mode {
			case "thinking-missing-feed":
				records[0].ClaudeThinking = nil
			case "thinking-unsafe-record":
				require.NotNil(t, records[0].ClaudeThinking)
				records[0].ClaudeThinking.Type = captureFixtureKey
			}
			obs, err := collectClaudeIsolationEvidence(isolation, records, result, started, time.Now().UTC())
			if err == nil {
				_, err = saveObservation(root, captureFixtureKey, "claude-thinking.json", obs)
			}
			path := filepath.Join(root, evidencePath, "observations/claude-thinking.json")
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
			assert.Equal(t, claudeThinkingSource, saved.Thinking.Source)
			assert.Equal(t, claudeStartupVersion, saved.Thinking.Version)
			assert.Equal(t, "ses_factory", saved.Thinking.Session)
			assert.Equal(t, "glm-5.3", saved.Thinking.Model)
			assert.Equal(t, tc.selected, saved.Thinking.Selected)
			assert.Equal(t, []string{records[0].RequestID}, saved.Thinking.RequestIDs)
			require.Len(t, saved.Thinking.Observed, 1)
			assert.Equal(t, tc.state, saved.Thinking.Observed[0].Type)
			assert.Equal(t, tc.budget, saved.Thinking.Observed[0].BudgetTokens)
			require.Len(t, saved.Inference, 1)
			require.NotNil(t, saved.Inference[0].ClaudeThinking)
			assert.Equal(t, saved.Thinking.Observed[0], *saved.Inference[0].ClaudeThinking)
			for _, secret := range []string{root, bin, captureFixtureKey, agentMissionPrompt, claudeForeignCredential} {
				assert.NotContains(t, string(raw), secret)
			}
		})
	}
}
