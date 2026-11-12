package lunaroute_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A harness selector is not a gateway model id. Only the lifecycle/header/
// response join can establish that this explicit Haiku request stayed selected.
func TestAgentEvidenceClaudeSubagentHaikuSelection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values []string
	}{
		{"exact", []string{"glm-5.3"}},
		{"missing", nil},
		{"drift", []string{"glm-5.3-flash"}},
		{"duplicate", []string{"glm-5.3", "glm-5.3"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec, _ := claudeStartupFixture(t)
			spec.Env = append(spec.Env, "CLAUDE_CODE_SUBAGENT_MODEL=glm-5.3")
			for _, value := range tc.values {
				spec.Env = append(spec.Env, "ANTHROPIC_DEFAULT_HAIKU_MODEL="+value)
			}
			err := collectClaudeSubagentSelection(spec)
			if tc.name == "exact" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestAgentEvidenceClaudeSubagentRequestedModel(t *testing.T) {
	assert.Contains(t, agentMissionPromptFor("claude-code"), "model=haiku")
	for _, tc := range []struct {
		name, field string
	}{
		{"haiku", `"model":"haiku",`},
		{"missing", ""},
		{"null", `"model":null,`},
		{"non-string", `"model":42,`},
		{"inherit", `"model":"inherit",`},
		{"different-alias", `"model":"sonnet",`},
		{"fixed-id", `"model":"glm-5.3",`},
		{"unsafe", `"model":"` + captureFixtureKey + `",`},
		{"duplicate", `"model":"sonnet","model":"haiku",`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines := claudeSubagentLines()
			lines[0] = strings.Replace(lines[0], `"model":"haiku",`, "", 1)
			lines[0] = strings.Replace(lines[0], `"input":{`, `"input":{`+tc.field, 1)
			proof, err := collectClaudeSubagentLifecycle(lines, "ses_factory", "glm-5.3")
			if tc.name != "haiku" {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), captureFixtureKey)
				return
			}
			require.NoError(t, err)
			raw, err := json.Marshal(proof)
			require.NoError(t, err)
			var saved map[string]any
			require.NoError(t, json.Unmarshal(raw, &saved))
			assert.Equal(t, "haiku", saved["requested_model"])
			assert.Equal(t, "glm-5.3", saved["harness_reported_model"])
			assert.Equal(t, claudeSubagentSource, saved["source"])
		})
	}
}
