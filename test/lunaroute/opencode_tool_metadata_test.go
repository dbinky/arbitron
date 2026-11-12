package lunaroute_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentEvidenceOpenCodeToolMetadataSource(t *testing.T) {
	proof, err := validateOpenCodeEffectiveControls([]byte(controlsOverlay), []byte(effectiveControlsFixture()))
	require.NoError(t, err)
	raw, err := json.Marshal(proof)
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(raw, &fields))
	assert.Equal(t, true, fields["tool_call"], "retain the actual emitted/resolved control, not a publication default")
	require.NoError(t, validateOpenCodeAgentInputSource([]byte(forcingSnapshot), proof))
	for name, capabilities := range map[string]string{
		"missing": `{}`, "null": `{"tools":null}`, "false": `{"tools":false}`,
		"string": `{"tools":"true"}`, "number": `{"tools":1}`,
		"duplicate": `{"tools":false,"tools":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			source := strings.Replace(forcingSnapshot, `"capabilities":{"tools":true}`, `"capabilities":`+capabilities, 1)
			err := validateOpenCodeAgentInputSource([]byte(source), proof)
			require.ErrorIs(t, err, errAgentEvidence)
			assert.NotContains(t, err.Error(), source)
		})
	}
	for name, source := range map[string]string{
		"capabilities-absent": strings.Replace(forcingSnapshot, `,"capabilities":{"tools":true}`, "", 1),
		"capabilities-null":   strings.Replace(forcingSnapshot, `{"tools":true}`, "null", 1),
		"foreign-model-tools": strings.Replace(strings.Replace(forcingSnapshot, `"capabilities":{"tools":true}`, `"capabilities":{}`, 1), `]}`, `,{"id":"glm-5.3-flash","capabilities":{"tools":true}}]}`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			require.True(t, json.Valid([]byte(source)), "source rejection must not rely on malformed JSON")
			require.ErrorIs(t, validateOpenCodeAgentInputSource([]byte(source), proof), errAgentEvidence)
		})
	}
}
