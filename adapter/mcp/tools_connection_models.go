package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/arbitron/arbitron/domain/provider"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const connectionModelsDescription = "List the models available through one connection (admin only), each with its vendor family and KNOWN capabilities: " +
	"task kinds (chat/agent), compatible agent backends (claude-code, opencode, codex, pi), context/output token limits and supported efforts. " +
	"Every capability field is either known (with an evidence reference) or explicitly \"unknown\"; unknown is not a guarantee, and availability in the listing does NOT imply task or backend compatibility. " +
	"Choose the model and the backend explicitly: nothing is recommended or auto-selected. LunaRoute agent profiles must name their backend and use pinned selection. " +
	"family is the model's actual vendor, aliases included: keep doer and judge on different vendors; an \"unresolved\" family is refused. Never returns credentials."

func registerConnectionModelsTool(srv *Server) {
	srv.AddTool(
		gomcp.NewTool("connection_models",
			gomcp.WithDescription(connectionModelsDescription),
			gomcp.WithString("name", gomcp.Required(), gomcp.Description("Connection name")),
		),
		connectionModelsHandler(srv),
	)
}

func connectionModelsHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.ModelDiscovery == nil {
			return gomcp.NewToolResultError("model discovery not available"), nil
		}
		name, _ := request.GetArguments()["name"].(string)
		if name == "" {
			return gomcp.NewToolResultError("name is required"), nil
		}
		view, err := srv.deps.ModelDiscovery.Discover(ctx, name)
		if err != nil {
			// Arbitrary store/probe errors can contain foreign credentials that a
			// LunaRoute-pattern scrubber cannot recognize. Log only the name;
			// the tool mapping below supplies safe, actionable diagnostics.
			connLogger(srv).Warn("connection_models failed", slog.String("connection", name))
			return discoveryErrorResult(name, err), nil
		}
		return MarshalResult(view)
	}
}

// discoveryErrorResult maps discovery failures to stable, actionable text.
// Classified gateway failures are already redacted by the LunaRoute classifier.
func discoveryErrorResult(name string, err error) *gomcp.CallToolResult {
	var failure *provider.LunaRouteFailure
	switch {
	case errors.Is(err, provider.ErrForbidden):
		return gomcp.NewToolResultError("forbidden: admin only")
	case errors.Is(err, provider.ErrUnauthenticated):
		return gomcp.NewToolResultError("authentication required")
	case errors.Is(err, provider.ErrConnectionNotFound):
		return gomcp.NewToolResultError(fmt.Sprintf("connection %q not found", name))
	case errors.As(err, &failure):
		return gomcp.NewToolResultError(fmt.Sprintf("connection %q: %s", name, failure.Error()))
	case errors.Is(err, provider.ErrLunaRouteStoredCredential):
		return gomcp.NewToolResultError(fmt.Sprintf("connection %q: %s", name, provider.ErrLunaRouteStoredCredential.Error()))
	case errors.Is(err, provider.ErrLunaRouteEndpoint):
		return gomcp.NewToolResultError(fmt.Sprintf("connection %q: %s", name, provider.ErrLunaRouteEndpoint.Error()))
	case errors.Is(err, provider.ErrSelectionPortsMissing):
		return gomcp.NewToolResultError("model discovery is not configured; contact the administrator")
	default:
		return gomcp.NewToolResultError(fmt.Sprintf("model listing for connection %q failed and the cause is unknown (see server logs)", name))
	}
}
