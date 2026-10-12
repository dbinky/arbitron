package mcp_test

import (
	"context"
	"encoding/json"
	"testing"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthoringGuidance_NamesStandardConventions(t *testing.T) {
	for _, name := range []string{
		"repo", "repo_url", "base_branch", "pr_number", "slug", "teams_webhook_url",
		"branch", "work_dir", "gitVariable", "model_profile",
	} {
		assert.Contains(t, adaptmcp.AuthoringGuidance, name, "guidance should document %q", name)
	}
}

func TestWorkflowCreateUpdate_DescriptionsPointAtConventions(t *testing.T) {
	srv := fullyWiredServer(t)
	adaptmcp.RegisterAllTools(srv)
	found := map[string]bool{}
	for _, tool := range srv.ListTools() {
		if tool.Name != "workflow_create" && tool.Name != "workflow_update" {
			continue
		}
		found[tool.Name] = true
		assert.Contains(t, tool.Description, "gitVariable", tool.Name)
		assert.Contains(t, tool.Description, "conventions", tool.Name)
	}
	assert.True(t, found["workflow_create"] && found["workflow_update"], "both tools registered")
}

func TestServer_InitializeCarriesAuthoringInstructions(t *testing.T) {
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
	assert.Equal(t, adaptmcp.AuthoringGuidance, decoded.Result.Instructions)
}
