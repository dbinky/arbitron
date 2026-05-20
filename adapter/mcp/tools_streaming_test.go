package mcp_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/adapter/eventbus"
)

func newStreamingServer() *adaptmcp.Server {
	bus := eventbus.New()
	deps := &adaptmcp.Deps{
		EventBus: bus,
	}
	srv := adaptmcp.NewServerWithDeps("test", "0.0.0", deps)
	adaptmcp.RegisterStreamingTools(srv)
	return srv
}

func TestStreamingToolsRegistered(t *testing.T) {
	srv := newStreamingServer()
	tools := srv.ListTools()
	require.Len(t, tools, 4)

	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Name] = true
	}

	assert.True(t, names["instance_subscribe"], "instance_subscribe should be registered")
	assert.True(t, names["instance_unsubscribe"], "instance_unsubscribe should be registered")
	assert.True(t, names["system_subscribe"], "system_subscribe should be registered")
	assert.True(t, names["system_unsubscribe"], "system_unsubscribe should be registered")
}

func TestInstanceSubscribeRoundTrip(t *testing.T) {
	t.Run("subscribe then unsubscribe", func(t *testing.T) {
		srv := newStreamingServer()
		id := uuid.New()

		// Subscribe.
		result := callTool(t, srv, "instance_subscribe", map[string]any{
			"instanceId": id.String(),
		})
		assert.False(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "subscribed")
		assert.Contains(t, text, id.String())

		// Verify active count via the subscription manager.
		require.NotNil(t, srv.Subscriptions())
		assert.Equal(t, 1, srv.Subscriptions().ActiveCount())

		// Unsubscribe.
		result = callTool(t, srv, "instance_unsubscribe", map[string]any{
			"instanceId": id.String(),
		})
		assert.False(t, result.IsError)
		text = extractText(t, result)
		assert.Contains(t, text, "unsubscribed")
		assert.Equal(t, 0, srv.Subscriptions().ActiveCount())
	})

	t.Run("subscribe with invalid UUID returns error", func(t *testing.T) {
		srv := newStreamingServer()

		result := callTool(t, srv, "instance_subscribe", map[string]any{
			"instanceId": "bad-uuid",
		})
		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "invalid instanceId")
	})

	t.Run("subscribe with missing instanceId returns error", func(t *testing.T) {
		srv := newStreamingServer()

		result := callTool(t, srv, "instance_subscribe", map[string]any{})
		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "instanceId is required")
	})

	t.Run("unsubscribe with missing UUID returns error", func(t *testing.T) {
		srv := newStreamingServer()

		result := callTool(t, srv, "instance_unsubscribe", map[string]any{})
		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "instanceId is required")
	})

	t.Run("unsubscribe with invalid UUID returns error", func(t *testing.T) {
		srv := newStreamingServer()

		result := callTool(t, srv, "instance_unsubscribe", map[string]any{
			"instanceId": "not-a-uuid",
		})
		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "invalid instanceId")
	})
}

func TestInstanceSubscribeDuplicate(t *testing.T) {
	t.Run("subscribing to same instance twice keeps active count at 1", func(t *testing.T) {
		// Edge case: duplicate subscribe for the same instance ID must replace
		// the existing subscription rather than accumulating goroutines/channels.
		srv := newStreamingServer()
		id := uuid.New()

		result := callTool(t, srv, "instance_subscribe", map[string]any{"instanceId": id.String()})
		require.False(t, result.IsError)
		assert.Equal(t, 1, srv.Subscriptions().ActiveCount())

		result = callTool(t, srv, "instance_subscribe", map[string]any{"instanceId": id.String()})
		require.False(t, result.IsError)
		assert.Equal(t, 1, srv.Subscriptions().ActiveCount(), "re-subscribing must not add a second subscription")
	})
}

func TestSystemSubscribeRoundTrip(t *testing.T) {
	t.Run("subscribe then unsubscribe", func(t *testing.T) {
		srv := newStreamingServer()

		// Subscribe.
		result := callTool(t, srv, "system_subscribe", nil)
		assert.False(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "subscribed")
		assert.Contains(t, text, "system")

		require.NotNil(t, srv.Subscriptions())
		assert.Equal(t, 1, srv.Subscriptions().ActiveCount())

		// Unsubscribe.
		result = callTool(t, srv, "system_unsubscribe", nil)
		assert.False(t, result.IsError)
		text = extractText(t, result)
		assert.Contains(t, text, "unsubscribed")
		assert.Equal(t, 0, srv.Subscriptions().ActiveCount())
	})
}

func TestStreamingToolsWithoutEventBus(t *testing.T) {
	newNobusSrv := func() *adaptmcp.Server {
		deps := &adaptmcp.Deps{} // EventBus is nil
		srv := adaptmcp.NewServerWithDeps("test", "0.0.0", deps)
		adaptmcp.RegisterStreamingTools(srv)
		return srv
	}

	t.Run("instance_subscribe returns error when event bus not configured", func(t *testing.T) {
		srv := newNobusSrv()
		result := callTool(t, srv, "instance_subscribe", map[string]any{
			"instanceId": uuid.New().String(),
		})
		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "not available")
	})

	t.Run("instance_unsubscribe returns error when event bus not configured", func(t *testing.T) {
		srv := newNobusSrv()
		result := callTool(t, srv, "instance_unsubscribe", map[string]any{
			"instanceId": uuid.New().String(),
		})
		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "not available")
	})

	t.Run("system_subscribe returns error when event bus not configured", func(t *testing.T) {
		srv := newNobusSrv()
		result := callTool(t, srv, "system_subscribe", nil)
		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "not available")
	})

	t.Run("system_unsubscribe returns error when event bus not configured", func(t *testing.T) {
		srv := newNobusSrv()
		result := callTool(t, srv, "system_unsubscribe", nil)
		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "not available")
	})
}
