package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/arbitron/arbitron/domain/schedule"
)

// RegisterScheduleTools registers the recurring-schedule management tools
// (org-readiness Phase 3). They mirror the REST surface and share the same
// schedule.Service (authz + validation), so REST and MCP never drift.
//
// The five tools are registered ONLY when a ScheduleService is wired. With
// registration conditional, the handlers can assume a non-nil service — there is
// no per-handler "unavailable" branch to forget, and an unwired deployment
// simply does not advertise the tools.
func RegisterScheduleTools(srv *Server) {
	if srv.deps == nil || srv.deps.ScheduleService == nil {
		return
	}
	srv.AddTool(
		gomcp.NewTool("schedule_create",
			gomcp.WithDescription("Create a recurring schedule that starts a workflow on a cron expression. Attributed to you; you may only schedule a workflow you can trigger."),
			gomcp.WithString("workflowName", gomcp.Required(), gomcp.Description("Name of the workflow to schedule")),
			gomcp.WithString("cronExpr", gomcp.Required(), gomcp.Description("Standard 5-field cron expression")),
			gomcp.WithString("timezone", gomcp.Description("IANA timezone (default UTC)")),
			gomcp.WithString("department", gomcp.Description("Optional: stamp runs under one of your departments")),
			gomcp.WithString("variables", gomcp.Description("JSON-encoded variables map passed at fire time")),
			gomcp.WithBoolean("enabled", gomcp.Description("Whether the schedule is active (default true)")),
		),
		scheduleCreateHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("schedule_list",
			gomcp.WithDescription("List your schedules (admins see all)"),
		),
		scheduleListHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("schedule_get",
			gomcp.WithDescription("Get a schedule by ID"),
			gomcp.WithString("id", gomcp.Required(), gomcp.Description("Schedule UUID")),
		),
		scheduleGetHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("schedule_update",
			gomcp.WithDescription("Update a schedule's mutable fields"),
			gomcp.WithString("id", gomcp.Required(), gomcp.Description("Schedule UUID")),
			gomcp.WithString("workflowName", gomcp.Description("New workflow name (re-checked against your trigger permission)")),
			gomcp.WithString("cronExpr", gomcp.Description("New cron expression")),
			gomcp.WithString("timezone", gomcp.Description("New IANA timezone")),
			gomcp.WithString("department", gomcp.Description("New department override")),
			gomcp.WithString("variables", gomcp.Description("JSON-encoded variables map")),
			gomcp.WithBoolean("enabled", gomcp.Description("Enable/disable the schedule")),
		),
		scheduleUpdateHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("schedule_delete",
			gomcp.WithDescription("Delete a schedule by ID"),
			gomcp.WithString("id", gomcp.Required(), gomcp.Description("Schedule UUID")),
		),
		scheduleDeleteHandler(srv),
	)
}

// scheduleResultError maps schedule sentinels to MCP error results (mirrors
// instanceTriggerHandler's error mapping).
func scheduleResultError(err error) *gomcp.CallToolResult {
	switch {
	case errors.Is(err, schedule.ErrForbidden), errors.Is(err, schedule.ErrNotAuthorizedWorkflow):
		return gomcp.NewToolResultError(fmt.Sprintf("forbidden: %s", err))
	case errors.Is(err, schedule.ErrNotFound):
		return gomcp.NewToolResultError(fmt.Sprintf("not found: %s", err))
	case errors.Is(err, schedule.ErrExists):
		return gomcp.NewToolResultError(fmt.Sprintf("conflict: %s", err))
	case errors.Is(err, schedule.ErrInvalidCron),
		errors.Is(err, schedule.ErrInvalidTimezone),
		errors.Is(err, schedule.ErrEmptyWorkflow),
		errors.Is(err, schedule.ErrDepartmentNotMember),
		errors.Is(err, schedule.ErrQuotaExceeded),
		errors.Is(err, schedule.ErrIntervalTooFrequent):
		return gomcp.NewToolResultError(fmt.Sprintf("invalid request: %s", err))
	default:
		return gomcp.NewToolResultError(fmt.Sprintf("schedule operation failed: %s", err))
	}
}

func parseVarsArg(args map[string]any) (map[string]any, error) {
	varsStr, _ := args["variables"].(string)
	if varsStr == "" {
		return nil, nil
	}
	var vars map[string]any
	if err := json.Unmarshal([]byte(varsStr), &vars); err != nil {
		return nil, fmt.Errorf("invalid variables JSON: %w", err)
	}
	return vars, nil
}

func scheduleCreateHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		args := request.GetArguments()
		wfName, _ := args["workflowName"].(string)
		if wfName == "" {
			return gomcp.NewToolResultError("workflowName is required"), nil
		}
		cronExpr, _ := args["cronExpr"].(string)
		if cronExpr == "" {
			return gomcp.NewToolResultError("cronExpr is required"), nil
		}
		vars, err := parseVarsArg(args)
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		enabled := true
		if v, ok := args["enabled"].(bool); ok {
			enabled = v
		}
		timezone, _ := args["timezone"].(string)
		department, _ := args["department"].(string)

		s, err := srv.deps.ScheduleService.Create(ctx, schedule.CreateInput{
			WorkflowName: wfName,
			CronExpr:     cronExpr,
			Timezone:     timezone,
			Department:   department,
			Variables:    vars,
			Enabled:      enabled,
		})
		if err != nil {
			return scheduleResultError(err), nil
		}
		return MarshalResult(s.WireMap())
	}
}

func scheduleListHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		schedules, err := srv.deps.ScheduleService.List(ctx)
		if err != nil {
			return scheduleResultError(err), nil
		}
		out := make([]map[string]any, 0, len(schedules))
		for _, s := range schedules {
			out = append(out, s.WireMap())
		}
		return MarshalResult(map[string]any{"schedules": out})
	}
}

func scheduleGetHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		id, err := ParseUUID(request, "id")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		s, err := srv.deps.ScheduleService.Get(ctx, id)
		if err != nil {
			return scheduleResultError(err), nil
		}
		return MarshalResult(s.WireMap())
	}
}

func scheduleUpdateHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		id, err := ParseUUID(request, "id")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		args := request.GetArguments()
		var in schedule.UpdateInput
		if v, ok := args["workflowName"].(string); ok {
			in.WorkflowName = &v
		}
		if v, ok := args["cronExpr"].(string); ok {
			in.CronExpr = &v
		}
		if v, ok := args["timezone"].(string); ok {
			in.Timezone = &v
		}
		if v, ok := args["department"].(string); ok {
			in.Department = &v
		}
		if v, ok := args["enabled"].(bool); ok {
			in.Enabled = &v
		}
		if _, ok := args["variables"]; ok {
			vars, err := parseVarsArg(args)
			if err != nil {
				return gomcp.NewToolResultError(err.Error()), nil
			}
			in.Variables = vars
		}
		s, err := srv.deps.ScheduleService.Update(ctx, id, in)
		if err != nil {
			return scheduleResultError(err), nil
		}
		return MarshalResult(s.WireMap())
	}
}

func scheduleDeleteHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		id, err := ParseUUID(request, "id")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		if err := srv.deps.ScheduleService.Delete(ctx, id); err != nil {
			return scheduleResultError(err), nil
		}
		return MarshalResult(map[string]string{"status": "deleted", "id": id.String()})
	}
}
