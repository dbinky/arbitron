package mcp_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthoringGuidance_ModelDiscoveryAndExplicitSelection(t *testing.T) {
	// Assert the client-visible initialize sink, not just the source constant.
	srv := adaptmcp.NewServer("arbitron", "test")
	req := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
	resp := srv.MCPServer().HandleMessage(context.Background(), json.RawMessage(req))
	raw, err := json.Marshal(resp)
	require.NoError(t, err)
	var decoded struct {
		Result struct {
			Instructions string `json:"instructions"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, adaptmcp.AuthoringGuidance, decoded.Result.Instructions)
	guidance := strings.Join(strings.Fields(decoded.Result.Instructions), " ")
	for _, s := range []string{
		"connection_models",
		`"unknown" is not a guarantee`,
		"not thereby compatible",
		"explicitly",
		"claude-code, codex, opencode or pi",
		"pinned-only",
		"actual vendor",
		"reviewed Claude aliases retain their evidenced Anthropic vendor",
		`"unresolved"`,
	} {
		assert.Contains(t, guidance, s)
	}
	assert.NotContains(t, guidance, "recommended model")
}
