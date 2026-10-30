package mcp

import (
	"context"
	"errors"
	"fmt"

	gomcp "github.com/mark3labs/mcp-go/mcp"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/workflow"
)

const (
	libScopePrivate = "private"
	libScopeShared  = "shared"
)

// routeLibrary decides how a workflow_*_file call's name is handled. It returns
// (true, nil) when the call is for the @lib library and the caller should run
// the library branch, (false, result) when the call is refused up front, and
// (false, nil) when it is an ordinary workflow call. Only the exact,
// case-sensitive "@lib" routes to the library; every other spelling of the
// reserved name is refused rather than treated as a workflow name.
func routeLibrary(name, scope string) (bool, *gomcp.CallToolResult) {
	if name == workflow.LibraryRefName {
		return true, nil
	}
	if workflow.IsReservedWorkflowName(name) {
		return false, gomcp.NewToolResultError(fmt.Sprintf("%q is reserved; use %s", name, workflow.LibraryRefName))
	}
	if scope != "" {
		return false, gomcp.NewToolResultError("scope is only valid with name " + workflow.LibraryRefName)
	}
	return false, nil
}

// parseLibScope maps the scope argument to a workflow.Scope. An empty scope is
// private (the caller's own library); it reports isDefault so reads can fall
// back to the effective read.
func parseLibScope(ctx context.Context, raw string) (scope workflow.Scope, isDefault bool, err error) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return workflow.Scope{}, false, identity.ErrUnauthenticated
	}
	switch raw {
	case "":
		return workflow.Private(id.UserID), true, nil
	case libScopePrivate:
		return workflow.Private(id.UserID), false, nil
	case libScopeShared:
		return workflow.Shared(), false, nil
	}
	return workflow.Scope{}, false, fmt.Errorf("invalid scope %q: use %q or %q", raw, libScopePrivate, libScopeShared)
}

func scopeLabel(s workflow.Scope) string {
	if s.Kind == workflow.ScopeShared {
		return libScopeShared
	}
	return libScopePrivate
}

// libToolError maps a library service error to a sanitized tool error.
func libToolError(op, p string, err error) *gomcp.CallToolResult {
	switch {
	case errors.Is(err, workflow.ErrUnauthorized):
		return gomcp.NewToolResultError("the shared library requires admin")
	case errors.Is(err, workflow.ErrInvalidLibPath), errors.Is(err, workflow.ErrInvalidFilePath):
		return gomcp.NewToolResultError("invalid library path: " + p)
	case errors.Is(err, workflow.ErrFileNotFound):
		return gomcp.NewToolResultError("not found: " + p)
	case errors.Is(err, workflow.ErrLibraryUnavailable):
		return gomcp.NewToolResultError("the library is not configured")
	case errors.Is(err, identity.ErrUnauthenticated):
		return gomcp.NewToolResultError(err.Error())
	}
	return gomcp.NewToolResultError("failed to " + op + " library file")
}

func libraryListHandler(ctx context.Context, srv *Server, rawScope string) (*gomcp.CallToolResult, error) {
	if _, _, err := parseLibScope(ctx, rawScope); err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	listing, err := workflowSvc(srv).LibraryList(ctx)
	if err != nil {
		return libToolError("list", "", err), nil
	}
	if listing.Private == nil {
		listing.Private = []string{}
	}
	if listing.Shared == nil {
		listing.Shared = []string{}
	}
	return MarshalResult(listing)
}

func libraryReadHandler(ctx context.Context, srv *Server, rawScope, p string) (*gomcp.CallToolResult, error) {
	if p == "" {
		return gomcp.NewToolResultError("path is required"), nil
	}
	scope, isDefault, err := parseLibScope(ctx, rawScope)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	var exact *workflow.Scope
	if !isDefault {
		exact = &scope
	}
	content, err := workflowSvc(srv).LibraryRead(ctx, exact, p)
	if err != nil {
		return libToolError("read", p, err), nil
	}
	return gomcp.NewToolResultText(string(content)), nil
}

func libraryWriteHandler(ctx context.Context, srv *Server, args map[string]any, rawScope, p string) (*gomcp.CallToolResult, error) {
	if p == "" {
		return gomcp.NewToolResultError("path is required"), nil
	}
	rawContent, hasContent := args["content"]
	if !hasContent {
		return gomcp.NewToolResultError("content is required"), nil
	}
	content, isString := rawContent.(string)
	if !isString {
		return gomcp.NewToolResultError("content must be a string"), nil
	}
	if len(content) > maxWriteFileContentBytes {
		return gomcp.NewToolResultError(fmt.Sprintf("content too large: %d bytes exceeds %d byte limit", len(content), maxWriteFileContentBytes)), nil
	}
	scope, _, err := parseLibScope(ctx, rawScope)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	if err := workflowSvc(srv).LibraryWrite(ctx, scope, p, []byte(content)); err != nil {
		return libToolError("write", p, err), nil
	}
	return MarshalResult(map[string]string{"status": "written", "name": workflow.LibraryRefName, "scope": scopeLabel(scope), "path": p})
}

func libraryDeleteHandler(ctx context.Context, srv *Server, rawScope, p string) (*gomcp.CallToolResult, error) {
	if p == "" {
		return gomcp.NewToolResultError("path is required"), nil
	}
	scope, _, err := parseLibScope(ctx, rawScope)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	if err := workflowSvc(srv).LibraryDelete(ctx, scope, p); err != nil {
		return libToolError("delete", p, err), nil
	}
	return MarshalResult(map[string]string{"status": "deleted", "name": workflow.LibraryRefName, "scope": scopeLabel(scope), "path": p})
}
