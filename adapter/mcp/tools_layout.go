package mcp

import (
	"context"
	"errors"
	"fmt"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
)

const layoutServiceNotConfigured = "layout service not configured"
const mutationPendingMessage = "workflow destination is awaiting durable mutation recovery; retry shortly"

// mutationPendingToolError never exposes filesystem or registry recovery details.
func mutationPendingToolError(err error) *gomcp.CallToolResult {
	if errors.Is(err, workflow.ErrMutationPending) {
		return gomcp.NewToolResultError(mutationPendingMessage)
	}
	return nil
}

// RegisterLayoutTools registers the arrangement retry tools. Both route through
// Deps.Layouts — the same authorised seam as the HTTP retry endpoints.
func RegisterLayoutTools(srv *Server) {
	srv.AddTool(
		gomcp.NewTool("workflow_layout_retry",
			gomcp.WithDescription("Retry automatic arrangement and readability review for a workflow's CURRENT saved revision. "+
				"Allowed when the arrangement failed or needs attention (or was never recorded); returns immediately with the new job generation. "+
				"Requires the same permission as editing the workflow."),
			gomcp.WithString("name", gomcp.Required(), gomcp.Description("Workflow name")),
			gomcp.WithString("expectedRevision", gomcp.Required(), gomcp.Description(
				"The saved revision being retried (workflow_get arrangement.savedRevision). A stale value is rejected so a concurrent save is never retried by mistake.")),
		),
		workflowLayoutRetryHandler(srv),
	)
	srv.AddTool(
		gomcp.NewTool("instance_layout_retry",
			gomcp.WithDescription("Retry automatic arrangement for the HISTORICAL saved revision a workflow instance ran from. "+
				"Never touches the current workflow or the instance's execution. Requires owner, department-lead or admin access to the instance."),
			gomcp.WithString("id", gomcp.Required(), gomcp.Description("Instance UUID")),
			gomcp.WithString("expectedRevision", gomcp.Required(), gomcp.Description(
				"The saved revision captured in the instance's workflow snapshot (workflowSnapshot.presentation.revision).")),
		),
		instanceLayoutRetryHandler(srv),
	)
}

func workflowLayoutRetryHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		args := request.GetArguments()
		name, _ := args["name"].(string)
		if name == "" {
			return gomcp.NewToolResultError("name is required"), nil
		}
		if !safeWorkflowName(name) {
			return gomcp.NewToolResultError(fmt.Sprintf("invalid workflow name: %s", name)), nil
		}
		expected, _ := args["expectedRevision"].(string)
		if expected == "" {
			return gomcp.NewToolResultError("expectedRevision is required"), nil
		}
		svc := layoutService(srv)
		if svc == nil {
			return gomcp.NewToolResultError(layoutServiceNotConfigured), nil
		}
		ack, err := svc.RetryWorkflow(ctx, name, expected)
		if err != nil {
			return layoutToolError("workflow", name, expected, err), nil
		}
		return MarshalResult(ack)
	}
}

func instanceLayoutRetryHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		id, err := ParseUUID(request, "id")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}
		expected, _ := request.GetArguments()["expectedRevision"].(string)
		if expected == "" {
			return gomcp.NewToolResultError("expectedRevision is required"), nil
		}
		svc := layoutService(srv)
		if svc == nil {
			return gomcp.NewToolResultError(layoutServiceNotConfigured), nil
		}
		ack, err := svc.RetryInstance(ctx, id, expected)
		if err != nil {
			return layoutToolError("instance", id.String(), expected, err), nil
		}
		return MarshalResult(ack)
	}
}

// layoutToolError maps the layout/authorization sentinels to tool errors.
// Not-found wording is identical for absent and inaccessible subjects.
func layoutToolError(kind, subject, expected string, err error) *gomcp.CallToolResult {
	switch {
	case errors.Is(err, layout.ErrRevisionConflict):
		return gomcp.NewToolResultError(fmt.Sprintf("layout revision conflict: expected %s is not current", expected))
	case errors.Is(err, layout.ErrRetryConflict):
		return gomcp.NewToolResultError("layout is not eligible for retry")
	case errors.Is(err, layout.ErrLegacyRevision):
		return gomcp.NewToolResultError("layout revision has no arrangement request (saved before automatic arrangement); save the workflow to arrange it")
	case errors.Is(err, workflow.ErrNotFound), errors.Is(err, instance.ErrNotFound):
		return gomcp.NewToolResultError(fmt.Sprintf("%s not found: %s", kind, subject))
	case errors.Is(err, workflow.ErrSharedReadOnly):
		return gomcp.NewToolResultError(fmt.Sprintf("workflow is shared and read-only: %s", subject))
	case errors.Is(err, identity.ErrForbidden), errors.Is(err, workflow.ErrUnauthorized):
		return gomcp.NewToolResultError(fmt.Sprintf("not authorized: %s", err))
	case errors.Is(err, workflow.ErrMutationPending):
		return mutationPendingToolError(err)
	case errors.Is(err, layout.ErrStoreUnavailable):
		return gomcp.NewToolResultError("layout status store unavailable; retry later")
	default:
		return gomcp.NewToolResultError(fmt.Sprintf("failed to retry layout: %s", err))
	}
}

// layoutService returns the injected layout query seam, or nil.
func layoutService(srv *Server) LayoutQueryService {
	if srv.deps == nil {
		return nil
	}
	return srv.deps.Layouts
}

// workflowWithArrangement renders the manifest exactly as before plus an
// "arrangement" summary for its saved revision.
func workflowWithArrangement(ctx context.Context, srv *Server, name string, wf *workflow.Workflow) (*gomcp.CallToolResult, error) {
	v := workflowArrangement(ctx, srv, name, wf)
	if v.SavedRevision != wf.SavedRevision() {
		// One authorised reload closes an ordinary save/read race.
		newer, err := workflowSvc(srv).Load(ctx, name)
		if result := mutationPendingToolError(err); result != nil {
			return result, nil
		}
		if err != nil || newer == nil {
			return gomcp.NewToolResultError("workflow is no longer accessible; reload"), nil
		}
		wf = newer
		v = workflowArrangement(ctx, srv, name, wf)
		if v.SavedRevision != wf.SavedRevision() {
			v = unavailableArrangement(wf, "workflow changed while reading arrangement; reload or retry")
		}
	}
	return MarshalResult(struct {
		*workflow.Workflow
		Arrangement map[string]any `json:"arrangement"`
	}{wf, arrangementSummary(v)})
}

func workflowArrangement(ctx context.Context, srv *Server, name string, wf *workflow.Workflow) layout.View {
	svc := layoutService(srv)
	if svc == nil {
		return unavailableArrangement(wf, layoutServiceNotConfigured)
	}
	v, err := svc.WorkflowLayout(ctx, name)
	if err != nil {
		switch {
		case errors.Is(err, workflow.ErrMutationPending):
			return unavailableArrangement(wf, mutationPendingMessage)
		case errors.Is(err, layout.ErrStoreUnavailable):
			return unavailableArrangement(wf, "layout status store unavailable; retry later")
		default:
			return unavailableArrangement(wf, "layout status unavailable; reload or retry")
		}
	}
	return v
}

// unavailableArrangement describes a view that could not be read, keyed by the
// manifest's own saved revision.
func unavailableArrangement(wf *workflow.Workflow, reason string) layout.View {
	v := layout.View{
		SavedRevision:     wf.SavedRevision(),
		DisplayStatus:     layout.DisplayUnavailable,
		Findings:          []layout.Finding{},
		Provenance:        layout.ProvenanceLegacy,
		Available:         false,
		AvailabilityError: reason,
	}
	if wf.Presentation != nil {
		v.Provenance = layout.ProvenanceRetainedPositions
		v.Unreviewed = true
		v.RetryEligible = true
	}
	return v
}

// arrangementSummary projects a view onto the MCP contract keys; geometry,
// contract, evidence and retained positions are deliberately omitted.
func arrangementSummary(v layout.View) map[string]any {
	findings := v.Findings
	if findings == nil {
		findings = []layout.Finding{}
	}
	return map[string]any{
		"savedRevision":     v.SavedRevision,
		"displayStatus":     v.DisplayStatus,
		"state":             v.State,
		"stage":             v.Stage,
		"attemptsUsed":      v.AttemptsUsed,
		"attemptLimit":      v.AttemptLimit,
		"findings":          findings,
		"failureStage":      v.FailureStage,
		"failureReason":     v.FailureReason,
		"retryEligible":     v.RetryEligible,
		"available":         v.Available,
		"availabilityError": v.AvailabilityError,
		"provenance":        v.Provenance,
	}
}
