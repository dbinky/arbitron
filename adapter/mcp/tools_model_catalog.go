package mcp

import (
	"context"
	"errors"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/modelcatalog"
)

// ModelCatalogAdmin is the admin catalog surface behind the model_catalog_*
// tools. Authorization lives in the implementation (modelcatalog.AdminService).
type ModelCatalogAdmin interface {
	List(ctx context.Context) (modelcatalog.PublicCatalogView, error)
	RefreshNow(ctx context.Context) (modelcatalog.PublicRefreshReport, error)
}

// RegisterModelCatalogTools registers the admin-only catalog list and manual refresh.
func RegisterModelCatalogTools(srv *Server) {
	srv.AddTool(gomcp.NewTool("model_catalog_list",
		gomcp.WithDescription("List stored catalog models with per-path usability, unmatched follow targets and recent refresh reports (admin only). Tiers reflect each entry's stored policy_revision and may lag policy edits until a successful refresh of that provider; compare with model_identity_policy_get."),
	), modelCatalogHandler(srv, false))
	srv.AddTool(gomcp.NewTool("model_catalog_refresh",
		gomcp.WithDescription("Run a catalog refresh now and return its report; reports busy if a refresh is already in progress (admin only)"),
	), modelCatalogHandler(srv, true))
}

// modelCatalogError returns static text only: a fetch or store error can carry
// the configured catalog URL, query token included.
func modelCatalogError(err error) *gomcp.CallToolResult {
	switch {
	case errors.Is(err, identity.ErrUnauthenticated):
		return gomcp.NewToolResultError("authentication required")
	case errors.Is(err, identity.ErrForbidden):
		return gomcp.NewToolResultError("admin only")
	case errors.Is(err, modelcatalog.ErrRefreshBusy):
		return gomcp.NewToolResultError("model catalog refresh already in progress; retry later")
	case errors.Is(err, modelcatalog.ErrRefreshClosed):
		return gomcp.NewToolResultError("model catalog refresh is shutting down")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return gomcp.NewToolResultError("model catalog request cancelled")
	default:
		return gomcp.NewToolResultError("model catalog unavailable; see server logs")
	}
}

func modelCatalogHandler(srv *Server, refresh bool) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.ModelCatalog == nil {
			return gomcp.NewToolResultError("model catalog unavailable"), nil
		}
		if len(request.GetArguments()) > 0 {
			return gomcp.NewToolResultError("model catalog tools take no arguments"), nil
		}
		if refresh {
			report, err := srv.deps.ModelCatalog.RefreshNow(ctx)
			if err != nil {
				return modelCatalogError(err), nil
			}
			return MarshalResult(report)
		}
		view, err := srv.deps.ModelCatalog.List(ctx)
		if err != nil {
			return modelCatalogError(err), nil
		}
		return MarshalResult(view)
	}
}
