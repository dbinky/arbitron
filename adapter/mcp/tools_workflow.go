package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/arbitron/arbitron/domain/workflow"
)

// RegisterWorkflowTools registers all workflow management tools with the server.
func RegisterWorkflowTools(srv *Server) {
	srv.AddTool(
		gomcp.NewTool("workflow_list",
			gomcp.WithDescription("List all available workflow definitions"),
		),
		workflowListHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("workflow_get",
			gomcp.WithDescription("Load a workflow definition by name"),
			gomcp.WithString("name", gomcp.Required(), gomcp.Description("Workflow name")),
		),
		workflowGetHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("workflow_create",
			gomcp.WithDescription("Create a new workflow definition from a JSON manifest"),
			gomcp.WithString("manifest", gomcp.Required(), gomcp.Description("JSON-encoded workflow manifest")),
		),
		workflowCreateHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("workflow_update",
			gomcp.WithDescription("Update an existing workflow definition"),
			gomcp.WithString("name", gomcp.Required(), gomcp.Description("Workflow name")),
			gomcp.WithString("manifest", gomcp.Required(), gomcp.Description("JSON-encoded workflow manifest")),
		),
		workflowUpdateHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("workflow_delete",
			gomcp.WithDescription("Delete a workflow definition by name"),
			gomcp.WithString("name", gomcp.Required(), gomcp.Description("Workflow name")),
		),
		workflowDeleteHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("workflow_validate",
			gomcp.WithDescription("Validate a workflow definition and return any errors"),
			gomcp.WithString("name", gomcp.Required(), gomcp.Description("Workflow name")),
		),
		workflowValidateHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("workflow_list_files",
			gomcp.WithDescription("List files associated with a workflow"),
			gomcp.WithString("name", gomcp.Required(), gomcp.Description("Workflow name")),
		),
		workflowListFilesHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("workflow_read_file",
			gomcp.WithDescription("Read a file associated with a workflow"),
			gomcp.WithString("name", gomcp.Required(), gomcp.Description("Workflow name")),
			gomcp.WithString("path", gomcp.Required(), gomcp.Description("Relative file path")),
		),
		workflowReadFileHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("workflow_write_file",
			gomcp.WithDescription("Write a file to a workflow directory"),
			gomcp.WithString("name", gomcp.Required(), gomcp.Description("Workflow name")),
			gomcp.WithString("path", gomcp.Required(), gomcp.Description("Relative file path")),
			gomcp.WithString("content", gomcp.Required(), gomcp.Description("File content")),
		),
		workflowWriteFileHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("workflow_delete_file",
			gomcp.WithDescription("Delete a file from a workflow directory"),
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
			return gomcp.NewToolResultError(fmt.Sprintf("failed to list workflows: %s", err)), nil
		}
		if summaries == nil {
			summaries = []workflow.WorkflowSummary{}
		}
		return MarshalResult(summaries)
	}
}

func workflowGetHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		args := request.GetArguments()
		name, _ := args["name"].(string)
		if name == "" {
			return gomcp.NewToolResultError("name is required"), nil
		}

		if !safeWorkflowName(name) {
			return gomcp.NewToolResultError(fmt.Sprintf("invalid workflow name: %s", name)), nil
		}

		wf, err := srv.deps.Workflows.Load(ctx, name)
		if err != nil {
			if errors.Is(err, workflow.ErrNotFound) {
				return gomcp.NewToolResultError(fmt.Sprintf("workflow not found: %s", name)), nil
			}
			return gomcp.NewToolResultError(fmt.Sprintf("failed to load workflow: %s", err)), nil
		}
		if wf == nil {
			return gomcp.NewToolResultError("workflow not found"), nil
		}
		return MarshalResult(wf)
	}
}

func workflowCreateHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		args := request.GetArguments()
		manifest, _ := args["manifest"].(string)
		if manifest == "" {
			return gomcp.NewToolResultError("manifest is required"), nil
		}

		var wf workflow.Workflow
		if err := json.Unmarshal([]byte(manifest), &wf); err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("invalid manifest JSON: %s", err)), nil
		}

		if !safeWorkflowName(wf.Name) {
			return gomcp.NewToolResultError(fmt.Sprintf("invalid workflow name: %s", wf.Name)), nil
		}

		if err := srv.deps.Workflows.Create(ctx, wf.Name, &wf); err != nil {
			if errors.Is(err, workflow.ErrAlreadyExists) {
				return gomcp.NewToolResultError(fmt.Sprintf("workflow already exists: %s", wf.Name)), nil
			}
			return gomcp.NewToolResultError(fmt.Sprintf("failed to create workflow: %s", err)), nil
		}
		return MarshalResult(map[string]string{"status": "created", "name": wf.Name})
	}
}

func workflowUpdateHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		args := request.GetArguments()
		name, _ := args["name"].(string)
		if name == "" {
			return gomcp.NewToolResultError("name is required"), nil
		}

		if !safeWorkflowName(name) {
			return gomcp.NewToolResultError(fmt.Sprintf("invalid workflow name: %s", name)), nil
		}

		manifest, _ := args["manifest"].(string)
		if manifest == "" {
			return gomcp.NewToolResultError("manifest is required"), nil
		}

		var wf workflow.Workflow
		if err := json.Unmarshal([]byte(manifest), &wf); err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("invalid manifest JSON: %s", err)), nil
		}
		// The tool's "name" arg is authoritative; overwrite whatever the manifest
		// contained so workflow.json stays consistent with its directory name.
		wf.Name = name

		if err := srv.deps.Workflows.Update(ctx, name, &wf); err != nil {
			if errors.Is(err, workflow.ErrNotFound) {
				return gomcp.NewToolResultError(fmt.Sprintf("workflow not found: %s", name)), nil
			}
			return gomcp.NewToolResultError(fmt.Sprintf("failed to update workflow: %s", err)), nil
		}
		return MarshalResult(map[string]string{"status": "updated", "name": name})
	}
}

func workflowDeleteHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		args := request.GetArguments()
		name, _ := args["name"].(string)
		if name == "" {
			return gomcp.NewToolResultError("name is required"), nil
		}

		if !safeWorkflowName(name) {
			return gomcp.NewToolResultError(fmt.Sprintf("invalid workflow name: %s", name)), nil
		}

		if err := srv.deps.Workflows.Delete(ctx, name); err != nil {
			if errors.Is(err, workflow.ErrNotFound) {
				return gomcp.NewToolResultError(fmt.Sprintf("workflow not found: %s", name)), nil
			}
			return gomcp.NewToolResultError(fmt.Sprintf("failed to delete workflow: %s", err)), nil
		}
		return MarshalResult(map[string]string{"status": "deleted", "name": name})
	}
}

func workflowValidateHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		args := request.GetArguments()
		name, _ := args["name"].(string)
		if name == "" {
			return gomcp.NewToolResultError("name is required"), nil
		}

		if !safeWorkflowName(name) {
			return gomcp.NewToolResultError(fmt.Sprintf("invalid workflow name: %s", name)), nil
		}

		wf, err := srv.deps.Workflows.Load(ctx, name)
		if err != nil {
			if errors.Is(err, workflow.ErrNotFound) {
				return gomcp.NewToolResultError(fmt.Sprintf("workflow not found: %s", name)), nil
			}
			return gomcp.NewToolResultError(fmt.Sprintf("failed to load workflow: %s", err)), nil
		}
		if wf == nil {
			return gomcp.NewToolResultError("workflow not found"), nil
		}

		validationErrors := workflow.Validate(wf, srv.deps.FileReader, srv.deps.ProfileChecker)
		if validationErrors == nil {
			validationErrors = []workflow.ValidationError{}
		}
		result := map[string]any{
			"valid":  len(validationErrors) == 0,
			"errors": validationErrors,
		}
		return MarshalResult(result)
	}
}

func workflowListFilesHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		args := request.GetArguments()
		name, _ := args["name"].(string)
		if name == "" {
			return gomcp.NewToolResultError("name is required"), nil
		}

		if !safeWorkflowName(name) {
			return gomcp.NewToolResultError(fmt.Sprintf("invalid workflow name: %s", name)), nil
		}

		files, err := srv.deps.Workflows.ListFiles(ctx, name)
		if err != nil {
			if errors.Is(err, workflow.ErrNotFound) {
				return gomcp.NewToolResultError(fmt.Sprintf("workflow not found: %s", name)), nil
			}
			return gomcp.NewToolResultError(fmt.Sprintf("failed to list files: %s", err)), nil
		}
		if files == nil {
			files = []string{}
		}
		return MarshalResult(files)
	}
}

func workflowReadFileHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		args := request.GetArguments()
		name, _ := args["name"].(string)
		if name == "" {
			return gomcp.NewToolResultError("name is required"), nil
		}

		if !safeWorkflowName(name) {
			return gomcp.NewToolResultError(fmt.Sprintf("invalid workflow name: %s", name)), nil
		}

		path, _ := args["path"].(string)
		if path == "" {
			return gomcp.NewToolResultError("path is required"), nil
		}

		content, err := srv.deps.FileReader.ReadFile(name, path)
		if err != nil {
			return gomcp.NewToolResultError(fmt.Sprintf("failed to read file: %s", err)), nil
		}
		return gomcp.NewToolResultText(string(content)), nil
	}
}

// maxWriteFileContentBytes is the maximum content size accepted by the MCP
// workflow_write_file tool, matching the HTTP handler's 10 MiB limit.
const maxWriteFileContentBytes = 10 << 20 // 10 MiB

func workflowWriteFileHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		args := request.GetArguments()
		name, _ := args["name"].(string)
		if name == "" {
			return gomcp.NewToolResultError("name is required"), nil
		}

		if !safeWorkflowName(name) {
			return gomcp.NewToolResultError(fmt.Sprintf("invalid workflow name: %s", name)), nil
		}

		path, _ := args["path"].(string)
		if path == "" {
			return gomcp.NewToolResultError("path is required"), nil
		}
		rawContent, hasContent := args["content"]
		if !hasContent {
			return gomcp.NewToolResultError("content is required"), nil
		}
		content, _ := rawContent.(string)

		if len(content) > maxWriteFileContentBytes {
			return gomcp.NewToolResultError(fmt.Sprintf("content too large: %d bytes exceeds %d byte limit", len(content), maxWriteFileContentBytes)), nil
		}

		if err := srv.deps.Workflows.WriteFile(ctx, name, path, []byte(content)); err != nil {
			if errors.Is(err, workflow.ErrNotFound) {
				return gomcp.NewToolResultError(fmt.Sprintf("workflow not found: %s", name)), nil
			}
			if errors.Is(err, workflow.ErrInvalidFilePath) {
				return gomcp.NewToolResultError(fmt.Sprintf("invalid file path: %s", path)), nil
			}
			return gomcp.NewToolResultError(fmt.Sprintf("failed to write file: %s", err)), nil
		}
		return MarshalResult(map[string]string{"status": "written", "name": name, "path": path})
	}
}

func workflowDeleteFileHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		args := request.GetArguments()
		name, _ := args["name"].(string)
		if name == "" {
			return gomcp.NewToolResultError("name is required"), nil
		}

		if !safeWorkflowName(name) {
			return gomcp.NewToolResultError(fmt.Sprintf("invalid workflow name: %s", name)), nil
		}

		path, _ := args["path"].(string)
		if path == "" {
			return gomcp.NewToolResultError("path is required"), nil
		}

		if err := srv.deps.Workflows.DeleteFile(ctx, name, path); err != nil {
			if errors.Is(err, workflow.ErrNotFound) || errors.Is(err, workflow.ErrFileNotFound) {
				return gomcp.NewToolResultError(fmt.Sprintf("not found: %s", err)), nil
			}
			if errors.Is(err, workflow.ErrInvalidFilePath) {
				return gomcp.NewToolResultError(fmt.Sprintf("invalid file path: %s", path)), nil
			}
			return gomcp.NewToolResultError(fmt.Sprintf("failed to delete file: %s", err)), nil
		}
		return MarshalResult(map[string]string{"status": "deleted", "name": name, "path": path})
	}
}
