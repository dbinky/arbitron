package lunaroute_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Independent expected contract: configured intent must not be presented as
// effective helper routing. Each value must come from the actual emitted env.
var expectedClaudeSelectors = []string{
	"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_SMALL_FAST_MODEL",
	"ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL",
}

func TestAgentEvidenceClaudeSelectorSelection(t *testing.T) {
	for _, name := range expectedClaudeSelectors {
		for _, mode := range []string{"exact", "missing", "empty", "drift", "duplicate", "unsafe"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				spec, _ := claudeStartupFixture(t)
				var env []string
				for _, entry := range spec.Env {
					if !strings.HasPrefix(entry, "ANTHROPIC_MODEL=") {
						env = append(env, entry)
					}
				}
				for _, selector := range expectedClaudeSelectors {
					if selector == name && mode == "missing" {
						continue
					}
					value := "glm-5.3"
					if selector == name {
						switch mode {
						case "empty":
							value = ""
						case "drift":
							value = "glm-5.3-flash"
						case "unsafe":
							value = captureFixtureKey
						}
					}
					env = append(env, selector+"="+value)
					if selector == name && mode == "duplicate" {
						env = append(env, selector+"="+value)
					}
				}
				spec.Env = env
				proof, err := collectClaudeSelectors(spec)
				if mode != "exact" {
					require.Error(t, err)
					assert.NotContains(t, err.Error(), captureFixtureKey)
					return
				}
				require.NoError(t, err)
				assert.Equal(t, "configured_intent", proof.Kind)
				assert.Equal(t, claudeSelectorsSource, proof.Source)
				assert.Equal(t, "glm-5.3", proof.Model)
				require.Len(t, proof.Values, len(expectedClaudeSelectors))
				for _, selector := range expectedClaudeSelectors {
					assert.Equal(t, "glm-5.3", proof.Values[selector])
				}
			})
		}
	}
}
