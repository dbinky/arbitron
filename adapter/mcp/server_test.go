package mcp_test

import (
	"context"
	"os"
	"testing"
	"time"

	gomcp "github.com/mark3labs/mcp-go/mcp"

	"github.com/arbitron/arbitron/adapter/eventbus"
	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewServer(t *testing.T) {
	srv := adaptmcp.NewServer("test-arbitron", "0.0.0")
	require.NotNil(t, srv)
	assert.NotNil(t, srv.MCPServer(), "underlying MCPServer should be set")
}

func TestServer_NoTools(t *testing.T) {
	srv := adaptmcp.NewServer("test-arbitron", "0.0.0")
	tools := srv.ListTools()
	assert.Equal(t, 0, len(tools))
}

func TestNewServerWithDeps(t *testing.T) {
	deps := &adaptmcp.Deps{}
	srv := adaptmcp.NewServerWithDeps("test", "0.0.0", deps)
	require.NotNil(t, srv)
	assert.NotNil(t, srv.MCPServer(), "underlying MCPServer should be set")
}

func TestServer_AddTool(t *testing.T) {
	srv := adaptmcp.NewServer("test-arbitron", "0.0.0")

	tool := gomcp.NewTool("echo", gomcp.WithDescription("echoes input"))
	srv.AddTool(tool, nil)

	tools := srv.ListTools()
	require.Len(t, tools, 1)
	assert.Equal(t, "echo", tools[0].Name)
}

func TestServer_AddMultipleTools(t *testing.T) {
	srv := adaptmcp.NewServer("test-arbitron", "0.0.0")

	srv.AddTool(gomcp.NewTool("tool-a", gomcp.WithDescription("first")), nil)
	srv.AddTool(gomcp.NewTool("tool-b", gomcp.WithDescription("second")), nil)

	tools := srv.ListTools()
	require.Len(t, tools, 2)
	assert.Equal(t, "tool-a", tools[0].Name)
	assert.Equal(t, "tool-b", tools[1].Name)
}

func TestServer_HTTPHandler(t *testing.T) {
	srv := adaptmcp.NewServer("test-arbitron", "0.0.0")
	handler := srv.HTTPHandler()
	require.NotNil(t, handler, "HTTPHandler must return a non-nil http.Handler")
}

func TestServer_HTTPHandlerWithDeps(t *testing.T) {
	deps := &adaptmcp.Deps{}
	srv := adaptmcp.NewServerWithDeps("test", "0.0.0", deps)
	handler := srv.HTTPHandler()
	require.NotNil(t, handler, "HTTPHandler must return a non-nil http.Handler for server with deps")
}

// Subscriptions() — failure/error/edge case tests.

func TestServer_Subscriptions_NilWhenNoEventBus(t *testing.T) {
	t.Run("NewServer returns nil Subscriptions", func(t *testing.T) {
		// NewServer has no deps so Subscriptions must be nil.
		srv := adaptmcp.NewServer("test", "0.0.0")
		assert.Nil(t, srv.Subscriptions(), "Subscriptions must be nil when no EventBus provided")
	})

	t.Run("NewServerWithDeps nil deps returns nil Subscriptions", func(t *testing.T) {
		// Nil deps must not panic and must yield nil Subscriptions.
		srv := adaptmcp.NewServerWithDeps("test", "0.0.0", nil)
		require.NotNil(t, srv)
		assert.Nil(t, srv.Subscriptions(), "Subscriptions must be nil when deps is nil")
	})

	t.Run("NewServerWithDeps nil EventBus returns nil Subscriptions", func(t *testing.T) {
		// Deps provided but EventBus is the zero value (nil interface).
		deps := &adaptmcp.Deps{} // EventBus field is nil
		srv := adaptmcp.NewServerWithDeps("test", "0.0.0", deps)
		assert.Nil(t, srv.Subscriptions(), "Subscriptions must be nil when EventBus is nil")
	})
}

func TestServer_Subscriptions_NonNilWithEventBus(t *testing.T) {
	t.Run("NewServerWithDeps with valid EventBus returns non-nil Subscriptions", func(t *testing.T) {
		bus := eventbus.New()
		deps := &adaptmcp.Deps{EventBus: bus}
		srv := adaptmcp.NewServerWithDeps("test", "0.0.0", deps)
		require.NotNil(t, srv.Subscriptions(), "Subscriptions must be non-nil when EventBus is provided")
	})
}

// Listen — transport smoke tests.

func TestServer_Listen_StdinEOF(t *testing.T) {
	t.Run("returns when stdin reaches EOF immediately", func(t *testing.T) {
		// Edge case: client disconnects before sending any data.
		// Listen must terminate rather than blocking forever.
		srv := adaptmcp.NewServer("test", "0.0.0")

		stdinR, stdinW, err := os.Pipe()
		require.NoError(t, err)
		_, stdoutW, err := os.Pipe()
		require.NoError(t, err)
		defer stdinR.Close()
		defer stdoutW.Close()

		// EOF on the write side surfaces as EOF on the read side.
		stdinW.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		done := make(chan error, 1)
		go func() { done <- srv.Listen(ctx, stdinR, stdoutW) }()

		select {
		case <-done:
			// any return value (nil or error) is acceptable
		case <-time.After(5 * time.Second):
			t.Fatal("Listen did not return after stdin EOF")
		}
	})
}

func TestServer_Listen_ContextCancellation(t *testing.T) {
	t.Run("shuts down when context is cancelled", func(t *testing.T) {
		// Error scenario: parent context cancelled (e.g., SIGINT handling).
		// Close stdin as well so the read loop definitely unblocks, since not
		// all stdio implementations interrupt blocked reads on ctx.Done().
		srv := adaptmcp.NewServer("test", "0.0.0")

		stdinR, stdinW, err := os.Pipe()
		require.NoError(t, err)
		_, stdoutW, err := os.Pipe()
		require.NoError(t, err)
		defer stdinR.Close()
		defer stdoutW.Close()

		ctx, cancel := context.WithCancel(context.Background())

		done := make(chan error, 1)
		go func() { done <- srv.Listen(ctx, stdinR, stdoutW) }()

		// Cancel the context then close stdin to ensure the read loop unblocks.
		cancel()
		stdinW.Close()

		select {
		case <-done:
			// returned cleanly
		case <-time.After(5 * time.Second):
			t.Fatal("Listen did not return after context cancellation")
		}
	})
}
