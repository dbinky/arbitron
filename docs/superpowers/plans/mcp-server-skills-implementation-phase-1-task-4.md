# Phase 1, Task 4: MCP Tool Implementation

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement all MCP tools that mirror the REST API surface — workflow management, instance management, execution traces, system configuration, notifications, and workflow stats.

**Architecture:** Each logical group of tools lives in its own file under `adapter/mcp/`. Each file has a `RegisterXxxTools(srv *Server)` function that registers all tools in that group. Tool handlers are closures that capture `srv.deps` and call domain ports directly. All tools follow the same pattern: parse input, call domain port, return JSON result.

**Tech Stack:** Go, github.com/mark3labs/mcp-go, encoding/json

---

## Prerequisites

- Task 3 complete (MCP server foundation with `Deps` struct available)

## Files to Create

- `adapter/mcp/tools_workflow.go` — workflow management tools (10 tools)
- `adapter/mcp/tools_workflow_test.go`
- `adapter/mcp/tools_instance.go` — instance management tools (9 tools)
- `adapter/mcp/tools_instance_test.go`
- `adapter/mcp/tools_trace.go` — execution trace tools (4 tools)
- `adapter/mcp/tools_trace_test.go`
- `adapter/mcp/tools_system.go` — system, notification, stats tools (7 tools)
- `adapter/mcp/tools_system_test.go`
- `adapter/mcp/register.go` — top-level function that calls all registration functions

---

### Task 4.1: Tool Registration Pattern & Workflow Tools

- [ ] **Step 1: Write failing test for workflow_list tool**

Create `adapter/mcp/tools_workflow_test.go`:

```go
package mcp_test

import (
	"context"
	"testing"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockWorkflowRepo struct {
	workflows []workflow.WorkflowSummary
	loaded    *workflow.Workflow
	loadErr   error
	createErr error
	updateErr error
	deleteErr error
	files     []string
	listErr   error
	writeErr  error
	delFileErr error
}

func (m *mockWorkflowRepo) List(_ context.Context) ([]workflow.WorkflowSummary, error) {
	return m.workflows, nil
}
func (m *mockWorkflowRepo) Load(_ context.Context, name string) (*workflow.Workflow, error) {
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	if m.loaded != nil {
		return m.loaded, nil
	}
	return &workflow.Workflow{Name: name}, nil
}
func (m *mockWorkflowRepo) SaveNodePositions(_ context.Context, _ string, _ map[string]workflow.NodePosition) error {
	return nil
}
func (m *mockWorkflowRepo) Create(_ context.Context, _ string, _ *workflow.Workflow) error {
	return m.createErr
}
func (m *mockWorkflowRepo) Update(_ context.Context, _ string, _ *workflow.Workflow) error {
	return m.updateErr
}
func (m *mockWorkflowRepo) Delete(_ context.Context, _ string) error {
	return m.deleteErr
}
func (m *mockWorkflowRepo) WriteFile(_ context.Context, _ string, _ string, _ []byte) error {
	return m.writeErr
}
func (m *mockWorkflowRepo) DeleteFile(_ context.Context, _ string, _ string) error {
	return m.delFileErr
}
func (m *mockWorkflowRepo) ListFiles(_ context.Context, _ string) ([]string, error) {
	return m.files, m.listErr
}

func TestWorkflowListTool_HappyPath(t *testing.T) {
	repo := &mockWorkflowRepo{
		workflows: []workflow.WorkflowSummary{
			{Name: "wf-1", Description: "First"},
			{Name: "wf-2", Description: "Second"},
		},
	}
	srv := adaptmcp.NewServerWithDeps("test", "0.0.0", &adaptmcp.Deps{Workflows: repo})
	adaptmcp.RegisterWorkflowTools(srv)

	tools := srv.ListTools()
	require.GreaterOrEqual(t, len(tools), 1)

	var found bool
	for _, tool := range tools {
		if tool.Name == "workflow_list" {
			found = true
			break
		}
	}
	assert.True(t, found, "workflow_list tool should be registered")
}
```

- [ ] **Step 2: Implement workflow tools**

Create `adapter/mcp/tools_workflow.go`:

```go
package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/arbitron/arbitron/domain/workflow"
)

// RegisterWorkflowTools registers all workflow management MCP tools.
func RegisterWorkflowTools(srv *Server) {
	srv.AddTool(
		gomcp.NewTool("workflow_list",
			gomcp.WithDescription("List all workflows in the workspace"),
		),
		workflowListHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("workflow_get",
			gomcp.WithDescription("Get a workflow definition by name"),
			gomcp.WithString("name", gomcp.Required(), gomcp.Description("Workflow name")),
		),
		workflowGetHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("workflow_create",
			gomcp.WithDescription("Create a new workflow with its manifest"),
			gomcp.WithString("manifest", gomcp.Required(), gomcp.Description("Workflow manifest JSON")),
		),
		workflowCreateHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("workflow_update",
			gomcp.WithDescription("Update a workflow manifest"),
			gomcp.WithString("name", gomcp.Required(), gomcp.Description("Workflow name")),
			gomcp.WithString("manifest", gomcp.Required(), gomcp.Description("Updated workflow manifest JSON")),
		),
		workflowUpdateHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("workflow_delete",
			gomcp.WithDescription("Delete a workflow and its folder"),
			gomcp.WithString("name", gomcp.Required(), gomcp.Description("Workflow name")),
		),
		workflowDeleteHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("workflow_validate",
			gomcp.WithDescription("Validate a workflow definition"),
			gomcp.WithString("name", gomcp.Required(), gomcp.Description("Workflow name")),
		),
		workflowValidateHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("workflow_list_files",
			gomcp.WithDescription("List files in a workflow folder"),
			gomcp.WithString("name", gomcp.Required(), gomcp.Description("Workflow name")),
		),
		workflowListFilesHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("workflow_read_file",
			gomcp.WithDescription("Read a file from a workflow folder"),
			gomcp.WithString("name", gomcp.Required(), gomcp.Description("Workflow name")),
			gomcp.WithString("path", gomcp.Required(), gomcp.Description("Relative file path")),
		),
		workflowReadFileHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("workflow_write_file",
			gomcp.WithDescription("Write or overwrite a file in a workflow folder"),
			gomcp.WithString("name", gomcp.Required(), gomcp.Description("Workflow name")),
			gomcp.WithString("path", gomcp.Required(), gomcp.Description("Relative file path")),
			gomcp.WithString("content", gomcp.Required(), gomcp.Description("File content")),
		),
		workflowWriteFileHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("workflow_delete_file",
			gomcp.WithDescription("Delete a file from a workflow folder"),
			gomcp.WithString("name", gomcp.Required(), gomcp.Description("Workflow name")),
			gomcp.WithString("path", gomcp.Required(), gomcp.Description("Relative file path")),
		),
		workflowDeleteFileHandler(srv),
	)
}

func workflowListHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		summaries, err := srv.deps.Workflows.List(ctx)
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		return marshalResult(summaries)
	}
}

func workflowGetHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		name, _ := request.Params.Arguments["name"].(string)
		wf, err := srv.deps.Workflows.Load(ctx, name)
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		return marshalResult(wf)
	}
}

func workflowCreateHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		manifestStr, _ := request.Params.Arguments["manifest"].(string)
		var wf workflow.Workflow
		if err := json.Unmarshal([]byte(manifestStr), &wf); err != nil {
			return gomcp.NewToolResultError("invalid manifest JSON: " + err.Error()), nil
		}
		if err := srv.deps.Workflows.Create(ctx, wf.Name, &wf); err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		return marshalResult(map[string]string{"status": "created", "name": wf.Name})
	}
}

func workflowUpdateHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		name, _ := request.Params.Arguments["name"].(string)
		manifestStr, _ := request.Params.Arguments["manifest"].(string)
		var wf workflow.Workflow
		if err := json.Unmarshal([]byte(manifestStr), &wf); err != nil {
			return gomcp.NewToolResultError("invalid manifest JSON: " + err.Error()), nil
		}
		if err := srv.deps.Workflows.Update(ctx, name, &wf); err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		return marshalResult(map[string]string{"status": "updated", "name": name})
	}
}

func workflowDeleteHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		name, _ := request.Params.Arguments["name"].(string)
		if err := srv.deps.Workflows.Delete(ctx, name); err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		return marshalResult(map[string]string{"status": "deleted", "name": name})
	}
}

func workflowValidateHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		name, _ := request.Params.Arguments["name"].(string)
		wf, err := srv.deps.Workflows.Load(ctx, name)
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		errs := workflow.Validate(wf, srv.deps.FileReader)
		valid := len(errs) == 0
		return marshalResult(map[string]any{"valid": valid, "errors": errs})
	}
}

func workflowListFilesHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		name, _ := request.Params.Arguments["name"].(string)
		files, err := srv.deps.Workflows.ListFiles(ctx, name)
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		return marshalResult(files)
	}
}

func workflowReadFileHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		name, _ := request.Params.Arguments["name"].(string)
		path, _ := request.Params.Arguments["path"].(string)
		content, err := srv.deps.FileReader.ReadFile(name, path)
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		return gomcp.NewToolResultText(string(content)), nil
	}
}

func workflowWriteFileHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		name, _ := request.Params.Arguments["name"].(string)
		path, _ := request.Params.Arguments["path"].(string)
		content, _ := request.Params.Arguments["content"].(string)
		if err := srv.deps.Workflows.WriteFile(ctx, name, path, []byte(content)); err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		return marshalResult(map[string]string{"status": "written", "path": path})
	}
}

func workflowDeleteFileHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		name, _ := request.Params.Arguments["name"].(string)
		path, _ := request.Params.Arguments["path"].(string)
		if err := srv.deps.Workflows.DeleteFile(ctx, name, path); err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		return marshalResult(map[string]string{"status": "deleted", "path": path})
	}
}

// marshalResult converts any value to a JSON text tool result.
func marshalResult(v any) (*gomcp.CallToolResult, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return gomcp.NewToolResultError("failed to marshal result: " + err.Error()), nil
	}
	return gomcp.NewToolResultText(string(data)), nil
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./adapter/mcp/ -run TestWorkflow -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add adapter/mcp/tools_workflow.go adapter/mcp/tools_workflow_test.go
git commit -m "feat: implement MCP workflow management tools"
```

---

### Task 4.2: Instance Management Tools

- [ ] **Step 1: Write failing tests for instance tools**

Create `adapter/mcp/tools_instance_test.go` with tests for `instance_list`, `instance_get`, `instance_trigger`, and state-change operations (pause, resume, cancel, retry, rerun, set_priority). Test:

- **Happy path:** Each tool calls the correct domain port
- **Failure:** Tool reports errors from domain layer (e.g., instance not found, wrong state)
- **Edge case:** Missing required parameters return clear errors

- [ ] **Step 2: Implement instance tools**

Create `adapter/mcp/tools_instance.go`:

```go
package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/arbitron/arbitron/domain/instance"
)

// RegisterInstanceTools registers all instance management MCP tools.
func RegisterInstanceTools(srv *Server) {
	srv.AddTool(
		gomcp.NewTool("instance_list",
			gomcp.WithDescription("List workflow instances with optional filters"),
			gomcp.WithString("state", gomcp.Description("Filter by state (running, queued, completed, failed, cancelled, paused, waiting)")),
			gomcp.WithString("workflowName", gomcp.Description("Filter by workflow name")),
		),
		instanceListHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("instance_get",
			gomcp.WithDescription("Get details of a workflow instance"),
			gomcp.WithString("id", gomcp.Required(), gomcp.Description("Instance UUID")),
		),
		instanceGetHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("instance_trigger",
			gomcp.WithDescription("Trigger a new workflow instance"),
			gomcp.WithString("workflowName", gomcp.Required(), gomcp.Description("Name of the workflow to trigger")),
			gomcp.WithString("variables", gomcp.Description("JSON object of input variables")),
			gomcp.WithString("priority", gomcp.Description("Priority: high, normal, or low (default: normal)")),
		),
		instanceTriggerHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("instance_pause",
			gomcp.WithDescription("Pause a running workflow instance"),
			gomcp.WithString("id", gomcp.Required(), gomcp.Description("Instance UUID")),
		),
		instanceStateHandler(srv, "pause"),
	)

	srv.AddTool(
		gomcp.NewTool("instance_resume",
			gomcp.WithDescription("Resume a paused workflow instance"),
			gomcp.WithString("id", gomcp.Required(), gomcp.Description("Instance UUID")),
		),
		instanceStateHandler(srv, "resume"),
	)

	srv.AddTool(
		gomcp.NewTool("instance_cancel",
			gomcp.WithDescription("Cancel a workflow instance"),
			gomcp.WithString("id", gomcp.Required(), gomcp.Description("Instance UUID")),
		),
		instanceStateHandler(srv, "cancel"),
	)

	srv.AddTool(
		gomcp.NewTool("instance_retry",
			gomcp.WithDescription("Retry a failed workflow instance with optional variable edits"),
			gomcp.WithString("id", gomcp.Required(), gomcp.Description("Instance UUID")),
			gomcp.WithString("variables", gomcp.Description("JSON object of variable edits")),
		),
		instanceRetryHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("instance_rerun",
			gomcp.WithDescription("Re-run a terminal workflow instance"),
			gomcp.WithString("id", gomcp.Required(), gomcp.Description("Instance UUID")),
		),
		instanceRerunHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("instance_set_priority",
			gomcp.WithDescription("Change priority of a queued or waiting instance"),
			gomcp.WithString("id", gomcp.Required(), gomcp.Description("Instance UUID")),
			gomcp.WithString("priority", gomcp.Required(), gomcp.Description("Priority: high, normal, or low")),
		),
		instanceSetPriorityHandler(srv),
	)
}

func instanceListHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		filters := instance.ListFilters{}
		if state, ok := request.Params.Arguments["state"].(string); ok && state != "" {
			s := instance.State(state)
			filters.State = &s
		}
		if wfName, ok := request.Params.Arguments["workflowName"].(string); ok && wfName != "" {
			filters.WorkflowName = &wfName
		}
		instances, err := srv.deps.Instances.List(ctx, filters)
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		return marshalResult(instances)
	}
}

func instanceGetHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		id, err := parseUUID(request, "id")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		inst, err := srv.deps.Instances.Get(ctx, id)
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		return marshalResult(inst)
	}
}

func instanceTriggerHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		wfName, _ := request.Params.Arguments["workflowName"].(string)
		priority, _ := request.Params.Arguments["priority"].(string)
		if priority == "" {
			priority = "normal"
		}

		var vars map[string]any
		if varsStr, ok := request.Params.Arguments["variables"].(string); ok && varsStr != "" {
			if err := json.Unmarshal([]byte(varsStr), &vars); err != nil {
				return gomcp.NewToolResultError("invalid variables JSON: " + err.Error()), nil
			}
		}

		id, started, err := srv.deps.Engine.Trigger(wfName, vars, priority)
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}

		state := "queued"
		if started {
			state = "running"
		}
		return marshalResult(map[string]any{"instanceId": id.String(), "state": state})
	}
}

func instanceStateHandler(srv *Server, action string) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		id, err := parseUUID(request, "id")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}

		switch action {
		case "pause":
			err = srv.deps.Engine.Pause(id)
		case "resume":
			err = srv.deps.Engine.Resume(id)
		case "cancel":
			err = srv.deps.Engine.Cancel(id)
		}
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		return marshalResult(map[string]string{"status": action + "d", "instanceId": id.String()})
	}
}

func instanceRetryHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		id, err := parseUUID(request, "id")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}

		var vars map[string]any
		if varsStr, ok := request.Params.Arguments["variables"].(string); ok && varsStr != "" {
			if err := json.Unmarshal([]byte(varsStr), &vars); err != nil {
				return gomcp.NewToolResultError("invalid variables JSON: " + err.Error()), nil
			}
		}

		if err := srv.deps.Engine.Retry(id, vars); err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		return marshalResult(map[string]string{"status": "retrying", "instanceId": id.String()})
	}
}

func instanceRerunHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		id, err := parseUUID(request, "id")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		newID, err := srv.deps.Engine.Rerun(id)
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		return marshalResult(map[string]string{"status": "rerun", "newInstanceId": newID.String()})
	}
}

func instanceSetPriorityHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		id, err := parseUUID(request, "id")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		priority, _ := request.Params.Arguments["priority"].(string)
		if err := srv.deps.Engine.SetPriority(id, priority); err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		return marshalResult(map[string]string{"status": "priority_updated", "instanceId": id.String()})
	}
}

// parseUUID extracts and parses a UUID from tool arguments.
func parseUUID(request gomcp.CallToolRequest, key string) (uuid.UUID, error) {
	str, _ := request.Params.Arguments[key].(string)
	if str == "" {
		return uuid.Nil, fmt.Errorf("%s is required", key)
	}
	id, err := uuid.Parse(str)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid %s: %s", key, str)
	}
	return id, nil
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./adapter/mcp/ -run TestInstance -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add adapter/mcp/tools_instance.go adapter/mcp/tools_instance_test.go
git commit -m "feat: implement MCP instance management tools"
```

---

### Task 4.3: Trace, System, Notification & Stats Tools

- [ ] **Step 1: Write failing tests for trace tools**

- [ ] **Step 2: Implement trace tools**

Create `adapter/mcp/tools_trace.go` with 4 tools:
- `trace_get` — calls `NodeExecs.ListByInstance(ctx, instanceID)`
- `trace_get_node` — calls `NodeExecs.GetByInstanceAndNode(ctx, instanceID, nodeID)`
- `variables_get` — calls `Instances.Get(ctx, id)` and returns `.Variables`
- `variables_history` — calls `Snapshots.ListByInstance(ctx, instanceID)`

- [ ] **Step 3: Implement system, notification & stats tools**

Create `adapter/mcp/tools_system.go` with 7 tools:
- `system_config` — reads from `srv.deps.Config` and queue counts from `srv.deps.QueueRepo`
- `system_update_queue_config` — updates caps via the config and queue manager
- `notification_list` — calls `Notifications.List(ctx, filters)`
- `notification_unread_count` — calls `Notifications.UnreadCount(ctx)`
- `notification_mark_read` — calls `Notifications.MarkRead(ctx, ids)`
- `notification_dismiss` — calls `Notifications.Dismiss(ctx, id)`
- `workflow_stats` — calls `Instances.Stats(ctx, workflowName, from, to)`

Each tool follows the same pattern: parse arguments, call domain port, return JSON result.

- [ ] **Step 4: Run all tests**

Run: `go test ./adapter/mcp/ -v`
Expected: ALL PASS

- [ ] **Step 5: Commit**

```bash
git add adapter/mcp/tools_trace.go adapter/mcp/tools_trace_test.go adapter/mcp/tools_system.go adapter/mcp/tools_system_test.go
git commit -m "feat: implement MCP trace, system, notification & stats tools"
```

---

### Task 4.4: Tool Registration Entry Point

- [ ] **Step 1: Create register.go**

Create `adapter/mcp/register.go`:

```go
package mcp

// RegisterAllTools registers all MCP tools on the server.
func RegisterAllTools(srv *Server) {
	RegisterWorkflowTools(srv)
	RegisterInstanceTools(srv)
	RegisterTraceTools(srv)
	RegisterSystemTools(srv)
}
```

- [ ] **Step 2: Write test that verifies total tool count**

```go
func TestRegisterAllTools_Count(t *testing.T) {
	srv := adaptmcp.NewServerWithDeps("test", "0.0.0", &adaptmcp.Deps{})
	adaptmcp.RegisterAllTools(srv)

	tools := srv.ListTools()
	// 10 workflow + 9 instance + 4 trace + 7 system = 30
	assert.Equal(t, 30, len(tools))
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./adapter/mcp/ -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add adapter/mcp/register.go adapter/mcp/server_test.go
git commit -m "feat: add RegisterAllTools entry point for MCP server"
```

---

## Acceptance Criteria

- 30 MCP tools registered covering workflow, instance, trace, system, notification, and stats
- Each tool calls the correct domain port with correct arguments
- Tool errors propagate domain errors as MCP tool result errors
- `RegisterAllTools` wires everything in one call
- All tools are discoverable via `ListTools()`
- All tests pass across 5 scenario categories
