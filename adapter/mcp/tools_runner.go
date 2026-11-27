package mcp

import (
	"context"
	"errors"
	"fmt"
	"math"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/runner"
)

// RunnerService is the pool service surface (*runner.Service satisfies it).
type RunnerService interface {
	List(ctx context.Context) ([]runner.RunnerView, error)
	Get(ctx context.Context, id runner.RunnerID) (runner.RunnerDetail, error)
	Events(ctx context.Context, limit int) ([]runner.PoolEvent, error)
	Update(ctx context.Context, id runner.RunnerID, p runner.RunnerConfigPatch) (runner.Runner, error)
	Cordon(ctx context.Context, id runner.RunnerID) error
	Uncordon(ctx context.Context, id runner.RunnerID) error
	Drain(ctx context.Context, id runner.RunnerID) (runner.DrainProgress, error)
	ProbeSource(ctx context.Context, id runner.SourceID) (runner.SourceHealth, error)
}

// RegisterRunnerTools registers the runner-pool tools. Authz, visibility and
// audit live in runner.Service (shared with REST), so the surfaces never drift.
func RegisterRunnerTools(srv *Server) {
	srv.AddTool(gomcp.NewTool("runner_list",
		gomcp.WithDescription("List runners in the pool with their credential sources, slot use and health."),
	), runnerListHandler(srv))
	srv.AddTool(gomcp.NewTool("runner_get",
		gomcp.WithDescription("Get one runner with its active assignments (runs you cannot view are hidden)."),
		gomcp.WithString("id", gomcp.Required(), gomcp.Description("Runner id, e.g. builtin")),
	), runnerGetHandler(srv))
	srv.AddTool(gomcp.NewTool("runner_events",
		gomcp.WithDescription("Recent pool events, newest first (events of runs you cannot view are omitted)."),
		gomcp.WithNumber("limit", gomcp.Description("Max events (default 100, max 500)")),
	), runnerEventsHandler(srv))
	srv.AddTool(gomcp.NewTool("runner_update",
		gomcp.WithDescription("Admin: change a runner's step limit (1-256) and/or labels."),
		gomcp.WithString("id", gomcp.Required(), gomcp.Description("Runner id")),
		gomcp.WithNumber("stepLimit", gomcp.Description("Concurrent step limit, 1-256")),
		gomcp.WithObject("labels", gomcp.Description("Replacement label map (string values)")),
	), runnerUpdateHandler(srv))
	srv.AddTool(gomcp.NewTool("runner_cordon",
		gomcp.WithDescription("Admin: stop placing new steps on a runner."),
		gomcp.WithString("id", gomcp.Required(), gomcp.Description("Runner id")),
	), runnerCordonHandler(srv, true))
	srv.AddTool(gomcp.NewTool("runner_uncordon",
		gomcp.WithDescription("Admin: allow placements on a runner again."),
		gomcp.WithString("id", gomcp.Required(), gomcp.Description("Runner id")),
	), runnerCordonHandler(srv, false))
	srv.AddTool(gomcp.NewTool("runner_drain",
		gomcp.WithDescription("Admin: drain a runner — stop new placements, let active steps finish, move its homed workspaces to eligible runners. Returns progress; blocked items name the missing capacity. Safe to call again to read progress."),
		gomcp.WithString("id", gomcp.Required(), gomcp.Description("Runner id")),
	), runnerDrainHandler(srv))
	srv.AddTool(gomcp.NewTool("runner_source_probe",
		gomcp.WithDescription("Admin: probe a credential source's auth/capacity now (read-only, no tokens)."),
		gomcp.WithString("sourceId", gomcp.Required(), gomcp.Description("Source id, e.g. builtin/anthropic_subscription/-")),
	), runnerProbeHandler(srv))
}

func runnerErrorResult(err error) *gomcp.CallToolResult {
	switch {
	case errors.Is(err, identity.ErrUnauthenticated):
		return gomcp.NewToolResultError("authentication required")
	case errors.Is(err, identity.ErrForbidden):
		return gomcp.NewToolResultError("admin only")
	case errors.Is(err, runner.ErrRunnerNotFound):
		return gomcp.NewToolResultError("runner not found")
	case errors.Is(err, runner.ErrSourceNotFound):
		return gomcp.NewToolResultError("source not found")
	case errors.Is(err, runner.ErrDrainUnavailable):
		return gomcp.NewToolResultError("drain is not configured")
	case errors.Is(err, runner.ErrInvalidStepLimit):
		return gomcp.NewToolResultError("stepLimit must be between 1 and 256")
	default:
		return gomcp.NewToolResultError(fmt.Sprintf("runner operation failed: %s", err))
	}
}

var errRunnerUnavailable = gomcp.NewToolResultError("runner service not available")

func runnerArg(request gomcp.CallToolRequest, name string) (string, *gomcp.CallToolResult) {
	v, _ := request.GetArguments()[name].(string)
	if v == "" {
		return "", gomcp.NewToolResultError(name + " is required")
	}
	return v, nil
}

func runnerListHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, _ gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.Runners == nil {
			return errRunnerUnavailable, nil
		}
		views, err := srv.deps.Runners.List(ctx)
		if err != nil {
			return runnerErrorResult(err), nil
		}
		out := make([]runnerViewJSON, 0, len(views))
		for _, v := range views {
			out = append(out, toRunnerViewJSON(v))
		}
		return MarshalResult(map[string]any{"runners": out})
	}
}

func runnerDrainHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.Runners == nil {
			return errRunnerUnavailable, nil
		}
		id, bad := runnerArg(request, "id")
		if bad != nil {
			return bad, nil
		}
		p, err := srv.deps.Runners.Drain(ctx, runner.RunnerID(id))
		if err != nil {
			return runnerErrorResult(err), nil
		}
		return MarshalResult(toDrainProgressJSON(p))
	}
}

func runnerGetHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.Runners == nil {
			return errRunnerUnavailable, nil
		}
		id, bad := runnerArg(request, "id")
		if bad != nil {
			return bad, nil
		}
		d, err := srv.deps.Runners.Get(ctx, runner.RunnerID(id))
		if err != nil {
			return runnerErrorResult(err), nil
		}
		return MarshalResult(toRunnerDetailJSON(d))
	}
}

func runnerEventsHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.Runners == nil {
			return errRunnerUnavailable, nil
		}
		limit := 0
		if v, ok := request.GetArguments()["limit"].(float64); ok {
			if v < 0 || v != math.Trunc(v) {
				return gomcp.NewToolResultError("limit must be a non-negative integer"), nil
			}
			limit = int(v)
		}
		evs, err := srv.deps.Runners.Events(ctx, limit)
		if err != nil {
			return runnerErrorResult(err), nil
		}
		views := make([]runner.PoolEventView, 0, len(evs))
		for _, ev := range evs {
			views = append(views, runner.NewPoolEventView(ev))
		}
		return MarshalResult(map[string]any{"events": views})
	}
}

func runnerUpdateHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.Runners == nil {
			return errRunnerUnavailable, nil
		}
		id, bad := runnerArg(request, "id")
		if bad != nil {
			return bad, nil
		}
		args := request.GetArguments()
		var p runner.RunnerConfigPatch
		if v, ok := args["stepLimit"].(float64); ok {
			if v != math.Trunc(v) {
				return gomcp.NewToolResultError("stepLimit must be an integer"), nil
			}
			n := int(v)
			p.StepLimit = &n
		}
		if raw, ok := args["labels"].(map[string]any); ok {
			p.Labels = make(map[string]string, len(raw))
			for k, v := range raw {
				s, ok := v.(string)
				if !ok {
					return gomcp.NewToolResultError("labels values must be strings"), nil
				}
				p.Labels[k] = s
			}
		}
		r, err := srv.deps.Runners.Update(ctx, runner.RunnerID(id), p)
		if err != nil {
			return runnerErrorResult(err), nil
		}
		return MarshalResult(toRunnerViewJSON(runner.RunnerView{Runner: r}))
	}
}

func runnerCordonHandler(srv *Server, on bool) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.Runners == nil {
			return errRunnerUnavailable, nil
		}
		id, bad := runnerArg(request, "id")
		if bad != nil {
			return bad, nil
		}
		var err error
		if on {
			err = srv.deps.Runners.Cordon(ctx, runner.RunnerID(id))
		} else {
			err = srv.deps.Runners.Uncordon(ctx, runner.RunnerID(id))
		}
		if err != nil {
			return runnerErrorResult(err), nil
		}
		return MarshalResult(map[string]any{"id": id, "cordoned": on})
	}
}

func runnerProbeHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.Runners == nil {
			return errRunnerUnavailable, nil
		}
		id, bad := runnerArg(request, "sourceId")
		if bad != nil {
			return bad, nil
		}
		h, err := srv.deps.Runners.ProbeSource(ctx, runner.SourceID(id))
		if err != nil {
			return runnerErrorResult(err), nil
		}
		return MarshalResult(toRunnerSourceJSON(runner.Source{Health: h}).Health)
	}
}
