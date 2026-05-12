# Phase 1, Task 3: MCP Server Foundation & stdio Transport

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the MCP server adapter with JSON-RPC protocol handling, stdio transport, tool registration framework, and MCP initialize/tools_list support.

**Architecture:** A new `adapter/mcp/` package implements the MCP server. It uses the `github.com/mark3labs/mcp-go` library for the MCP protocol. The server receives domain port interfaces (same as the HTTP adapter) and exposes them as MCP tools. This task builds the foundation; Tasks 4 and 5 register the actual tools.

**Tech Stack:** Go, github.com/mark3labs/mcp-go, encoding/json, os, io

---

## Prerequisites

- Tasks 1-2 complete (WorkflowRepository write operations and REST endpoints available)

## Files to Create

### Create: `adapter/mcp/server.go`

The core MCP server that:
- Accepts domain port interfaces as constructor arguments
- Registers tools via a registration pattern
- Handles the MCP protocol (initialize, tools/list, tools/call)
- Supports stdio transport

### Create: `adapter/mcp/server_test.go`

Tests for the MCP server foundation.

---

### Task 3.1: Add mcp-go dependency

- [ ] **Step 1: Add the mcp-go library**

Run: `go get github.com/mark3labs/mcp-go`

- [ ] **Step 2: Verify it's in go.mod**

Run: `grep mcp-go go.mod`
Expected: `github.com/mark3labs/mcp-go vX.Y.Z`

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: add mcp-go dependency"
```

---

### Task 3.2: Create MCP Server Adapter

- [ ] **Step 1: Write failing test for server construction and tool listing**

Create `adapter/mcp/server_test.go`:

```go
package mcp_test

import (
	"context"
	"testing"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewServer_NoTools(t *testing.T) {
	srv := adaptmcp.NewServer("test-arbitron", "0.0.0")
	require.NotNil(t, srv)
}

func TestServer_ToolCount(t *testing.T) {
	srv := adaptmcp.NewServer("test-arbitron", "0.0.0")
	// No tools registered yet — the tool list should be empty
	tools := srv.ListTools()
	assert.Equal(t, 0, len(tools))
}
```

- [ ] **Step 2: Implement the MCP server foundation**

Create `adapter/mcp/server.go`:

```go
// Package mcp provides an MCP (Model Context Protocol) server adapter for the Arbitron.
package mcp

import (
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Server wraps the mcp-go server with Arbitron-specific domain dependencies.
type Server struct {
	mcpServer *server.MCPServer
	tools     []mcp.Tool
}

// NewServer creates a new MCP server with the given name and version.
func NewServer(name, version string) *Server {
	s := server.NewMCPServer(name, version,
		server.WithToolCapabilities(true),
	)
	return &Server{
		mcpServer: s,
	}
}

// ListTools returns all registered tools.
func (s *Server) ListTools() []mcp.Tool {
	return s.tools
}

// AddTool registers a tool with its handler on the MCP server.
func (s *Server) AddTool(tool mcp.Tool, handler server.ToolHandlerFunc) {
	s.tools = append(s.tools, tool)
	s.mcpServer.AddTool(tool, handler)
}

// MCPServer returns the underlying mcp-go server for transport wiring.
func (s *Server) MCPServer() *server.MCPServer {
	return s.mcpServer
}
```

- [ ] **Step 3: Run tests to verify they pass**

Run: `go test ./adapter/mcp/ -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add adapter/mcp/server.go adapter/mcp/server_test.go
git commit -m "feat: create MCP server adapter foundation"
```

---

### Task 3.3: stdio Transport Runner

- [ ] **Step 1: Write test for stdio transport setup**

Add to `adapter/mcp/server_test.go`:

```go
func TestServer_ServeStdio_ContextCancellation(t *testing.T) {
	srv := adaptmcp.NewServer("test-arbitron", "0.0.0")

	// Create pipes to simulate stdin/stdout
	stdinR, stdinW, _ := os.Pipe()
	stdoutR, stdoutW, _ := os.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- srv.ServeStdio(ctx, stdinR, stdoutW)
	}()

	// Cancel context; server should exit cleanly
	cancel()
	stdinW.Close()

	err := <-done
	assert.NoError(t, err)

	stdoutR.Close()
}
```

- [ ] **Step 2: Implement ServeStdio**

Add to `adapter/mcp/server.go`:

```go
import (
	"context"
	"io"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ServeStdio runs the MCP server over stdin/stdout using the stdio transport.
// It blocks until the context is cancelled or the input stream is closed.
func (s *Server) ServeStdio(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	transport := server.NewStdioTransport()
	return transport.Run(ctx, s.mcpServer, stdin, stdout)
}
```

Note: The exact `mcp-go` API may vary. Consult the library docs and adapt accordingly. The key contract is: read JSON-RPC from stdin, write responses to stdout, stop when context is cancelled.

- [ ] **Step 3: Run tests to verify they pass**

Run: `go test ./adapter/mcp/ -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add adapter/mcp/server.go adapter/mcp/server_test.go
git commit -m "feat: add stdio transport to MCP server"
```

---

### Task 3.4: Domain Dependencies Struct

- [ ] **Step 1: Create a dependencies struct for tool registration**

The MCP tools need access to domain ports. Rather than passing 10+ interfaces to each tool handler, create a `Deps` struct that holds all of them.

Create `adapter/mcp/deps.go`:

```go
package mcp

import (
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/notification"
	"github.com/arbitron/arbitron/domain/workflow"
)

// EngineService combines both WorkflowService and InstanceService interfaces.
type EngineService interface {
	Trigger(workflowName string, variables map[string]any, priority string) (uuid.UUID, bool, error)
	Pause(instanceID uuid.UUID) error
	Resume(instanceID uuid.UUID) error
	Cancel(instanceID uuid.UUID) error
	Retry(instanceID uuid.UUID, variableEdits map[string]any) error
	Rerun(instanceID uuid.UUID) (uuid.UUID, error)
	SetPriority(id uuid.UUID, priority string) error
}

// Deps holds all domain dependencies needed by MCP tool handlers.
type Deps struct {
	Engine       EngineService
	Workflows    workflow.WorkflowRepository
	Instances    instance.InstanceRepository
	NodeExecs    instance.NodeExecutionRepository
	Snapshots    instance.VariableSnapshotRepository
	FileReader   engine.FileReader
	EventBus     engine.EventBus
	Config       *config.Config
	QueueRepo    instance.QueueRepository
	Notifications notification.Repository
}
```

Add `"github.com/google/uuid"` to imports.

- [ ] **Step 2: Add a constructor that accepts Deps**

Update `adapter/mcp/server.go`:

```go
// NewServerWithDeps creates a new MCP server with domain dependencies for tool registration.
func NewServerWithDeps(name, version string, deps *Deps) *Server {
	s := NewServer(name, version)
	s.deps = deps
	return s
}
```

Add `deps *Deps` field to the `Server` struct.

- [ ] **Step 3: Run tests**

Run: `go test ./adapter/mcp/ -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add adapter/mcp/deps.go adapter/mcp/server.go
git commit -m "feat: add domain dependencies struct for MCP tool handlers"
```

---

## Acceptance Criteria

- `adapter/mcp/` package exists with `Server` type
- `NewServer` constructs a server with MCP capabilities
- `AddTool` registers tools on the underlying mcp-go server
- `ListTools` returns all registered tools
- `ServeStdio` runs the server over provided stdin/stdout streams
- `Deps` struct holds all domain port interfaces needed by tool handlers
- Context cancellation causes `ServeStdio` to exit cleanly
- All tests pass
