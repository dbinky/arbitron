# Phase 2, Task 3: Interactive Session Participation via MCP

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire interactive session participation through MCP with three new tools: `interactive_send_message`, `interactive_end_session`, and `interactive_get_history`. Reuse the existing `ConversationHandler` from the domain layer.

**Architecture:** The MCP adapter calls `ConversationHandler.HandleMessage()` for sending messages (same as the dashboard's WebSocket handler). Responses stream back via MCP notifications (`interactive.token` and `interactive.message` events). Session end uses the same end-session logic. History is read from the node execution record.

**Tech Stack:** Go, github.com/mark3labs/mcp-go

---

## Prerequisites

- Phase 2 Task 1 complete (subscription manager and event streaming)
- Phase 2 Task 2 complete (Streamable HTTP transport)

## Files to Create/Modify

### Create: `adapter/mcp/tools_interactive.go`

Three new MCP tools for interactive session participation.

### Create: `adapter/mcp/tools_interactive_test.go`

### Modify: `adapter/mcp/deps.go`

Add `ConversationHandler` field to `Deps`.

### Modify: `adapter/mcp/register.go`

Add `RegisterInteractiveTools(srv)`.

### Modify: `cmd/arbitron/main.go`

Pass `ConversationHandler` to MCP `Deps`.

---

### Task 3.1: Add ConversationHandler to Deps

- [ ] **Step 1: Add field to Deps struct**

In `adapter/mcp/deps.go`:

```go
type Deps struct {
	// ... existing fields ...
	ConversationHandler engine.ConversationHandler
}
```

- [ ] **Step 2: Commit**

---

### Task 3.2: interactive_send_message Tool

- [ ] **Step 1: Write failing tests**

```go
func TestInteractiveSendMessage_HappyPath(t *testing.T) {
	handler := &mockConversationHandler{}
	deps := &adaptmcp.Deps{ConversationHandler: handler}
	srv := adaptmcp.NewServerWithDeps("test", "0.0.0", deps)
	adaptmcp.RegisterInteractiveTools(srv)

	tools := srv.ListTools()
	var found bool
	for _, tool := range tools {
		if tool.Name == "interactive_send_message" {
			found = true
			break
		}
	}
	assert.True(t, found)
}

func TestInteractiveSendMessage_WrongState(t *testing.T) {
	handler := &mockConversationHandler{
		handleErr: fmt.Errorf("instance is not in waiting state"),
	}
	// Verify the tool returns an error result, not a protocol error
}

func TestInteractiveSendMessage_InstanceNotFound(t *testing.T) {
	handler := &mockConversationHandler{
		handleErr: fmt.Errorf("instance not found"),
	}
	// Verify error propagation
}
```

- [ ] **Step 2: Implement interactive_send_message**

```go
func interactiveSendMessageHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		id, err := parseUUID(request, "instanceId")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		content, _ := request.Params.Arguments["content"].(string)
		if content == "" {
			return gomcp.NewToolResultError("content is required"), nil
		}

		if err := srv.deps.ConversationHandler.HandleMessage(ctx, id, content); err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}

		return marshalResult(map[string]string{
			"status":     "message_sent",
			"instanceId": id.String(),
		})
	}
}
```

The LLM response streams back via MCP notifications through the subscription manager (the client must have an active `instance_subscribe` for this instance to receive `interactive.token` and `interactive.message` events).

- [ ] **Step 3: Run tests, verify pass**
- [ ] **Step 4: Commit**

---

### Task 3.3: interactive_end_session Tool

- [ ] **Step 1: Write failing tests**

```go
func TestInteractiveEndSession_HappyPath(t *testing.T) {
	handler := &mockConversationHandler{}
	// Verify tool calls EndSession on the handler
}

func TestInteractiveEndSession_NotInInteractiveStep(t *testing.T) {
	handler := &mockConversationHandler{
		endErr: fmt.Errorf("instance is not in an interactive step"),
	}
	// Verify error propagation
}
```

- [ ] **Step 2: Implement interactive_end_session**

```go
func interactiveEndSessionHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		id, err := parseUUID(request, "instanceId")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}

		if err := srv.deps.ConversationHandler.EndSession(ctx, id); err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}

		return marshalResult(map[string]string{
			"status":     "session_ended",
			"instanceId": id.String(),
		})
	}
}
```

- [ ] **Step 3: Run tests, verify pass**
- [ ] **Step 4: Commit**

---

### Task 3.4: interactive_get_history Tool

- [ ] **Step 1: Write failing tests**

```go
func TestInteractiveGetHistory_HappyPath(t *testing.T) {
	nodeExecRepo := &mockNodeExecRepo{
		exec: &instance.NodeExecution{
			LLMConversation: map[string]any{
				"messages": []any{
					map[string]any{"role": "assistant", "content": "Hello!"},
					map[string]any{"role": "user", "content": "Hi there"},
				},
			},
		},
	}
	// Verify the tool returns the conversation history
}

func TestInteractiveGetHistory_NoActiveSession(t *testing.T) {
	// Instance exists but is not on an interactive node
}
```

- [ ] **Step 2: Implement interactive_get_history**

```go
func interactiveGetHistoryHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		id, err := parseUUID(request, "instanceId")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}

		inst, err := srv.deps.Instances.Get(ctx, id)
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}

		if inst.CurrentNodeID == nil {
			return gomcp.NewToolResultError("instance has no current node"), nil
		}

		exec, err := srv.deps.NodeExecs.GetByInstanceAndNode(ctx, id, *inst.CurrentNodeID)
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}

		return marshalResult(exec.LLMConversation)
	}
}
```

- [ ] **Step 3: Run tests, verify pass**
- [ ] **Step 4: Commit**

---

### Task 3.5: Register and Wire

- [ ] **Step 1: Create RegisterInteractiveTools function**

```go
func RegisterInteractiveTools(srv *Server) {
	srv.AddTool(
		gomcp.NewTool("interactive_send_message",
			gomcp.WithDescription("Send a user message to an active interactive session"),
			gomcp.WithString("instanceId", gomcp.Required(), gomcp.Description("Instance UUID")),
			gomcp.WithString("content", gomcp.Required(), gomcp.Description("Message content")),
		),
		interactiveSendMessageHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("interactive_end_session",
			gomcp.WithDescription("End an interactive session early"),
			gomcp.WithString("instanceId", gomcp.Required(), gomcp.Description("Instance UUID")),
		),
		interactiveEndSessionHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("interactive_get_history",
			gomcp.WithDescription("Get the full conversation history for an interactive session"),
			gomcp.WithString("instanceId", gomcp.Required(), gomcp.Description("Instance UUID")),
		),
		interactiveGetHistoryHandler(srv),
	)
}
```

- [ ] **Step 2: Update register.go**

```go
func RegisterAllTools(srv *Server) {
	RegisterWorkflowTools(srv)
	RegisterInstanceTools(srv)
	RegisterTraceTools(srv)
	RegisterSystemTools(srv)
	RegisterStreamingTools(srv)
	RegisterInteractiveTools(srv)
}
```

- [ ] **Step 3: Update Deps in main.go**

Pass `conversationHandler` to the MCP Deps struct in both `runServe` and `runMCP`.

- [ ] **Step 4: Run full test suite**

Run: `go test ./... -count=1`
Expected: ALL PASS

- [ ] **Step 5: Commit**

```bash
git add adapter/mcp/ cmd/arbitron/main.go
git commit -m "feat: add interactive session MCP tools (send, end, history)"
```

---

## Acceptance Criteria

- `interactive_send_message` calls `ConversationHandler.HandleMessage()` and returns success
- LLM response streams back via MCP notifications (requires active subscription)
- `interactive_end_session` ends the session and transitions the instance
- `interactive_get_history` returns the full conversation from the node execution record
- Error cases (wrong state, not found, not interactive) return clear error results
- All tests pass
