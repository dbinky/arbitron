package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/workflow"
)

// registerWorkflowGroupTools registers the parallel_group authoring tools. It
// is called from RegisterWorkflowTools so every surface that exposes the
// workflow tools exposes these too. Each handler is a thin delegate to the
// workflow.Service group operations (authorization, validation and atomic
// persistence live there).
func registerWorkflowGroupTools(srv *Server) {
	srv.AddTool(
		gomcp.NewTool("workflow_group_upsert",
			gomcp.WithDescription("Create or revise ONE parallel_group node: all settings, ordered tasks with their inputs, and the success/failure continuation targets. "+
				"Rewrites the group node and all of its outgoing edges atomically and re-validates; nothing is saved when validation fails "+
				`(the result is then {"valid":false,"errors":[...]}). Body nodes and edges INTO the group are authored with workflow_create/workflow_update.`+
				ParallelGroupGuidance),
			gomcp.WithString("name", gomcp.Required(), gomcp.Description("Workflow name")),
			gomcp.WithString("group", gomcp.Required(), gomcp.Description(
				`JSON-encoded group: {"nodeId":"g","label":"...","description":"...","entrypoint":true|false (omit to keep),`+
					`"config":{"form":"branches"|"workflows","tasks":[{"key":"k","entry":"<node id>"} or {"key":"k","workflow":"<name>","inputs":{...}}],`+
					`"completionRule":"all_succeed"|"first_success","unfinishedBehavior":"wait_for_remaining"|"cancel_unfinished"|"finish_independently",`+
					`"gatherMode":"results"|"results_and_files","repoPath":"...","resolverProfile":"<agent profile>","resolverPromptFile":"<file>"},`+
					`"successTarget":"<node id>","failureTarget":"<node id>"}`)),
		),
		workflowGroupUpsertHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("workflow_group_get",
			gomcp.WithDescription("Inspect parallel_group nodes (read-only): ordered tasks and config, continuation targets, computed branch-body node ids, nested groups, the enclosing group/task, and advisories. Omit nodeId to list every group in workflow order."),
			gomcp.WithString("name", gomcp.Required(), gomcp.Description("Workflow name")),
			gomcp.WithString("nodeId", gomcp.Description("Group node id; omit to list all groups")),
		),
		workflowGroupGetHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("workflow_group_delete",
			gomcp.WithDescription("Delete one parallel_group node and every edge touching it. Returns retainedNodes (former body nodes and continuation targets left in the workflow) so you can reconnect or remove them. Refused, with structured errors, when the deletion would leave another group invalid."),
			gomcp.WithString("name", gomcp.Required(), gomcp.Description("Workflow name")),
			gomcp.WithString("nodeId", gomcp.Required(), gomcp.Description("Group node id to delete")),
		),
		workflowGroupDeleteHandler(srv),
	)
}

func workflowGroupUpsertHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		args := request.GetArguments()
		name, _ := args["name"].(string)
		if name == "" {
			return gomcp.NewToolResultError("name is required"), nil
		}
		if !safeWorkflowName(name) {
			return gomcp.NewToolResultError(fmt.Sprintf("invalid workflow name: %s", name)), nil
		}
		raw, _ := args["group"].(string)
		if raw == "" {
			return gomcp.NewToolResultError("group is required"), nil
		}
		up, err := decodeGroupUpsert(raw)
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("invalid group JSON: %s", err)), nil
		}
		view, err := workflowSvc(srv).UpsertGroup(ctx, name, up)
		if err != nil {
			return groupToolError(name, "upsert", err), nil
		}
		return MarshalResult(view)
	}
}

func workflowGroupGetHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		args := request.GetArguments()
		name, _ := args["name"].(string)
		if name == "" {
			return gomcp.NewToolResultError("name is required"), nil
		}
		if !safeWorkflowName(name) {
			return gomcp.NewToolResultError(fmt.Sprintf("invalid workflow name: %s", name)), nil
		}
		nodeID, _ := args["nodeId"].(string)
		if nodeID == "" {
			list, err := workflowSvc(srv).ListGroups(ctx, name)
			if err != nil {
				return groupToolError(name, "list", err), nil
			}
			if list == nil {
				list = []workflow.GroupView{}
			}
			return MarshalResult(list)
		}
		view, err := workflowSvc(srv).GetGroup(ctx, name, nodeID)
		if err != nil {
			return groupToolError(name, "get", err), nil
		}
		return MarshalResult(view)
	}
}

func workflowGroupDeleteHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		args := request.GetArguments()
		name, _ := args["name"].(string)
		if name == "" {
			return gomcp.NewToolResultError("name is required"), nil
		}
		if !safeWorkflowName(name) {
			return gomcp.NewToolResultError(fmt.Sprintf("invalid workflow name: %s", name)), nil
		}
		nodeID, _ := args["nodeId"].(string)
		if nodeID == "" {
			return gomcp.NewToolResultError("nodeId is required"), nil
		}
		res, err := workflowSvc(srv).DeleteGroup(ctx, name, nodeID)
		if err != nil {
			return groupToolError(name, "delete", err), nil
		}
		return MarshalResult(res)
	}
}

// decodeGroupUpsert strictly decodes the group argument: unknown fields (a
// typo such as "completion_rule") and trailing data are rejected so the author
// gets actionable feedback instead of a silently ignored setting.
func decodeGroupUpsert(raw string) (workflow.GroupUpsert, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var up workflow.GroupUpsert
	if err := dec.Decode(&up); err != nil {
		return workflow.GroupUpsert{}, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return workflow.GroupUpsert{}, errors.New("unexpected data after the group object")
	}
	return up, nil
}

// groupValidationResult renders a *workflow.GroupValidationError in
// workflow_validate's {"valid":false,"errors":[...]} shape with IsError=true.
// It returns (nil, false) for any other error.
func groupValidationResult(err error) (*gomcp.CallToolResult, bool) {
	var gve *workflow.GroupValidationError
	if !errors.As(err, &gve) {
		return nil, false
	}
	errs := gve.Errors
	if errs == nil {
		errs = []workflow.ValidationError{}
	}
	res, _ := MarshalResult(map[string]any{"valid": false, "errors": errs})
	res.IsError = true
	return res, true
}

// groupToolError maps a group-operation error to a tool result.
func groupToolError(name, op string, err error) *gomcp.CallToolResult {
	if res, ok := groupValidationResult(err); ok {
		return res
	}
	switch {
	case errors.Is(err, workflow.ErrGroupUpsertInvalid):
		return gomcp.NewToolResultError(fmt.Sprintf("invalid group: %s", err))
	case errors.Is(err, workflow.ErrGroupNotFound):
		// The wrapped message already reads `parallel group not found: "<id>" in workflow "<name>"`.
		return gomcp.NewToolResultError(err.Error())
	case errors.Is(err, identity.ErrUnauthenticated):
		return gomcp.NewToolResultError("not authenticated")
	}
	if res, ok := mapLifecycleError(name, err); ok {
		return res
	}
	return gomcp.NewToolResultError(fmt.Sprintf("failed to %s parallel group: %s", op, err))
}

// hasHardValidationError reports whether any finding gates validity; advisory
// findings (Severity "warning") never do.
func hasHardValidationError(errs []workflow.ValidationError) bool {
	for _, e := range errs {
		if e.Severity != "warning" {
			return true
		}
	}
	return false
}
