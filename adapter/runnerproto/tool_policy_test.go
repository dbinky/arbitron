package runnerproto

import (
	"encoding/json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestExecutionCapability_ToolPolicyWireName(t *testing.T) {
	b, err := json.Marshal(ExecutionCapability{ToolPolicy: "allowlist"})
	require.NoError(t, err)
	assert.Contains(t, string(b), `"toolPolicy":"allowlist"`)
	b, err = json.Marshal(ExecutionCapability{})
	require.NoError(t, err)
	assert.NotContains(t, string(b), "toolPolicy", "omitted when unset")
}
