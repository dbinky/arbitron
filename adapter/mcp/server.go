// Package mcp provides the MCP (Model Context Protocol) server adapter for Arbitron.
// It wraps the mcp-go library and exposes workflow orchestration capabilities as
// MCP tools that can be consumed by LLM clients via the stdio transport.
package mcp

import (
	"context"
	"net/http"
	"os"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Server wraps the mcp-go MCPServer, providing a higher-level API for
// registering Arbitron workflow tools and serving them over stdio.
type Server struct {
	mcpServer *server.MCPServer
	tools     []gomcp.Tool
	deps      *Deps
	subs      *SubscriptionManager
}

// NewServer constructs a new MCP Server with the given name and version.
// Tools must be added via AddTool before serving.
func NewServer(name, version string) *Server {
	s := server.NewMCPServer(name, version,
		server.WithToolCapabilities(true),
	)
	return &Server{mcpServer: s}
}

// NewServerWithDeps constructs a new MCP Server pre-wired with domain dependencies.
func NewServerWithDeps(name, version string, deps *Deps) *Server {
	srv := NewServer(name, version)
	srv.deps = deps
	if deps != nil && deps.EventBus != nil {
		srv.subs = NewSubscriptionManager(deps.EventBus)
	}
	return srv
}

// AddTool registers a tool definition and its handler with the underlying MCPServer.
func (s *Server) AddTool(tool gomcp.Tool, handler server.ToolHandlerFunc) {
	s.tools = append(s.tools, tool)
	s.mcpServer.AddTool(tool, handler)
}

// ListTools returns the set of tools registered with this server.
func (s *Server) ListTools() []gomcp.Tool {
	return s.tools
}

// MCPServer returns the underlying mcp-go MCPServer for advanced use cases.
func (s *Server) MCPServer() *server.MCPServer {
	return s.mcpServer
}

// Subscriptions returns the SubscriptionManager for event streaming, or nil
// if no EventBus was provided.
func (s *Server) Subscriptions() *SubscriptionManager {
	return s.subs
}

// HTTPHandler returns an http.Handler for serving the MCP protocol over
// Streamable HTTP. Mount at a path (e.g., /mcp) on the HTTP router. The
// returned handler accepts POST (JSON-RPC), GET (SSE streaming), and DELETE
// (session termination) requests per the MCP Streamable HTTP specification.
func (s *Server) HTTPHandler() http.Handler {
	return server.NewStreamableHTTPServer(s.mcpServer)
}

// ServeStdio starts the MCP server on stdin/stdout. This blocks until the
// client disconnects, the context is cancelled, or a termination signal is
// received. The ctx parameter controls the server's lifecycle; cancelling it
// will shut down the stdio server.
func (s *Server) ServeStdio(ctx context.Context) error {
	stdio := server.NewStdioServer(s.mcpServer)
	return stdio.Listen(ctx, os.Stdin, os.Stdout)
}

// Listen starts the MCP server on the provided reader/writer pair. This is
// useful for testing (piped IO) or embedding scenarios where os.Stdin/Stdout
// are not appropriate.
func (s *Server) Listen(ctx context.Context, stdin *os.File, stdout *os.File) error {
	stdio := server.NewStdioServer(s.mcpServer)
	return stdio.Listen(ctx, stdin, stdout)
}
