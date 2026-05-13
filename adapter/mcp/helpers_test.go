package mcp_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
)

func TestMarshalResult(t *testing.T) {
	t.Run("serializes a map to indented JSON text result", func(t *testing.T) {
		input := map[string]string{"status": "ok"}
		result, err := adaptmcp.MarshalResult(input)
		require.NoError(t, err)
		require.NotNil(t, result)

		// The result should contain JSON text content.
		require.Len(t, result.Content, 1)
		text, ok := result.Content[0].(gomcp.TextContent)
		require.True(t, ok)

		var parsed map[string]string
		require.NoError(t, json.Unmarshal([]byte(text.Text), &parsed))
		assert.Equal(t, "ok", parsed["status"])
	})

	t.Run("serializes nil to JSON null text result", func(t *testing.T) {
		// nil marshals to "null" — must return a valid text result, not an error.
		result, err := adaptmcp.MarshalResult(nil)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.False(t, result.IsError)
		require.Len(t, result.Content, 1)
		text, ok := result.Content[0].(gomcp.TextContent)
		require.True(t, ok)
		assert.Equal(t, "null", text.Text)
	})

	t.Run("returns error result for unmarshalable values", func(t *testing.T) {
		// Channels cannot be marshaled to JSON.
		result, err := adaptmcp.MarshalResult(make(chan int))
		require.NoError(t, err, "should not return a Go error")
		require.NotNil(t, result)
		assert.True(t, result.IsError, "result should be flagged as error")
	})
}

func TestParseUUID(t *testing.T) {
	t.Run("parses a valid UUID", func(t *testing.T) {
		expected := uuid.New()
		req := gomcp.CallToolRequest{}
		req.Params.Arguments = map[string]any{"id": expected.String()}

		id, err := adaptmcp.ParseUUID(req, "id")
		require.NoError(t, err)
		assert.Equal(t, expected, id)
	})

	t.Run("returns error for missing key", func(t *testing.T) {
		req := gomcp.CallToolRequest{}
		req.Params.Arguments = map[string]any{}

		_, err := adaptmcp.ParseUUID(req, "id")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "id is required")
	})

	t.Run("returns error for invalid UUID string", func(t *testing.T) {
		req := gomcp.CallToolRequest{}
		req.Params.Arguments = map[string]any{"id": "not-a-uuid"}

		_, err := adaptmcp.ParseUUID(req, "id")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid id")
	})

	t.Run("returns error for nil arguments", func(t *testing.T) {
		req := gomcp.CallToolRequest{}
		// Arguments is nil (not set).

		_, err := adaptmcp.ParseUUID(req, "id")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "id is required")
	})

	t.Run("returns type error when key exists with non-string value", func(t *testing.T) {
		// An LLM might pass a JSON number for a UUID parameter. The old code
		// emitted "id is required" (misleading). The new code says "expected string".
		req := gomcp.CallToolRequest{}
		req.Params.Arguments = map[string]any{"id": float64(42)}

		_, err := adaptmcp.ParseUUID(req, "id")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid id")
		assert.Contains(t, err.Error(), "expected string")
		assert.NotContains(t, err.Error(), "is required")
	})

	t.Run("returns error for empty string value", func(t *testing.T) {
		// Key present, value is the empty string — still "required".
		req := gomcp.CallToolRequest{}
		req.Params.Arguments = map[string]any{"id": ""}

		_, err := adaptmcp.ParseUUID(req, "id")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "id is required")
	})
}
