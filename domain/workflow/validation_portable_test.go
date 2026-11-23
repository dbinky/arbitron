package workflow

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func portableFindings(kind NodeType, cfg NodeConfig) []ValidationError {
	return Validate(&Workflow{Nodes: []Node{{ID: "process", Type: kind, Config: cfg, Entrypoint: true}}}, nil, nil, nil)
}

func requirePortableCode(t *testing.T, errs []ValidationError, code string) {
	t.Helper()
	for _, err := range errs {
		if err.Code == code {
			require.Equal(t, "process", err.NodeID)
			return
		}
	}
	t.Fatalf("missing %s in %v", code, errs)
}

func TestValidate_PortableAndRequiresFields(t *testing.T) {
	t.Run("valid names", func(t *testing.T) {
		require.Empty(t, portableFindings(NodeTypeToolCall, NodeConfig{Portable: true, Command: "echo", Requires: []string{"jq", "git", "node"}}))
	})
	for _, names := range [][]string{{"a/b"}, {""}, {"a b"}, {"a\\b"}, {"a\tb"}, {"jq", "jq"}} {
		t.Run("invalid names "+fmt.Sprint(names), func(t *testing.T) {
			requirePortableCode(t, portableFindings(NodeTypeToolCall, NodeConfig{Portable: true, Command: "echo", Requires: names}), "INVALID_REQUIRES")
		})
	}
	t.Run("non process", func(t *testing.T) {
		requirePortableCode(t, portableFindings(NodeTypeLLMCall, NodeConfig{Portable: true}), "INVALID_PORTABLE")
	})
	t.Run("requires without portable", func(t *testing.T) {
		requirePortableCode(t, portableFindings(NodeTypeToolCall, NodeConfig{Command: "echo", Requires: []string{"jq"}}), "REQUIRES_WITHOUT_PORTABLE")
	})
}

func TestValidate_PortableScriptWithoutCheckerFailsClosed(t *testing.T) {
	requirePortableCode(t, portableFindings(NodeTypeScript, NodeConfig{Portable: true, ScriptFile: "run.sh"}), "SCRIPT_BUNDLE_CHECKER_UNAVAILABLE")
}

func TestNodeConfig_PortableRequiresJSONRoundTrip(t *testing.T) {
	var c NodeConfig
	require.NoError(t, json.Unmarshal([]byte(`{"portable":true,"requires":["jq","git"]}`), &c))
	require.True(t, c.Portable)
	require.Equal(t, []string{"jq", "git"}, c.Requires)
	b, err := json.Marshal(c)
	require.NoError(t, err)
	var restored NodeConfig
	require.NoError(t, json.Unmarshal(b, &restored))
	require.Equal(t, c, restored)
}
