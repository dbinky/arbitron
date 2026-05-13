package mcp

import (
	"context"
	"fmt"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// RegisterInteractiveTools registers all interactive session tools with the server.
func RegisterInteractiveTools(srv *Server) {
	srv.AddTool(
		gomcp.NewTool("interactive_send_message",
			gomcp.WithDescription("Send a user message to an active interactive session"),
			gomcp.WithString("instanceId", gomcp.Required(), gomcp.Description("Instance UUID")),
			gomcp.WithString("content", gomcp.Required(), gomcp.Description("User message content")),
		),
		interactiveSendMessageHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("interactive_end_session",
			gomcp.WithDescription("End an interactive session early (user-initiated)"),
			gomcp.WithString("instanceId", gomcp.Required(), gomcp.Description("Instance UUID")),
		),
		interactiveEndSessionHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("interactive_get_history",
			gomcp.WithDescription("Retrieve conversation history for reconnection"),
			gomcp.WithString("instanceId", gomcp.Required(), gomcp.Description("Instance UUID")),
		),
		interactiveGetHistoryHandler(srv),
	)
}

func interactiveSendMessageHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.ConversationHandler == nil {
			return gomcp.NewToolResultError("interactive sessions not available"), nil
		}

		id, err := ParseUUID(request, "instanceId")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}

		args := request.GetArguments()
		content, _ := args["content"].(string)
		if content == "" {
			return gomcp.NewToolResultError("content is required"), nil
		}

		if err := srv.deps.ConversationHandler.HandleMessage(ctx, id, content); err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("failed to send message: %s", err)), nil
		}
		return MarshalResult(map[string]string{"status": "sent", "instanceId": id.String()})
	}
}

func interactiveEndSessionHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.ConversationHandler == nil {
			return gomcp.NewToolResultError("interactive sessions not available"), nil
		}

		id, err := ParseUUID(request, "instanceId")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}

		output, err := srv.deps.ConversationHandler.EndSession(ctx, id)
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("failed to end session: %s", err)), nil
		}
		return MarshalResult(output)
	}
}

func interactiveGetHistoryHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		if srv.deps.ConversationHandler == nil {
			return gomcp.NewToolResultError("interactive sessions not available"), nil
		}

		id, err := ParseUUID(request, "instanceId")
		if err != nil {
			return gomcp.NewToolResultError(err.Error()), nil
		}

		inst, err := srv.deps.Instances.Get(ctx, id)
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("failed to get instance: %s", err)), nil
		}
		if inst == nil {
			return gomcp.NewToolResultError("instance not found"), nil
		}

		if inst.CurrentNodeID == nil || *inst.CurrentNodeID == "" {
			return gomcp.NewToolResultError("instance has no current node"), nil
		}

		exec, err := srv.deps.NodeExecs.GetByInstanceAndNode(ctx, id, *inst.CurrentNodeID)
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("failed to get node execution: %s", err)), nil
		}
		if exec == nil {
			return gomcp.NewToolResultError("node execution not found"), nil
		}

		return MarshalResult(exec.LLMConversation)
	}
}
