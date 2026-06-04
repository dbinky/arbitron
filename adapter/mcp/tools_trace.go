package mcp

import (
	"context"
	"fmt"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/arbitron/arbitron/domain/instance"
)

// RegisterTraceTools registers all trace and variable inspection tools with the server.
func RegisterTraceTools(srv *Server) {
	srv.AddTool(
		gomcp.NewTool("trace_get",
			gomcp.WithDescription("Get all node executions for a workflow instance"),
			gomcp.WithString("id", gomcp.Required(), gomcp.Description("Instance UUID")),
		),
		traceGetHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("trace_get_node",
			gomcp.WithDescription("Get the execution details for a specific node in an instance"),
			gomcp.WithString("instanceId", gomcp.Required(), gomcp.Description("Instance UUID")),
			gomcp.WithString("nodeId", gomcp.Required(), gomcp.Description("Node ID within the workflow")),
		),
		traceGetNodeHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("variables_get",
			gomcp.WithDescription("Get the current variables for a workflow instance"),
			gomcp.WithString("id", gomcp.Required(), gomcp.Description("Instance UUID")),
		),
		variablesGetHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("variables_history",
			gomcp.WithDescription("Get the variable snapshot history for a workflow instance"),
			gomcp.WithString("id", gomcp.Required(), gomcp.Description("Instance UUID")),
		),
		variablesHistoryHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("trace_interventions",
			gomcp.WithDescription("Get the intervention audit trail (admin actions) for a workflow instance"),
			gomcp.WithString("id", gomcp.Required(), gomcp.Description("Instance UUID")),
		),
		traceInterventionsHandler(srv),
	)
}

func traceInterventionsHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		id, err := ParseUUID(request, "id")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}

		// Scoped read: GetInterventions returns ErrNotFound for a non-owner
		// non-admin (existence not leaked). Visible to owner + admins.
		ivs, err := srv.deps.Engine.GetInterventions(ctx, id)
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("failed to get interventions: %s", err)), nil
		}
		if ivs == nil {
			ivs = []instance.Intervention{}
		}
		return MarshalResult(ivs)
	}
}

func traceGetHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		id, err := ParseUUID(request, "id")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}

		execs, err := srv.deps.Engine.GetTrace(ctx, id)
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("failed to get trace: %s", err)), nil
		}
		if execs == nil {
			execs = []instance.NodeExecution{}
		}
		return MarshalResult(execs)
	}
}

func traceGetNodeHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		instID, err := ParseUUID(request, "instanceId")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}

		args := request.GetArguments()
		nodeID, _ := args["nodeId"].(string)
		if nodeID == "" {
			return gomcp.NewToolResultError("nodeId is required"), nil
		}

		exec, err := srv.deps.Engine.GetNodeTrace(ctx, instID, nodeID)
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("failed to get node execution: %s", err)), nil
		}
		if exec == nil {
			return gomcp.NewToolResultError("node execution not found"), nil
		}
		return MarshalResult(exec)
	}
}

func variablesGetHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		id, err := ParseUUID(request, "id")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}

		vars, err := srv.deps.Engine.GetVariables(ctx, id)
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("failed to get instance: %s", err)), nil
		}
		return MarshalResult(vars)
	}
}

func variablesHistoryHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		id, err := ParseUUID(request, "id")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}

		snapshots, err := srv.deps.Engine.GetVariableHistory(ctx, id)
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("failed to get variable history: %s", err)), nil
		}
		if snapshots == nil {
			snapshots = []instance.VariableSnapshot{}
		}
		return MarshalResult(snapshots)
	}
}
