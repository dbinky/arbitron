package mcp_test

import (
	"testing"

	gomcp "github.com/mark3labs/mcp-go/mcp"

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
