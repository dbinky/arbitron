package mcp

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	gomcp "github.com/mark3labs/mcp-go/mcp"
)

// MarshalResult serializes any value as indented JSON and wraps it in a
// CallToolResult. On marshal failure it returns a tool error result instead
// of a Go error, so the MCP client always receives a well-formed response.
func MarshalResult(v any) (*gomcp.CallToolResult, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return gomcp.NewToolResultError(fmt.Sprintf("marshal error: %s", err)), nil
	}
	return gomcp.NewToolResultText(string(data)), nil
}

// ParseUUID extracts and validates a UUID string from a CallToolRequest
// argument map. It returns a descriptive error if the key is missing, the
// value is not a string, or the string is not a valid UUID.
func ParseUUID(request gomcp.CallToolRequest, key string) (uuid.UUID, error) {
	args := request.GetArguments()
	val, ok := args[key]
	if !ok || val == nil {
		return uuid.Nil, fmt.Errorf("%s is required", key)
	}
	str, ok := val.(string)
	if !ok {
		return uuid.Nil, fmt.Errorf("invalid %s: expected string, got %T", key, val)
	}
	if str == "" {
		return uuid.Nil, fmt.Errorf("%s is required", key)
	}
	id, err := uuid.Parse(str)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid %s: %s", key, str)
	}
	return id, nil
}
