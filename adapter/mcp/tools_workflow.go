package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/workflow"
)

// callerOwner returns the authenticated caller's user ID from the tool ctx.
// The Phase 1 auth middleware wraps /mcp, so an identity is always present.
func callerOwner(ctx context.Context) uuid.UUID {
	return identity.MustFromContext(ctx).UserID
}

// workflowSvc returns the WorkflowService for the workflow_* tools, building a
// default one over the repository when Deps.WorkflowSvc is unset.
func workflowSvc(srv *Server) *workflow.WorkflowService {
	if srv.deps.WorkflowSvc != nil {
		return srv.deps.WorkflowSvc
	}
	return workflow.NewWorkflowService(srv.deps.Workflows, nil)
}

// ownerBoundChecker adapts a scope-aware engine.FileReader to the owner-blind
// workflow.FileChecker by binding the caller's owner.
type ownerBoundChecker struct {
	fr    interface {
		FileExists(owner uuid.UUID, workflowName, relativePath string) bool
	}
	owner uuid.UUID
}

func (c ownerBoundChecker) FileExists(workflowName, relativePath string) bool {
	return c.fr.FileExists(c.owner, workflowName, relativePath)
}

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

	srv.AddTool(
		gomcp.NewTool("workflow_publish",
			gomcp.WithDescription("Publish a private workflow to the shared workspace"),
			gomcp.WithString("name", gomcp.Required(), gomcp.Description("Private workflow name to publish")),
			gomcp.WithString("publishedName", gomcp.Required(), gomcp.Description("Name to publish under in the shared workspace")),
		),
		workflowPublishHandler(srv),
	)

	srv.AddTool(
		gomcp.NewTool("workflow_copy",
			gomcp.WithDescription("Copy a shared workflow into your private workspace"),
			gomcp.WithString("name", gomcp.Required(), gomcp.Description("Shared workflow name to copy")),
			gomcp.WithString("newName", gomcp.Required(), gomcp.Description("New private workflow name")),
		),
		workflowCopyHandler(srv),
	)
}

func workflowListHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		summaries, err := workflowSvc(srv).List(ctx)
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

		wf, err := workflowSvc(srv).Load(ctx, name)
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

		if err := workflowSvc(srv).Create(ctx, wf.Name, &wf); err != nil {
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

		if err := workflowSvc(srv).Update(ctx, name, &wf); err != nil {
			if errors.Is(err, workflow.ErrSharedReadOnly) {
				return gomcp.NewToolResultError(fmt.Sprintf("workflow is shared and read-only: %s", name)), nil
			}
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

		if err := workflowSvc(srv).Delete(ctx, name); err != nil {
			if errors.Is(err, workflow.ErrSharedReadOnly) {
				return gomcp.NewToolResultError(fmt.Sprintf("workflow is shared and read-only: %s", name)), nil
			}
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

		wf, err := workflowSvc(srv).Load(ctx, name)
		if err != nil {
			if errors.Is(err, workflow.ErrNotFound) {
				return gomcp.NewToolResultError(fmt.Sprintf("workflow not found: %s", name)), nil
			}
			return gomcp.NewToolResultError(fmt.Sprintf("failed to load workflow: %s", err)), nil
		}
		if wf == nil {
			return gomcp.NewToolResultError("workflow not found"), nil
		}

		checker := ownerBoundChecker{fr: srv.deps.FileReader, owner: callerOwner(ctx)}
		validationErrors := workflow.Validate(wf, checker, srv.deps.ProfileChecker, srv.deps.ConnChecker)
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

		files, err := workflowSvc(srv).ListFiles(ctx, name)
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

		content, err := srv.deps.FileReader.ReadFile(callerOwner(ctx), name, path)
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

		if err := workflowSvc(srv).WriteFile(ctx, name, path, []byte(content)); err != nil {
			if errors.Is(err, workflow.ErrSharedReadOnly) {
				return gomcp.NewToolResultError(fmt.Sprintf("workflow is shared and read-only: %s", name)), nil
			}
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

		if err := workflowSvc(srv).DeleteFile(ctx, name, path); err != nil {
			if errors.Is(err, workflow.ErrSharedReadOnly) {
				return gomcp.NewToolResultError(fmt.Sprintf("workflow is shared and read-only: %s", name)), nil
			}
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

func workflowPublishHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		args := request.GetArguments()
		name, _ := args["name"].(string)
		publishedName, _ := args["publishedName"].(string)
		if name == "" {
			return gomcp.NewToolResultError("name is required"), nil
		}
		if publishedName == "" {
			return gomcp.NewToolResultError("publishedName is required"), nil
		}
		if !safeWorkflowName(name) {
			return gomcp.NewToolResultError(fmt.Sprintf("invalid workflow name: %s", name)), nil
		}
		if !safeWorkflowName(publishedName) {
			return gomcp.NewToolResultError(fmt.Sprintf("invalid published name: %s", publishedName)), nil
		}

		if err := workflowSvc(srv).Publish(ctx, name, publishedName); err != nil {
			switch {
			case errors.Is(err, workflow.ErrUnauthorized):
				return gomcp.NewToolResultError(fmt.Sprintf("not authorized to publish %q: %s", publishedName, err)), nil
			case errors.Is(err, workflow.ErrNotFound):
				return gomcp.NewToolResultError(fmt.Sprintf("workflow not found: %s", name)), nil
			case errors.Is(err, workflow.ErrAlreadyExists):
				return gomcp.NewToolResultError(fmt.Sprintf("published name already exists: %s", publishedName)), nil
			default:
				return gomcp.NewToolResultError(fmt.Sprintf("failed to publish workflow: %s", err)), nil
			}
		}
		return MarshalResult(map[string]string{"status": "published", "name": publishedName})
	}
}

func workflowCopyHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		args := request.GetArguments()
		name, _ := args["name"].(string)
		newName, _ := args["newName"].(string)
		if name == "" {
			return gomcp.NewToolResultError("name is required"), nil
		}
		if newName == "" {
			return gomcp.NewToolResultError("newName is required"), nil
		}
		if !safeWorkflowName(name) {
			return gomcp.NewToolResultError(fmt.Sprintf("invalid workflow name: %s", name)), nil
		}
		if !safeWorkflowName(newName) {
			return gomcp.NewToolResultError(fmt.Sprintf("invalid new name: %s", newName)), nil
		}

		if err := workflowSvc(srv).Copy(ctx, name, newName); err != nil {
			switch {
			case errors.Is(err, workflow.ErrAlreadyExists):
				return gomcp.NewToolResultError(fmt.Sprintf("private workflow already exists: %s", newName)), nil
			case errors.Is(err, workflow.ErrNotFound):
				return gomcp.NewToolResultError(fmt.Sprintf("workflow not found: %s", name)), nil
			default:
				return gomcp.NewToolResultError(fmt.Sprintf("failed to copy workflow: %s", err)), nil
			}
		}
		return MarshalResult(map[string]string{"status": "copied", "name": newName})
	}
}
