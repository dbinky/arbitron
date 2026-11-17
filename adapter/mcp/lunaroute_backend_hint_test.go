package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/stretchr/testify/require"
)

type profileHintTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func validateProfileBackendHints(tools []profileHintTool) error {
	seen := map[string]bool{}
	for _, tool := range tools {
		if tool.Name != "profile_create" && tool.Name != "profile_update" {
			continue
		}
		if seen[tool.Name] || strings.Count(tool.Description, " LunaRoute:") != 1 {
			return fmt.Errorf("missing or duplicate profile hint")
		}
		seen[tool.Name] = true
		for _, want := range []string{
			"connection_models", "backend explicitly (codex or pi)", "selections are pinned only", "claude-code and opencode are not yet supported",
			"codex", "private CODEX_HOME", "backend-health login", "does not gate",
			"pi", "known context and output limits", "known reasoning metadata",
			"independent evidence-backed reasoning fact", "unknown", "refused",
			"requested/effective effort stays exact", "generic native-provider flooring does not apply to LunaRoute",
		} {
			if !strings.Contains(tool.Description, want) {
				return fmt.Errorf("%s missing guidance: %s", tool.Name, want)
			}
		}
		for _, forbidden := range []string{"recommend", "not yet enabled", "automatically select", "backend explicitly (claude-code, opencode, codex or pi)", "following"} {
			if strings.Contains(tool.Description, forbidden) {
				return fmt.Errorf("contradictory profile guidance")
			}
		}
	}
	if !seen["profile_create"] || !seen["profile_update"] {
		return fmt.Errorf("missing profile tools")
	}
	return nil
}

func TestLunaRouteBackendHint_ReachesProfileTools(t *testing.T) {
	srv := fullyWiredServer(t)
	adaptmcp.RegisterAllTools(srv)
	// Exercise the client-visible tools/list response, not just the constant.
	response := srv.MCPServer().HandleMessage(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	raw, err := json.Marshal(response)
	require.NoError(t, err)
	var result struct {
		Result struct {
			Tools []profileHintTool `json:"tools"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(raw, &result))
	require.NoError(t, validateProfileBackendHints(result.Result.Tools))
	require.NotEmpty(t, adaptmcp.LunaRouteBackendHint)
	for _, tool := range result.Result.Tools {
		if tool.Name == "profile_create" || tool.Name == "profile_update" {
			require.Equal(t, 1, strings.Count(tool.Description, adaptmcp.LunaRouteBackendHint), tool.Name)
			require.True(t, strings.HasSuffix(tool.Description, adaptmcp.LunaRouteBackendHint), tool.Name)
		}
	}
}

func TestLunaRouteBackendHint_RejectsMissingAndContradictorySurfaces(t *testing.T) {
	// The rejection checks use the actual registered descriptions as their base.
	srv := fullyWiredServer(t)
	adaptmcp.RegisterAllTools(srv)
	var tools []profileHintTool
	for _, tool := range srv.ListTools() {
		if tool.Name == "profile_create" || tool.Name == "profile_update" {
			tools = append(tools, profileHintTool{tool.Name, tool.Description})
		}
	}
	require.Len(t, tools, 2)
	require.NoError(t, validateProfileBackendHints(tools))
	for name, mutate := range map[string]func([]profileHintTool) []profileHintTool{
		"empty":               func([]profileHintTool) []profileHintTool { return nil },
		"missing update":      func(in []profileHintTool) []profileHintTool { return in[:1] },
		"duplicate tool":      func(in []profileHintTool) []profileHintTool { return append(in, in[0]) },
		"missing feed":        func(in []profileHintTool) []profileHintTool { in[0].Description = "Create a profile"; return in },
		"duplicate hint":      func(in []profileHintTool) []profileHintTool { in[0].Description += " LunaRoute:"; return in },
		"obsolete enablement": func(in []profileHintTool) []profileHintTool { in[0].Description += " pi is not yet enabled"; return in },
		"recommendation":      func(in []profileHintTool) []profileHintTool { in[0].Description += " recommended model"; return in },
		"obsolete scope": func(in []profileHintTool) []profileHintTool {
			in[0].Description = strings.ReplaceAll(in[0].Description, "backend explicitly (codex or pi)", "backend explicitly (claude-code, opencode, codex or pi)")
			return in
		},
		"following allowed": func(in []profileHintTool) []profileHintTool {
			in[0].Description = strings.ReplaceAll(in[0].Description, "pinned only", "pinned or following")
			return in
		},
		"missing effort exception": func(in []profileHintTool) []profileHintTool {
			in[0].Description = strings.ReplaceAll(in[0].Description, "generic native-provider flooring does not apply to LunaRoute", "flooring applies")
			return in
		},
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, validateProfileBackendHints(mutate(append([]profileHintTool(nil), tools...))))
		})
	}
}
