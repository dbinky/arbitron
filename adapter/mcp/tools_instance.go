package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
)

// isValidPriority reports whether p is one of the three accepted priority values.
func isValidPriority(p string) bool {
	return p == string(instance.PriorityHigh) || p == string(instance.PriorityNormal) || p == string(instance.PriorityLow)
}

// RegisterInstanceTools registers all instance management tools with the server.
func RegisterInstanceTools(srv *Server) {
	srv.AddTool(
		gomcp.NewTool("instance_list",
			gomcp.WithDescription("List workflow instances with optional filters"),
			gomcp.WithString("state", gomcp.Description("Filter by state (queued, running, waiting, completed, failed, cancelled, paused)")),
			gomcp.WithString("workflowName", gomcp.Description("Filter by workflow name")),
			gomcp.WithString("parent", gomcp.Description(
				"Narrow by position in the spawn hierarchy: omit for all runs, "+
					"\"roots\" for top-level runs only, or an instance UUID for that run's children")),
			gomcp.WithNumber("limit", gomcp.Description(
				fmt.Sprintf("Maximum rows to return (default %d, maximum %d)", mcpListDefaultLimit, mcpListMaxLimit))),
			gomcp.WithNumber("offset", gomcp.Description(
				"Rows to skip, for paging past the limit")),
		),
		instanceListHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("instance_get",
			gomcp.WithDescription("Get a workflow instance by ID"),
			gomcp.WithString("id", gomcp.Required(), gomcp.Description("Instance UUID")),
		),
		instanceGetHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("instance_trigger",
			gomcp.WithDescription("Trigger a new workflow instance"),
			gomcp.WithString("workflowName", gomcp.Required(), gomcp.Description("Name of the workflow to trigger")),
			gomcp.WithString("variables", gomcp.Description("JSON-encoded variables map")),
			gomcp.WithString("priority", gomcp.Description("Priority: high, normal, or low")),
			gomcp.WithString("department", gomcp.Description("Optional: stamp the run under one of your departments (must be one you belong to)")),
		),
		instanceTriggerHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("instance_pause",
			gomcp.WithDescription("Pause a running workflow instance"),
			gomcp.WithString("id", gomcp.Required(), gomcp.Description("Instance UUID")),
		),
		instancePauseHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("instance_resume",
			gomcp.WithDescription("Resume a paused workflow instance"),
			gomcp.WithString("id", gomcp.Required(), gomcp.Description("Instance UUID")),
		),
		instanceResumeHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("instance_cancel",
			gomcp.WithDescription("Cancel a workflow instance"),
			gomcp.WithString("id", gomcp.Required(), gomcp.Description("Instance UUID")),
		),
		instanceCancelHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("instance_retry",
			gomcp.WithDescription("Retry a failed workflow instance with optional variable edits"),
			gomcp.WithString("id", gomcp.Required(), gomcp.Description("Instance UUID")),
			gomcp.WithString("variables", gomcp.Description("JSON-encoded variable edits")),
		),
		instanceRetryHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("instance_rerun",
			gomcp.WithDescription("Re-run a completed or failed workflow instance as a new instance"),
			gomcp.WithString("id", gomcp.Required(), gomcp.Description("Instance UUID")),
		),
		instanceRerunHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("instance_set_priority",
			gomcp.WithDescription("Change the priority of a workflow instance"),
			gomcp.WithString("id", gomcp.Required(), gomcp.Description("Instance UUID")),
			gomcp.WithString("priority", gomcp.Required(), gomcp.Description("Priority: high, normal, or low")),
		),
		instanceSetPriorityHandler(srv),
	)
}

// Paging bounds for instance_list. The default matches the repository's
// historical implicit page size; the maximum bounds one tool call's payload.
const (
	mcpListDefaultLimit = 50
	mcpListMaxLimit     = 500
)

func instanceListHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		args := request.GetArguments()

		var filters instance.ListFilters
		if stateStr, _ := args["state"].(string); stateStr != "" {
			s := instance.State(stateStr)
			filters.State = &s
		}
		if wfName, _ := args["workflowName"].(string); wfName != "" {
			filters.WorkflowName = &wfName
		}
		// The parent selector is the ONE caller-supplied narrowing here. It
		// composes with — and can never replace — the owner/department constraints
		// ListEnriched service-sets from the request identity. Decoding goes
		// through the SAME shared domain parser the REST handler uses, so the two
		// surfaces cannot drift on the wire format.
		//
		// A malformed selector is an error, never a silent unfiltered list.
		parentRaw, _ := args["parent"].(string)
		scope, parentID, perr := instance.ParseParentScope(parentRaw)
		if perr != nil {
			return gomcp.NewToolResultError(perr.Error()), nil
		}
		filters.ParentScope = scope
		filters.ParentInstanceID = parentID

		// REAL paging, not a silent default. Without it the parent scope
		// inherited the repository's unspoken 50-row cap, so a run with more
		// children than that returned a truncated list INDISTINGUISHABLE from a
		// complete one — an agent reading it would conclude the extra children do
		// not exist. An explicit, documented limit with an offset is what makes
		// "there may be more" expressible at all.
		filters.Limit = mcpListDefaultLimit
		if v, ok := args["limit"].(float64); ok && v > 0 {
			filters.Limit = min(int(v), mcpListMaxLimit)
		}
		if v, ok := args["offset"].(float64); ok && v > 0 {
			filters.Offset = int(v)
		}

		enriched, err := srv.deps.Engine.ListEnriched(ctx, filters)
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("failed to list instances: %s", err)), nil
		}
		if enriched == nil {
			enriched = []instance.EnrichedInstanceSummary{}
		}
		// The result stays a BARE ARRAY: the REST endpoint returns one, and a
		// parity test pins the two surfaces to the same shape. Truncation is
		// expressible because the limit is now documented and caller-set — a full
		// page means "ask for the next offset" — rather than an unspoken 50 the
		// caller could not distinguish from a complete list.
		return MarshalResult(enriched)
	}
}

func instanceGetHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		id, err := ParseUUID(request, "id")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}

		inst, err := srv.deps.Engine.GetInstance(ctx, id)
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("failed to get instance: %s", err)), nil
		}
		if inst == nil {
			return gomcp.NewToolResultError("instance not found"), nil
		}
		return MarshalResult(inst)
	}
}

func instanceTriggerHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		args := request.GetArguments()
		wfName, _ := args["workflowName"].(string)
		if wfName == "" {
			return gomcp.NewToolResultError("workflowName is required"), nil
		}

		var vars map[string]any
		if varsStr, _ := args["variables"].(string); varsStr != "" {
			if err := json.Unmarshal([]byte(varsStr), &vars); err != nil {
				return gomcp.NewToolResultError(fmt.Sprintf("invalid variables JSON: %s", err)), nil
			}
		}

		priority, _ := args["priority"].(string)
		if priority != "" && !isValidPriority(priority) {
			return gomcp.NewToolResultError(fmt.Sprintf("invalid priority %q: must be high, normal, or low", priority)), nil
		}

		department, _ := args["department"].(string)

		id, queued, err := srv.deps.Engine.TriggerWithDepartment(ctx, wfName, vars, priority, department)
		if err != nil {
			if errors.Is(err, engine.ErrInvalidDepartmentOverride) {
				return gomcp.NewToolResultError(fmt.Sprintf("invalid department: %s", err)), nil
			}
			return gomcp.NewToolResultError(fmt.Sprintf("failed to trigger workflow: %s", err)), nil
		}
		return MarshalResult(map[string]any{
			"id":     id.String(),
			"queued": queued,
		})
	}
}

func instancePauseHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		id, err := ParseUUID(request, "id")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}

		if err := srv.deps.Engine.Pause(ctx, id); err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("failed to pause instance: %s", err)), nil
		}
		return MarshalResult(map[string]string{"status": "paused", "id": id.String()})
	}
}

func instanceResumeHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		id, err := ParseUUID(request, "id")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}

		if err := srv.deps.Engine.Resume(ctx, id); err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("failed to resume instance: %s", err)), nil
		}
		return MarshalResult(map[string]string{"status": "resumed", "id": id.String()})
	}
}

func instanceCancelHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		id, err := ParseUUID(request, "id")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}

		if err := srv.deps.Engine.Cancel(ctx, id); err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("failed to cancel instance: %s", err)), nil
		}
		return MarshalResult(map[string]string{"status": "cancelled", "id": id.String()})
	}
}

func instanceRetryHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		id, err := ParseUUID(request, "id")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}

		args := request.GetArguments()
		var vars map[string]any
		if varsStr, _ := args["variables"].(string); varsStr != "" {
			if err := json.Unmarshal([]byte(varsStr), &vars); err != nil {
				return gomcp.NewToolResultError(fmt.Sprintf("invalid variables JSON: %s", err)), nil
			}
		}

		if err := srv.deps.Engine.Retry(ctx, id, vars); err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("failed to retry instance: %s", err)), nil
		}
		return MarshalResult(map[string]string{"status": "retrying", "id": id.String()})
	}
}

func instanceRerunHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		id, err := ParseUUID(request, "id")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}

		newID, err := srv.deps.Engine.Rerun(ctx, id)
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("failed to rerun instance: %s", err)), nil
		}
		return MarshalResult(map[string]string{"status": "rerun", "newId": newID.String()})
	}
}

func instanceSetPriorityHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		id, err := ParseUUID(request, "id")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}

		args := request.GetArguments()
		priority, _ := args["priority"].(string)
		if priority == "" {
			return gomcp.NewToolResultError("priority is required"), nil
		}
		if !isValidPriority(priority) {
			return gomcp.NewToolResultError(fmt.Sprintf("invalid priority %q: must be high, normal, or low", priority)), nil
		}

		if err := srv.deps.Engine.SetPriority(ctx, id, priority); err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("failed to set priority: %s", err)), nil
		}
		return MarshalResult(map[string]string{"status": "updated", "id": id.String(), "priority": priority})
	}
}
