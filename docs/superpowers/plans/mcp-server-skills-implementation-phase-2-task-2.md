# Phase 2, Task 2: Streamable HTTP Transport

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the Streamable HTTP transport to the MCP server, served at `/mcp` on the existing HTTP server. This enables remote Claude Code sessions to connect to the Arbitron MCP server over HTTP.

**Architecture:** The mcp-go library provides a Streamable HTTP transport handler. We mount it at `/mcp` on the existing chi router. The handler shares the same MCP server instance (and therefore the same domain dependencies) as the stdio transport. When `arbitron serve` runs, the `/mcp` endpoint is always available.

**Tech Stack:** Go, github.com/mark3labs/mcp-go, net/http, chi

---

## Prerequisites

- Phase 2 Task 1 complete (event streaming works)

## Files to Modify

### Modify: `adapter/mcp/server.go`

Add a method that returns an `http.Handler` for the Streamable HTTP transport:

```go
// HTTPHandler returns an http.Handler that serves the MCP protocol over
// Streamable HTTP. Mount this at a path (e.g., /mcp) on the HTTP router.
func (s *Server) HTTPHandler() http.Handler {
	return server.NewStreamableHTTPHandler(s.mcpServer)
}
```

### Modify: `adapter/http/server.go`

Mount the MCP handler at `/mcp` in the router. The MCP handler is passed as an optional parameter to `NewRouter`.

### Modify: `cmd/arbitron/main.go`

When building the router in `runServe`, create the MCP server, register tools, and pass the HTTP handler to the router.

### Test: `adapter/mcp/server_test.go`, `adapter/http/server_test.go`

---

### Task 2.1: MCP HTTP Handler

- [ ] **Step 1: Write failing test**

```go
func TestServer_HTTPHandler(t *testing.T) {
	srv := adaptmcp.NewServer("test", "0.0.0")
	handler := srv.HTTPHandler()
	require.NotNil(t, handler)
}
```

- [ ] **Step 2: Implement HTTPHandler method**

Add to `adapter/mcp/server.go`:

```go
func (s *Server) HTTPHandler() http.Handler {
	return server.NewStreamableHTTPHandler(s.mcpServer)
}
```

Note: Consult the mcp-go library for the exact API. The handler accepts HTTP POST requests with JSON-RPC payloads and returns streaming responses for notifications.

- [ ] **Step 3: Run test, verify pass**
- [ ] **Step 4: Commit**

---

### Task 2.2: Mount on Router

- [ ] **Step 1: Write failing test**

Add to `adapter/http/server_test.go`:

```go
func TestRouter_MCPEndpoint(t *testing.T) {
	// Verify /mcp endpoint exists and responds
	mcpHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	router := NewRouter(/* ... minimal deps ... */)
	// After adding MCP handler support to NewRouter

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","method":"initialize","id":1}`))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Should not return 404 — the endpoint exists
	assert.NotEqual(t, http.StatusNotFound, w.Code)
}
```

- [ ] **Step 2: Add MCP handler parameter to NewRouter**

Modify `NewRouter` signature to accept an optional MCP handler:

```go
func NewRouter(
	/* ... existing params ... */
	mcpHandler http.Handler, // new parameter, may be nil
) chi.Router {
	// ... existing setup ...

	// Mount MCP endpoint
	if mcpHandler != nil {
		r.Mount("/mcp", mcpHandler)
	}

	return r
}
```

Update all existing callers of `NewRouter` to pass the new parameter (nil in tests that don't need it).

- [ ] **Step 3: Wire in main.go**

In `runServe`, create the MCP server and pass its HTTP handler to `NewRouter`:

```go
	mcpDeps := &adaptmcp.Deps{ /* ... same as arbitron mcp ... */ }
	mcpSrv := adaptmcp.NewServerWithDeps("arbitron", version, mcpDeps)
	adaptmcp.RegisterAllTools(mcpSrv)

	router := adapthttp.NewRouter(
		eng, workflowRepo, instanceRepo, /* ... */
		mcpSrv.HTTPHandler(), // MCP Streamable HTTP handler
	)
```

- [ ] **Step 4: Run all tests**

Run: `go test ./... -count=1`
Expected: ALL PASS

- [ ] **Step 5: Commit**

```bash
git add adapter/mcp/server.go adapter/http/server.go cmd/arbitron/main.go
git commit -m "feat: mount MCP Streamable HTTP endpoint at /mcp"
```

---

## Acceptance Criteria

- `POST /mcp` accepts JSON-RPC MCP requests and returns responses
- Streaming notifications flow over the HTTP connection
- The endpoint is always available when `arbitron serve` runs
- No additional CLI flag needed for Streamable HTTP (unlike `--mcp` for stdio)
- All existing tests pass with the new router parameter
