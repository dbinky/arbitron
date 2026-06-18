package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/filesystem"
	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/domain/audit"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// mcpTestUserID is the authenticated caller injected into MCP tool-call
// contexts (the Phase 1 auth middleware supplies this in production).
var mcpTestUserID = uuid.MustParse("88888888-8888-8888-8888-888888888888")

// --- Mocks ---

type stubWorkflowRepo struct {
	listFn       func(ctx context.Context) ([]workflow.WorkflowSummary, error)
	loadFn       func(ctx context.Context, name string) (*workflow.Workflow, error)
	createFn     func(ctx context.Context, name string, wf *workflow.Workflow) error
	updateFn     func(ctx context.Context, name string, wf *workflow.Workflow) error
	deleteFn     func(ctx context.Context, name string) error
	writeFileFn  func(ctx context.Context, name, path string, content []byte) error
	deleteFileFn func(ctx context.Context, name, path string) error
	listFilesFn  func(ctx context.Context, name string) ([]string, error)
}

func (s *stubWorkflowRepo) List(ctx context.Context, scope workflow.Scope) ([]workflow.WorkflowSummary, error) {
	// The canned listing represents the caller's PRIVATE workspace; the shared
	// scope is empty so the service's private+shared merge doesn't double it.
	if scope.Kind == workflow.ScopeShared {
		return nil, nil
	}
	if s.listFn != nil {
		return s.listFn(ctx)
	}
	return nil, nil
}

func (s *stubWorkflowRepo) Load(ctx context.Context, _ workflow.Scope, name string) (*workflow.Workflow, error) {
	if s.loadFn != nil {
		return s.loadFn(ctx, name)
	}
	return nil, nil
}

func (s *stubWorkflowRepo) Create(ctx context.Context, _ workflow.Scope, name string, wf *workflow.Workflow) error {
	if s.createFn != nil {
		return s.createFn(ctx, name, wf)
	}
	return nil
}

func (s *stubWorkflowRepo) Update(ctx context.Context, _ workflow.Scope, name string, wf *workflow.Workflow) error {
	if s.updateFn != nil {
		return s.updateFn(ctx, name, wf)
	}
	return nil
}

func (s *stubWorkflowRepo) Delete(ctx context.Context, _ workflow.Scope, name string) error {
	if s.deleteFn != nil {
		return s.deleteFn(ctx, name)
	}
	return nil
}

func (s *stubWorkflowRepo) WriteFile(ctx context.Context, _ workflow.Scope, name, relativePath string, content []byte) error {
	if s.writeFileFn != nil {
		return s.writeFileFn(ctx, name, relativePath, content)
	}
	return nil
}

func (s *stubWorkflowRepo) DeleteFile(ctx context.Context, _ workflow.Scope, name, relativePath string) error {
	if s.deleteFileFn != nil {
		return s.deleteFileFn(ctx, name, relativePath)
	}
	return nil
}

func (s *stubWorkflowRepo) ListFiles(ctx context.Context, _ workflow.Scope, name string) ([]string, error) {
	if s.listFilesFn != nil {
		return s.listFilesFn(ctx, name)
	}
	return nil, nil
}

func (s *stubWorkflowRepo) SaveNodePositions(_ context.Context, _ workflow.Scope, _ string, _ map[string]workflow.NodePosition) error {
	return nil
}

func (s *stubWorkflowRepo) Exists(_ context.Context, _ workflow.Scope, _ string) (bool, error) {
	return false, nil
}

func (s *stubWorkflowRepo) CopyTree(_ context.Context, _ workflow.Scope, _ string, _ workflow.Scope, _ string, _ bool) error {
	return nil
}

// stubFileReader is the configurable FileReader used in MCP tool tests.
// capturedOwner records the last owner UUID passed to ReadFile so tests can
// verify that callerOwner(ctx) is correctly forwarded by the MCP tool handler.
type stubFileReader struct {
	readFileFn    func(workflowName, path string) ([]byte, error)
	fileExistsFn  func(workflowName, path string) bool
	capturedOwner uuid.UUID
}

func (s *stubFileReader) ReadFile(owner uuid.UUID, workflowName, relativePath string) ([]byte, error) {
	s.capturedOwner = owner
	if s.readFileFn != nil {
		return s.readFileFn(workflowName, relativePath)
	}
	return nil, nil
}

func (s *stubFileReader) ResolvePath(_ uuid.UUID, _, _ string) (string, error) { return "", nil }

func (s *stubFileReader) FileExists(_ uuid.UUID, workflowName, relativePath string) bool {
	if s.fileExistsFn != nil {
		return s.fileExistsFn(workflowName, relativePath)
	}
	return false
}

// --- Helpers ---

func newWorkflowServer(repo *stubWorkflowRepo, fr *stubFileReader) *adaptmcp.Server {
	deps := &adaptmcp.Deps{
		Workflows:  repo,
		FileReader: fr,
	}
	srv := adaptmcp.NewServerWithDeps("test", "0.0.0", deps)
	adaptmcp.RegisterWorkflowTools(srv)
	return srv
}

func callTool(t *testing.T, srv *adaptmcp.Server, name string, args map[string]any) *gomcp.CallToolResult {
	t.Helper()
	return callToolWithIdentity(t, srv, name, args, identity.Identity{
		UserID:   mcpTestUserID,
		Username: "tester",
		Role:     identity.RoleUser,
	})
}

// callToolAdmin invokes a tool with an admin identity (same user id), so
// role-gated publish/visibility tools can promote to public.
func callToolAdmin(t *testing.T, srv *adaptmcp.Server, name string, args map[string]any) *gomcp.CallToolResult {
	t.Helper()
	return callToolWithIdentity(t, srv, name, args, identity.Identity{
		UserID:   mcpTestUserID,
		Username: "admin",
		Role:     identity.RoleAdmin,
	})
}

func callToolWithIdentity(t *testing.T, srv *adaptmcp.Server, name string, args map[string]any, id identity.Identity) *gomcp.CallToolResult {
	t.Helper()
	req := gomcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args

	ctx := identity.WithIdentity(context.Background(), id)
	result := srv.MCPServer().HandleMessage(ctx, mustMarshal(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  req.Params,
	}))

	// Parse the JSON-RPC response to extract the result.
	data, err := json.Marshal(result)
	require.NoError(t, err)

	var resp struct {
		Result gomcp.CallToolResult `json:"result"`
	}
	require.NoError(t, json.Unmarshal(data, &resp))
	return &resp.Result
}

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return data
}

func extractText(t *testing.T, result *gomcp.CallToolResult) string {
	t.Helper()
	require.NotNil(t, result)
	require.NotEmpty(t, result.Content)
	text, ok := result.Content[0].(gomcp.TextContent)
	require.True(t, ok, "expected TextContent, got %T", result.Content[0])
	return text.Text
}

// --- Tests ---

func TestWorkflowList(t *testing.T) {
	t.Run("happy path returns summaries", func(t *testing.T) {
		repo := &stubWorkflowRepo{
			listFn: func(_ context.Context) ([]workflow.WorkflowSummary, error) {
				return []workflow.WorkflowSummary{
					{Name: "wf1", Description: "first", NodeCount: 3},
				}, nil
			},
		}
		srv := newWorkflowServer(repo, &stubFileReader{})
		result := callTool(t, srv, "workflow_list", nil)

		assert.False(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "wf1")
	})

	t.Run("domain error returned as tool error", func(t *testing.T) {
		repo := &stubWorkflowRepo{
			listFn: func(_ context.Context) ([]workflow.WorkflowSummary, error) {
				return nil, errors.New("db down")
			},
		}
		srv := newWorkflowServer(repo, &stubFileReader{})
		result := callTool(t, srv, "workflow_list", nil)

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "db down")
	})

	t.Run("nil summaries returns empty array not null", func(t *testing.T) {
		// Empty workspace: List returns nil slice. Handler must normalise to []
		// so LLM clients receive [] and can safely iterate without a null-check.
		repo := &stubWorkflowRepo{
			listFn: func(_ context.Context) ([]workflow.WorkflowSummary, error) {
				return nil, nil
			},
		}
		srv := newWorkflowServer(repo, &stubFileReader{})
		result := callTool(t, srv, "workflow_list", nil)

		assert.False(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "[]", "nil summaries must serialise as [] not null")
	})
}

func TestWorkflowGet(t *testing.T) {
	t.Run("happy path returns workflow", func(t *testing.T) {
		repo := &stubWorkflowRepo{
			loadFn: func(_ context.Context, name string) (*workflow.Workflow, error) {
				return &workflow.Workflow{Name: name, Description: "test wf"}, nil
			},
		}
		srv := newWorkflowServer(repo, &stubFileReader{})
		result := callTool(t, srv, "workflow_get", map[string]any{"name": "my-wf"})

		assert.False(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "my-wf")
	})

	t.Run("missing name returns error", func(t *testing.T) {
		srv := newWorkflowServer(&stubWorkflowRepo{}, &stubFileReader{})
		result := callTool(t, srv, "workflow_get", map[string]any{})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "name is required")
	})

	t.Run("domain error returned as tool error", func(t *testing.T) {
		repo := &stubWorkflowRepo{
			loadFn: func(_ context.Context, _ string) (*workflow.Workflow, error) {
				return nil, errors.New("not found")
			},
		}
		srv := newWorkflowServer(repo, &stubFileReader{})
		result := callTool(t, srv, "workflow_get", map[string]any{"name": "missing"})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "not found")
	})

	t.Run("nil workflow without error returns not found", func(t *testing.T) {
		// stubWorkflowRepo.Load returns (nil, nil) by default — handler must guard
		// against nil wf before marshaling, otherwise it returns confusing JSON null.
		srv := newWorkflowServer(&stubWorkflowRepo{}, &stubFileReader{})
		result := callTool(t, srv, "workflow_get", map[string]any{"name": "ghost"})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "not found")
	})
}

func TestWorkflowCreate(t *testing.T) {
	t.Run("happy path creates workflow", func(t *testing.T) {
		var capturedName string
		repo := &stubWorkflowRepo{
			createFn: func(_ context.Context, name string, _ *workflow.Workflow) error {
				capturedName = name
				return nil
			},
		}
		srv := newWorkflowServer(repo, &stubFileReader{})

		manifest := `{"name":"new-wf","description":"test"}`
		result := callTool(t, srv, "workflow_create", map[string]any{"manifest": manifest})

		assert.False(t, result.IsError)
		assert.Equal(t, "new-wf", capturedName)
		text := extractText(t, result)
		assert.Contains(t, text, "created")
	})

	t.Run("missing manifest returns error", func(t *testing.T) {
		srv := newWorkflowServer(&stubWorkflowRepo{}, &stubFileReader{})
		result := callTool(t, srv, "workflow_create", map[string]any{})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "manifest is required")
	})

	t.Run("invalid JSON manifest returns error", func(t *testing.T) {
		srv := newWorkflowServer(&stubWorkflowRepo{}, &stubFileReader{})
		result := callTool(t, srv, "workflow_create", map[string]any{"manifest": "not-json"})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "invalid manifest JSON")
	})

	t.Run("domain error returned as tool error", func(t *testing.T) {
		repo := &stubWorkflowRepo{
			createFn: func(_ context.Context, _ string, _ *workflow.Workflow) error {
				return errors.New("already exists")
			},
		}
		srv := newWorkflowServer(repo, &stubFileReader{})
		result := callTool(t, srv, "workflow_create", map[string]any{"manifest": `{"name":"dup"}`})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "already exists")
	})
}

func TestWorkflowUpdate(t *testing.T) {
	t.Run("happy path updates workflow", func(t *testing.T) {
		var capturedName string
		repo := &stubWorkflowRepo{
			updateFn: func(_ context.Context, name string, _ *workflow.Workflow) error {
				capturedName = name
				return nil
			},
		}
		srv := newWorkflowServer(repo, &stubFileReader{})
		result := callTool(t, srv, "workflow_update", map[string]any{
			"name":     "my-wf",
			"manifest": `{"name":"my-wf","description":"updated"}`,
		})

		assert.False(t, result.IsError)
		assert.Equal(t, "my-wf", capturedName)
	})

	t.Run("missing name returns error", func(t *testing.T) {
		srv := newWorkflowServer(&stubWorkflowRepo{}, &stubFileReader{})
		result := callTool(t, srv, "workflow_update", map[string]any{"manifest": `{}`})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "name is required")
	})

	t.Run("missing manifest returns error", func(t *testing.T) {
		srv := newWorkflowServer(&stubWorkflowRepo{}, &stubFileReader{})
		result := callTool(t, srv, "workflow_update", map[string]any{"name": "x"})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "manifest is required")
	})

	t.Run("invalid JSON manifest returns error", func(t *testing.T) {
		srv := newWorkflowServer(&stubWorkflowRepo{}, &stubFileReader{})
		result := callTool(t, srv, "workflow_update", map[string]any{
			"name":     "my-wf",
			"manifest": "not-json",
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "invalid manifest JSON")
	})

	t.Run("domain error returned as tool error", func(t *testing.T) {
		repo := &stubWorkflowRepo{
			updateFn: func(_ context.Context, _ string, _ *workflow.Workflow) error {
				return errors.New("workflow locked")
			},
		}
		srv := newWorkflowServer(repo, &stubFileReader{})
		result := callTool(t, srv, "workflow_update", map[string]any{
			"name":     "my-wf",
			"manifest": `{"name":"my-wf"}`,
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "workflow locked")
	})

	t.Run("tool name arg wins when manifest name differs", func(t *testing.T) {
		// The "name" tool arg identifies which workflow to update (authoritative).
		// A conflicting "name" field in the manifest must be overwritten before
		// the manifest is stored, otherwise workflow.json diverges from its directory.
		var capturedWF *workflow.Workflow
		repo := &stubWorkflowRepo{
			updateFn: func(_ context.Context, _ string, wf *workflow.Workflow) error {
				capturedWF = wf
				return nil
			},
		}
		srv := newWorkflowServer(repo, &stubFileReader{})
		result := callTool(t, srv, "workflow_update", map[string]any{
			"name":     "real-name",
			"manifest": `{"name":"wrong-name","description":"updated"}`,
		})

		assert.False(t, result.IsError)
		require.NotNil(t, capturedWF)
		assert.Equal(t, "real-name", capturedWF.Name)
	})

	// Failure: updating a shared-only workflow returns a tool error with the
	// "shared and read-only" message — not a generic internal error.
	t.Run("shared-only workflow returns tool error", func(t *testing.T) {
		repo := &stubWorkflowRepo{
			updateFn: func(_ context.Context, _ string, _ *workflow.Workflow) error {
				return fmt.Errorf("%w: pub", workflow.ErrSharedReadOnly)
			},
		}
		srv := newWorkflowServer(repo, &stubFileReader{})
		result := callTool(t, srv, "workflow_update", map[string]any{
			"name":     "pub",
			"manifest": `{"name":"pub"}`,
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "shared and read-only")
	})
}

func TestWorkflowDelete(t *testing.T) {
	t.Run("happy path deletes workflow", func(t *testing.T) {
		var capturedName string
		repo := &stubWorkflowRepo{
			deleteFn: func(_ context.Context, name string) error {
				capturedName = name
				return nil
			},
		}
		srv := newWorkflowServer(repo, &stubFileReader{})
		// workflow_delete is admin-gated (workflow-management Phase 3 Task 2).
		result := callToolAdmin(t, srv, "workflow_delete", map[string]any{"name": "to-delete"})

		assert.False(t, result.IsError)
		assert.Equal(t, "to-delete", capturedName)
	})

	t.Run("missing name returns error", func(t *testing.T) {
		srv := newWorkflowServer(&stubWorkflowRepo{}, &stubFileReader{})
		result := callTool(t, srv, "workflow_delete", map[string]any{})

		assert.True(t, result.IsError)
	})

	t.Run("domain error returned as tool error", func(t *testing.T) {
		repo := &stubWorkflowRepo{
			deleteFn: func(_ context.Context, _ string) error {
				return errors.New("workflow not found")
			},
		}
		srv := newWorkflowServer(repo, &stubFileReader{})
		result := callToolAdmin(t, srv, "workflow_delete", map[string]any{"name": "ghost"})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "workflow not found")
	})

	// Failure: deleting a shared-only workflow returns a tool error with the
	// "shared and read-only" message — not a generic internal error.
	t.Run("shared-only workflow returns tool error", func(t *testing.T) {
		repo := &stubWorkflowRepo{
			deleteFn: func(_ context.Context, _ string) error {
				return fmt.Errorf("%w: pub", workflow.ErrSharedReadOnly)
			},
		}
		srv := newWorkflowServer(repo, &stubFileReader{})
		result := callToolAdmin(t, srv, "workflow_delete", map[string]any{"name": "pub"})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "shared and read-only")
	})
}

func TestWorkflowValidate(t *testing.T) {
	t.Run("valid workflow returns valid=true", func(t *testing.T) {
		repo := &stubWorkflowRepo{
			loadFn: func(_ context.Context, _ string) (*workflow.Workflow, error) {
				return &workflow.Workflow{
					Name: "valid-wf",
					Nodes: []workflow.Node{
						{ID: "start", Type: workflow.NodeTypeLLMCall, Entrypoint: true, Config: workflow.NodeConfig{TemplateFile: "t.txt"}},
					},
				}, nil
			},
		}
		fr := &stubFileReader{
			fileExistsFn: func(_, _ string) bool { return true },
		}
		srv := newWorkflowServer(repo, fr)
		result := callTool(t, srv, "workflow_validate", map[string]any{"name": "valid-wf"})

		assert.False(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, `"valid": true`)
	})

	t.Run("invalid workflow returns validation errors", func(t *testing.T) {
		repo := &stubWorkflowRepo{
			loadFn: func(_ context.Context, _ string) (*workflow.Workflow, error) {
				return &workflow.Workflow{Name: "bad-wf"}, nil // no entry node
			},
		}
		srv := newWorkflowServer(repo, &stubFileReader{})
		result := callTool(t, srv, "workflow_validate", map[string]any{"name": "bad-wf"})

		assert.False(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, `"valid": false`)
	})

	t.Run("missing name returns error", func(t *testing.T) {
		srv := newWorkflowServer(&stubWorkflowRepo{}, &stubFileReader{})
		result := callTool(t, srv, "workflow_validate", map[string]any{})

		assert.True(t, result.IsError)
	})

	t.Run("load error returned as tool error", func(t *testing.T) {
		repo := &stubWorkflowRepo{
			loadFn: func(_ context.Context, _ string) (*workflow.Workflow, error) {
				return nil, errors.New("workflow not found")
			},
		}
		srv := newWorkflowServer(repo, &stubFileReader{})
		result := callTool(t, srv, "workflow_validate", map[string]any{"name": "missing-wf"})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "workflow not found")
	})

	t.Run("nil workflow without error returns not found", func(t *testing.T) {
		// stubWorkflowRepo.Load returns (nil, nil) by default — handler must guard
		// against nil wf before passing to Validate, otherwise Validate panics.
		srv := newWorkflowServer(&stubWorkflowRepo{}, &stubFileReader{})
		result := callTool(t, srv, "workflow_validate", map[string]any{"name": "ghost"})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "not found")
	})

	t.Run("valid workflow errors field is JSON array not null", func(t *testing.T) {
		// Edge case: Validate returns nil on success; handler must normalize to []
		// so LLM clients can iterate errors without a nil-dereference.
		repo := &stubWorkflowRepo{
			loadFn: func(_ context.Context, _ string) (*workflow.Workflow, error) {
				return &workflow.Workflow{
					Name: "ok-wf",
					Nodes: []workflow.Node{
						{ID: "start", Type: workflow.NodeTypeLLMCall, Entrypoint: true, Config: workflow.NodeConfig{TemplateFile: "t.txt"}},
					},
				}, nil
			},
		}
		fr := &stubFileReader{fileExistsFn: func(_, _ string) bool { return true }}
		srv := newWorkflowServer(repo, fr)
		result := callTool(t, srv, "workflow_validate", map[string]any{"name": "ok-wf"})

		assert.False(t, result.IsError)
		text := extractText(t, result)
		// Must serialize as [] not null so the client can iterate.
		assert.Contains(t, text, `"errors": []`)
		assert.NotContains(t, text, `"errors": null`)
	})

	t.Run("threads injected profile checker into validation", func(t *testing.T) {
		// An llm_call referencing an agent-kind profile must fail validation with
		// PROFILE_KIND_MISMATCH, proving the workflow_validate tool forwards the
		// Deps.ProfileChecker to workflow.Validate.
		repo := &stubWorkflowRepo{
			loadFn: func(_ context.Context, _ string) (*workflow.Workflow, error) {
				return &workflow.Workflow{
					Name: "mismatch-wf",
					Nodes: []workflow.Node{
						{ID: "llm", Type: workflow.NodeTypeLLMCall, Entrypoint: true, Config: workflow.NodeConfig{ModelProfile: "an-agent"}},
					},
				}, nil
			},
		}
		deps := &adaptmcp.Deps{
			Workflows:      repo,
			FileReader:     &stubFileReader{fileExistsFn: func(_, _ string) bool { return true }},
			ProfileChecker: mcpStubProfileChecker{"an-agent": "agent"},
		}
		srv := adaptmcp.NewServerWithDeps("test", "0.0.0", deps)
		adaptmcp.RegisterWorkflowTools(srv)

		result := callTool(t, srv, "workflow_validate", map[string]any{"name": "mismatch-wf"})

		assert.False(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, `"valid": false`)
		assert.Contains(t, text, "PROFILE_KIND_MISMATCH")
	})

	t.Run("threads injected connection checker into validation", func(t *testing.T) {
		// An llm_call whose profile references a missing connection must fail
		// validation with UNKNOWN_CONNECTION, proving the workflow_validate tool
		// forwards Deps.ConnChecker to workflow.Validate.
		repo := &stubWorkflowRepo{
			loadFn: func(_ context.Context, _ string) (*workflow.Workflow, error) {
				return &workflow.Workflow{
					Name: "conn-wf",
					Nodes: []workflow.Node{
						{ID: "llm", Type: workflow.NodeTypeLLMCall, Entrypoint: true, Config: workflow.NodeConfig{ModelProfile: "az-chat"}},
					},
				}, nil
			},
		}
		deps := &adaptmcp.Deps{
			Workflows:      repo,
			FileReader:     &stubFileReader{fileExistsFn: func(_, _ string) bool { return true }},
			ProfileChecker: mcpStubProfileChecker{"az-chat": "chat"},
			ConnChecker: mcpStubConnChecker{
				refs:    map[string]string{"az-chat": "azure-prod"},
				present: map[string]bool{},
			},
		}
		srv := adaptmcp.NewServerWithDeps("test", "0.0.0", deps)
		adaptmcp.RegisterWorkflowTools(srv)

		result := callTool(t, srv, "workflow_validate", map[string]any{"name": "conn-wf"})

		assert.False(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, `"valid": false`)
		assert.Contains(t, text, "UNKNOWN_CONNECTION")
	})
}

// mcpStubProfileChecker is a minimal workflow.ProfileChecker for MCP tool tests.
type mcpStubProfileChecker map[string]string

func (s mcpStubProfileChecker) ProfileKind(alias string) (string, bool) {
	kind, ok := s[alias]
	return kind, ok
}

// mcpStubConnChecker is a minimal workflow.ConnectionChecker for MCP tool tests.
type mcpStubConnChecker struct {
	refs    map[string]string
	present map[string]bool
}

func (s mcpStubConnChecker) ProfileConnectionRef(alias string) (string, bool) {
	ref, ok := s.refs[alias]
	return ref, ok
}

func (s mcpStubConnChecker) ConnectionExists(name string) bool { return s.present[name] }

func TestWorkflowListFiles(t *testing.T) {
	t.Run("happy path returns files", func(t *testing.T) {
		repo := &stubWorkflowRepo{
			listFilesFn: func(_ context.Context, _ string) ([]string, error) {
				return []string{"prompt.txt", "script.sh"}, nil
			},
		}
		srv := newWorkflowServer(repo, &stubFileReader{})
		result := callTool(t, srv, "workflow_list_files", map[string]any{"name": "wf1"})

		assert.False(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "prompt.txt")
		assert.Contains(t, text, "script.sh")
	})

	t.Run("missing name returns error", func(t *testing.T) {
		srv := newWorkflowServer(&stubWorkflowRepo{}, &stubFileReader{})
		result := callTool(t, srv, "workflow_list_files", map[string]any{})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "name is required")
	})

	t.Run("domain error returned as tool error", func(t *testing.T) {
		repo := &stubWorkflowRepo{
			listFilesFn: func(_ context.Context, _ string) ([]string, error) {
				return nil, errors.New("no such workflow")
			},
		}
		srv := newWorkflowServer(repo, &stubFileReader{})
		result := callTool(t, srv, "workflow_list_files", map[string]any{"name": "x"})

		assert.True(t, result.IsError)
	})

	t.Run("nil repo result produces JSON array not null", func(t *testing.T) {
		// Edge case: repo returns nil slice (zero files); handler must normalize to
		// [] so LLM clients can iterate without a nil-dereference on a JSON null.
		repo := &stubWorkflowRepo{
			listFilesFn: func(_ context.Context, _ string) ([]string, error) {
				return nil, nil
			},
		}
		srv := newWorkflowServer(repo, &stubFileReader{})
		result := callTool(t, srv, "workflow_list_files", map[string]any{"name": "empty-wf"})

		assert.False(t, result.IsError)
		text := extractText(t, result)
		assert.Equal(t, "[]", text)
	})
}

func TestWorkflowReadFile(t *testing.T) {
	t.Run("happy path returns file content as text", func(t *testing.T) {
		fr := &stubFileReader{
			readFileFn: func(_, _ string) ([]byte, error) {
				return []byte("hello world"), nil
			},
		}
		srv := newWorkflowServer(&stubWorkflowRepo{}, fr)
		result := callTool(t, srv, "workflow_read_file", map[string]any{"name": "wf1", "path": "readme.txt"})

		assert.False(t, result.IsError)
		text := extractText(t, result)
		assert.Equal(t, "hello world", text)
	})

	t.Run("missing name returns error", func(t *testing.T) {
		srv := newWorkflowServer(&stubWorkflowRepo{}, &stubFileReader{})
		result := callTool(t, srv, "workflow_read_file", map[string]any{"path": "readme.txt"})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "name is required")
	})

	t.Run("missing path returns error", func(t *testing.T) {
		srv := newWorkflowServer(&stubWorkflowRepo{}, &stubFileReader{})
		result := callTool(t, srv, "workflow_read_file", map[string]any{"name": "wf1"})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "path is required")
	})

	t.Run("read error returned as tool error", func(t *testing.T) {
		fr := &stubFileReader{
			readFileFn: func(_, _ string) ([]byte, error) {
				return nil, errors.New("file not found")
			},
		}
		srv := newWorkflowServer(&stubWorkflowRepo{}, fr)
		result := callTool(t, srv, "workflow_read_file", map[string]any{"name": "wf1", "path": "missing.txt"})

		assert.True(t, result.IsError)
	})

	// Edge: verify that the MCP tool handler forwards the caller's UserID from
	// context through callerOwner() into the FileReader. callTool injects
	// mcpTestUserID via identity.WithIdentity — a regression would serve files
	// from the wrong owner partition silently.
	t.Run("caller identity UserID is forwarded to file reader as owner", func(t *testing.T) {
		fr := &stubFileReader{
			readFileFn: func(_, _ string) ([]byte, error) { return []byte("data"), nil },
		}
		srv := newWorkflowServer(&stubWorkflowRepo{}, fr)
		result := callTool(t, srv, "workflow_read_file", map[string]any{"name": "wf1", "path": "readme.txt"})

		assert.False(t, result.IsError)
		assert.Equal(t, mcpTestUserID, fr.capturedOwner,
			"workflow_read_file must pass the caller's UserID to FileReader, not uuid.Nil or a different user")
	})
}

func TestWorkflowWriteFile(t *testing.T) {
	t.Run("missing name returns error", func(t *testing.T) {
		srv := newWorkflowServer(&stubWorkflowRepo{}, &stubFileReader{})
		result := callTool(t, srv, "workflow_write_file", map[string]any{"path": "x.txt", "content": "data"})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "name is required")
	})

	t.Run("missing path returns error", func(t *testing.T) {
		srv := newWorkflowServer(&stubWorkflowRepo{}, &stubFileReader{})
		result := callTool(t, srv, "workflow_write_file", map[string]any{"name": "wf1", "content": "data"})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "path is required")
	})

	t.Run("happy path writes file", func(t *testing.T) {
		var capturedContent []byte
		repo := &stubWorkflowRepo{
			writeFileFn: func(_ context.Context, _, _ string, content []byte) error {
				capturedContent = content
				return nil
			},
		}
		srv := newWorkflowServer(repo, &stubFileReader{})
		result := callTool(t, srv, "workflow_write_file", map[string]any{
			"name": "wf1", "path": "test.txt", "content": "file content",
		})

		assert.False(t, result.IsError)
		assert.Equal(t, []byte("file content"), capturedContent)
	})

	t.Run("empty content writes empty file", func(t *testing.T) {
		// Edge case: empty string content is valid — creates an empty file.
		// This mirrors "PUT file with empty body" from the REST API design.
		var capturedContent []byte
		repo := &stubWorkflowRepo{
			writeFileFn: func(_ context.Context, _, _ string, content []byte) error {
				capturedContent = content
				return nil
			},
		}
		srv := newWorkflowServer(repo, &stubFileReader{})
		result := callTool(t, srv, "workflow_write_file", map[string]any{
			"name": "wf1", "path": "empty.txt", "content": "",
		})

		assert.False(t, result.IsError)
		assert.Len(t, capturedContent, 0)
	})

	t.Run("missing content returns error", func(t *testing.T) {
		// Failure: content key is absent entirely — must be rejected.
		srv := newWorkflowServer(&stubWorkflowRepo{}, &stubFileReader{})
		result := callTool(t, srv, "workflow_write_file", map[string]any{"name": "wf1", "path": "x"})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "content is required")
	})

	t.Run("domain error returned as tool error", func(t *testing.T) {
		repo := &stubWorkflowRepo{
			writeFileFn: func(_ context.Context, _, _ string, _ []byte) error {
				return errors.New("disk full")
			},
		}
		srv := newWorkflowServer(repo, &stubFileReader{})
		result := callTool(t, srv, "workflow_write_file", map[string]any{
			"name": "wf1", "path": "x.txt", "content": "data",
		})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "disk full")
	})
}

func TestWorkflowDeleteFile(t *testing.T) {
	t.Run("happy path deletes file", func(t *testing.T) {
		var capturedPath string
		repo := &stubWorkflowRepo{
			deleteFileFn: func(_ context.Context, _, path string) error {
				capturedPath = path
				return nil
			},
		}
		srv := newWorkflowServer(repo, &stubFileReader{})
		result := callTool(t, srv, "workflow_delete_file", map[string]any{"name": "wf1", "path": "old.txt"})

		assert.False(t, result.IsError)
		assert.Equal(t, "old.txt", capturedPath)
	})

	t.Run("missing name returns error", func(t *testing.T) {
		srv := newWorkflowServer(&stubWorkflowRepo{}, &stubFileReader{})
		result := callTool(t, srv, "workflow_delete_file", map[string]any{"path": "old.txt"})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "name is required")
	})

	t.Run("missing path returns error", func(t *testing.T) {
		srv := newWorkflowServer(&stubWorkflowRepo{}, &stubFileReader{})
		result := callTool(t, srv, "workflow_delete_file", map[string]any{"name": "wf1"})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "path is required")
	})

	t.Run("domain error returned as tool error", func(t *testing.T) {
		repo := &stubWorkflowRepo{
			deleteFileFn: func(_ context.Context, _, _ string) error {
				return errors.New("not found")
			},
		}
		srv := newWorkflowServer(repo, &stubFileReader{})
		result := callTool(t, srv, "workflow_delete_file", map[string]any{"name": "wf1", "path": "x"})

		assert.True(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, "not found")
	})
}

// --- Publish / Copy ---

// fakePublishedStore is a workflow.PublishedStore for MCP publish/copy tests.
type fakePublishedStore struct {
	pub map[string]workflow.Publication
}

func (p *fakePublishedStore) PublisherOf(_ context.Context, name string) (uuid.UUID, bool, error) {
	pub, ok := p.pub[name]
	return pub.Publisher, ok, nil
}
func (p *fakePublishedStore) PublicationOf(_ context.Context, name string) (workflow.Publication, bool, error) {
	pub, ok := p.pub[name]
	return pub, ok, nil
}
func (p *fakePublishedStore) Upsert(_ context.Context, name string, publisher uuid.UUID, sourceName string, visibility workflow.Visibility, department string) error {
	if p.pub == nil {
		p.pub = map[string]workflow.Publication{}
	}
	p.pub[name] = workflow.Publication{Publisher: publisher, SourceName: sourceName, Visibility: visibility, Department: department}
	return nil
}
func (p *fakePublishedStore) Delete(_ context.Context, name string) error {
	delete(p.pub, name)
	return nil
}
func (p *fakePublishedStore) SetWorkflowID(_ context.Context, _ string, _ uuid.UUID) error {
	return nil
}

// newPublishServer builds an MCP server whose WorkflowSvc is backed by a real
// filesystem repo (temp dir) and the given publisher store, so the publish/copy
// tools exercise the real service path.
func newPublishServer(t *testing.T, store *fakePublishedStore) (*adaptmcp.Server, *filesystem.WorkflowRepo) {
	t.Helper()
	repo := filesystem.NewWorkflowRepo(t.TempDir())
	deps := &adaptmcp.Deps{
		Workflows:   repo,
		WorkflowSvc: workflow.NewWorkflowService(repo, store),
		FileReader:  &stubFileReader{},
	}
	srv := adaptmcp.NewServerWithDeps("test", "0.0.0", deps)
	adaptmcp.RegisterWorkflowTools(srv)
	return srv, repo
}

func sampleManifest(name string) *workflow.Workflow {
	return &workflow.Workflow{Name: name, Description: "d", Author: "a"}
}

func TestWorkflowTool_Publish_HappyPath(t *testing.T) {
	store := &fakePublishedStore{pub: map[string]workflow.Publication{}}
	srv, repo := newPublishServer(t, store)

	// Seed the caller's private source under the test identity's user id.
	require.NoError(t, repo.Create(ctxWithMCPIdentity(), workflow.Private(mcpTestUserID), "src", sampleManifest("src")))

	result := callToolAdmin(t, srv, "workflow_publish", map[string]any{"name": "src", "publishedName": "pub"})
	assert.False(t, result.IsError, extractText(t, result))

	ok, err := repo.Exists(ctxWithMCPIdentity(), workflow.Shared(), "pub")
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestWorkflowTool_Publish_NotFound(t *testing.T) {
	store := &fakePublishedStore{pub: map[string]workflow.Publication{}}
	srv, _ := newPublishServer(t, store)

	result := callToolAdmin(t, srv, "workflow_publish", map[string]any{"name": "ghost", "publishedName": "pub"})
	assert.True(t, result.IsError)
	assert.Contains(t, extractText(t, result), "not found")
}

func TestWorkflowTool_Publish_InvalidName(t *testing.T) {
	store := &fakePublishedStore{pub: map[string]workflow.Publication{}}
	srv, _ := newPublishServer(t, store)

	result := callTool(t, srv, "workflow_publish", map[string]any{"name": "src", "publishedName": "../escape"})
	assert.True(t, result.IsError)
	assert.Contains(t, extractText(t, result), "invalid published name")
}

// TestWorkflowTool_Publish_DotName verifies that "." is rejected as both source
// name and publishedName at the MCP adapter layer, not silently passed through to
// the filesystem where it would match the scope root and return an opaque error.
func TestWorkflowTool_Publish_DotName(t *testing.T) {
	store := &fakePublishedStore{pub: map[string]workflow.Publication{}}
	srv, _ := newPublishServer(t, store)

	t.Run("dot source name rejected", func(t *testing.T) {
		result := callTool(t, srv, "workflow_publish", map[string]any{"name": ".", "publishedName": "pub"})
		assert.True(t, result.IsError)
		assert.Contains(t, extractText(t, result), "invalid workflow name")
	})
	t.Run("dot publishedName rejected", func(t *testing.T) {
		result := callTool(t, srv, "workflow_publish", map[string]any{"name": "src", "publishedName": "."})
		assert.True(t, result.IsError)
		assert.Contains(t, extractText(t, result), "invalid published name")
	})
}

// TestWorkflowTool_Copy_DotName verifies that "." is rejected as both source
// and destination name at the MCP adapter layer.
func TestWorkflowTool_Copy_DotName(t *testing.T) {
	store := &fakePublishedStore{pub: map[string]workflow.Publication{}}
	srv, _ := newPublishServer(t, store)

	t.Run("dot source name rejected", func(t *testing.T) {
		result := callTool(t, srv, "workflow_copy", map[string]any{"name": ".", "newName": "mine"})
		assert.True(t, result.IsError)
		assert.Contains(t, extractText(t, result), "invalid workflow name")
	})
	t.Run("dot newName rejected", func(t *testing.T) {
		result := callTool(t, srv, "workflow_copy", map[string]any{"name": "orig", "newName": "."})
		assert.True(t, result.IsError)
		assert.Contains(t, extractText(t, result), "invalid new name")
	})
}

func TestWorkflowTool_Copy_HappyPath(t *testing.T) {
	store := &fakePublishedStore{pub: map[string]workflow.Publication{}}
	srv, repo := newPublishServer(t, store)

	require.NoError(t, repo.Create(ctxWithMCPIdentity(), workflow.Shared(), "orig", sampleManifest("orig")))

	result := callTool(t, srv, "workflow_copy", map[string]any{"name": "orig", "newName": "mine"})
	assert.False(t, result.IsError, extractText(t, result))

	ok, err := repo.Exists(ctxWithMCPIdentity(), workflow.Private(mcpTestUserID), "mine")
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestWorkflowTool_Copy_DuplicatePrivateName(t *testing.T) {
	store := &fakePublishedStore{pub: map[string]workflow.Publication{}}
	srv, repo := newPublishServer(t, store)

	require.NoError(t, repo.Create(ctxWithMCPIdentity(), workflow.Shared(), "orig", sampleManifest("orig")))
	require.NoError(t, repo.Create(ctxWithMCPIdentity(), workflow.Private(mcpTestUserID), "mine", sampleManifest("mine")))

	result := callTool(t, srv, "workflow_copy", map[string]any{"name": "orig", "newName": "mine"})
	assert.True(t, result.IsError)
	assert.Contains(t, extractText(t, result), "already exists")
}

// TestWorkflowTool_Publish_DifferentPublisher_Unauthorized verifies the publisher-only-overwrite
// invariant on the MCP surface: a caller cannot re-publish under a name already owned by
// a different user.
func TestWorkflowTool_Publish_DifferentPublisher_Unauthorized(t *testing.T) {
	otherUser := uuid.New()
	// Pre-seed the store so "pub" is already owned by a different user.
	store := &fakePublishedStore{pub: map[string]workflow.Publication{"pub": {Publisher: otherUser, Visibility: workflow.VisibilityPublic}}}
	srv, repo := newPublishServer(t, store)

	require.NoError(t, repo.Create(ctxWithMCPIdentity(), workflow.Private(mcpTestUserID), "src", sampleManifest("src")))

	result := callTool(t, srv, "workflow_publish", map[string]any{"name": "src", "publishedName": "pub"})
	assert.True(t, result.IsError)
	assert.Contains(t, extractText(t, result), "not authorized")
}

// TestWorkflowTool_Copy_SharedMissing_NotFound verifies that copying a shared workflow
// that does not exist returns a not-found tool error, not a server error.
func TestWorkflowTool_Copy_SharedMissing_NotFound(t *testing.T) {
	store := &fakePublishedStore{pub: map[string]workflow.Publication{}}
	srv, _ := newPublishServer(t, store)

	result := callTool(t, srv, "workflow_copy", map[string]any{"name": "ghost", "newName": "mine"})
	assert.True(t, result.IsError)
	assert.Contains(t, extractText(t, result), "not found")
}

// --- SetVisibility ---

// Happy path: a lead of the publication's department can promote it to public.
func TestWorkflowTool_SetVisibility_LeadPromotesToPublic(t *testing.T) {
	store := &fakePublishedStore{pub: map[string]workflow.Publication{
		"eng-wf": {Publisher: mcpTestUserID, SourceName: "src", Visibility: workflow.VisibilityDepartment, Department: "engineering"},
	}}
	srv, _ := newPublishServer(t, store)

	result := callToolWithIdentity(t, srv, "workflow_set_visibility", map[string]any{
		"name": "eng-wf", "visibility": "public",
	}, identity.Identity{
		UserID: mcpTestUserID, Username: "lead", Role: identity.RoleUser,
		Departments: []string{"engineering"}, LedDepartments: []string{"engineering"},
	})
	assert.False(t, result.IsError, extractText(t, result))
	assert.Equal(t, workflow.VisibilityPublic, store.pub["eng-wf"].Visibility)
}

// Success: admin can change visibility of any publication regardless of whether
// they are the publisher or a department member. Admin is both authorized to
// mutate and authorized to publish to public.
func TestWorkflowTool_SetVisibility_AdminCanChange(t *testing.T) {
	otherPublisher := uuid.New()
	store := &fakePublishedStore{pub: map[string]workflow.Publication{
		"eng-wf": {Publisher: otherPublisher, SourceName: "src", Visibility: workflow.VisibilityDepartment, Department: "engineering"},
	}}
	srv, _ := newPublishServer(t, store)

	result := callToolAdmin(t, srv, "workflow_set_visibility", map[string]any{
		"name": "eng-wf", "visibility": "public",
	})
	assert.False(t, result.IsError, extractText(t, result))
	assert.Equal(t, workflow.VisibilityPublic, store.pub["eng-wf"].Visibility)
}

// Failure: a plain member (not lead, not admin) cannot promote a publication to
// public — the role gate at authorizePublish is enforced on the MCP surface.
func TestWorkflowTool_SetVisibility_MemberCannotPromoteToPublic(t *testing.T) {
	store := &fakePublishedStore{pub: map[string]workflow.Publication{
		"eng-wf": {Publisher: mcpTestUserID, SourceName: "src", Visibility: workflow.VisibilityDepartment, Department: "engineering"},
	}}
	srv, _ := newPublishServer(t, store)

	// callTool uses RoleUser with no led departments.
	result := callTool(t, srv, "workflow_set_visibility", map[string]any{
		"name": "eng-wf", "visibility": "public",
	})
	assert.True(t, result.IsError)
	assert.Contains(t, extractText(t, result), "not authorized")
}

// Failure: an unknown publication name returns a not-found error, not a server
// error. This verifies the ErrNotFound → ToolResultError mapping on the MCP surface.
func TestWorkflowTool_SetVisibility_UnknownName_NotFound(t *testing.T) {
	store := &fakePublishedStore{pub: map[string]workflow.Publication{}}
	srv, _ := newPublishServer(t, store)

	result := callToolAdmin(t, srv, "workflow_set_visibility", map[string]any{
		"name": "ghost", "visibility": "public",
	})
	assert.True(t, result.IsError)
	assert.Contains(t, extractText(t, result), "not found")
}

// Error: an invalid visibility string (e.g. "private") is rejected with an
// invalid-visibility error before reaching the store.
func TestWorkflowTool_SetVisibility_InvalidVisibility(t *testing.T) {
	store := &fakePublishedStore{pub: map[string]workflow.Publication{
		"eng-wf": {Publisher: mcpTestUserID, SourceName: "src", Visibility: workflow.VisibilityDepartment, Department: "engineering"},
	}}
	srv, _ := newPublishServer(t, store)

	result := callToolAdmin(t, srv, "workflow_set_visibility", map[string]any{
		"name": "eng-wf", "visibility": "private",
	})
	assert.True(t, result.IsError)
	assert.Contains(t, extractText(t, result), "invalid visibility")
}

// Edge: an unsafe workflow name (e.g. "../escape") is rejected at the adapter
// input validation layer, not passed through to the service.
func TestWorkflowTool_SetVisibility_InvalidName(t *testing.T) {
	store := &fakePublishedStore{pub: map[string]workflow.Publication{}}
	srv, _ := newPublishServer(t, store)

	result := callToolAdmin(t, srv, "workflow_set_visibility", map[string]any{
		"name": "../escape", "visibility": "public",
	})
	assert.True(t, result.IsError)
	assert.Contains(t, extractText(t, result), "invalid workflow name")
}

func ctxWithMCPIdentity() context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{UserID: mcpTestUserID, Username: "tester", Role: identity.RoleUser})
}

// ---------------------------------------------------------------------------
// Phase 3 Task 4: lifecycle MCP tools (rename/unpublish/duplicate/archive/
// unarchive/transfer_ownership/audit)
// ---------------------------------------------------------------------------

// memRegistry is an in-memory workflow.WorkflowRegistry for the lifecycle MCP
// tool tests (the admin ops require a wired registry).
type memRegistry struct {
	byID   map[uuid.UUID]workflow.RegistryEntry
	byName map[string]uuid.UUID
}

func newMemRegistry() *memRegistry {
	return &memRegistry{byID: map[uuid.UUID]workflow.RegistryEntry{}, byName: map[string]uuid.UUID{}}
}

func (r *memRegistry) Register(_ context.Context, e workflow.RegistryEntry) error {
	if _, ok := r.byName[e.Name]; ok {
		return workflow.ErrAlreadyExists
	}
	r.byID[e.ID] = e
	r.byName[e.Name] = e.ID
	return nil
}

func (r *memRegistry) ResolveID(_ context.Context, name string) (uuid.UUID, error) {
	if id, ok := r.byName[name]; ok {
		return id, nil
	}
	return uuid.Nil, workflow.ErrNotFound
}

func (r *memRegistry) ResolveName(_ context.Context, id uuid.UUID) (string, error) {
	if e, ok := r.byID[id]; ok {
		return e.Name, nil
	}
	return "", workflow.ErrNotFound
}

func (r *memRegistry) Rename(_ context.Context, id uuid.UUID, newName string) error {
	e, ok := r.byID[id]
	if !ok {
		return workflow.ErrNotFound
	}
	if _, taken := r.byName[newName]; taken {
		return workflow.ErrAlreadyExists
	}
	delete(r.byName, e.Name)
	e.Name = newName
	r.byID[id] = e
	r.byName[newName] = id
	return nil
}

func (r *memRegistry) SetArchived(_ context.Context, id uuid.UUID, archived bool) error {
	e, ok := r.byID[id]
	if !ok {
		return workflow.ErrNotFound
	}
	e.Archived = archived
	r.byID[id] = e
	return nil
}

func (r *memRegistry) SetOwner(_ context.Context, id, owner uuid.UUID) error {
	e, ok := r.byID[id]
	if !ok {
		return workflow.ErrNotFound
	}
	e.OwnerID = owner
	r.byID[id] = e
	return nil
}

func (r *memRegistry) Delete(_ context.Context, id uuid.UUID) error {
	e, ok := r.byID[id]
	if !ok {
		return workflow.ErrNotFound
	}
	delete(r.byID, id)
	delete(r.byName, e.Name)
	return nil
}

func (r *memRegistry) List(_ context.Context) ([]workflow.RegistryEntry, error) {
	out := make([]workflow.RegistryEntry, 0, len(r.byID))
	for _, e := range r.byID {
		out = append(out, e)
	}
	return out, nil
}

// memActiveCounter is an in-memory workflow.ActiveInstanceCounter; active maps a
// workflow id to its non-terminal instance count.
type memActiveCounter struct{ active map[uuid.UUID]int }

func (c *memActiveCounter) CountActiveByWorkflowID(_ context.Context, id uuid.UUID) (int, error) {
	return c.active[id], nil
}

// recordingMCPStore is an in-memory audit.EventStore for the workflow_audit tool
// tests.
type recordingMCPStore struct{ events []audit.Event }

func (s *recordingMCPStore) Append(_ context.Context, ev audit.Event) error {
	s.events = append(s.events, ev)
	return nil
}
func (s *recordingMCPStore) ListByActor(context.Context, string, int) ([]audit.Event, error) {
	return s.events, nil
}
func (s *recordingMCPStore) ListByTarget(_ context.Context, target string) ([]audit.Event, error) {
	out := make([]audit.Event, 0)
	// Newest-first: iterate in reverse insertion order.
	for i := len(s.events) - 1; i >= 0; i-- {
		if s.events[i].Target == target {
			out = append(out, s.events[i])
		}
	}
	return out, nil
}

// lifecycleFixture bundles the lifecycle MCP server and its backing stores.
type lifecycleFixture struct {
	srv     *adaptmcp.Server
	repo    *filesystem.WorkflowRepo
	reg     *memRegistry
	counter *memActiveCounter
	store   *recordingMCPStore
}

// newLifecycleServer builds an MCP server whose WorkflowSvc is registry-backed
// (the admin lifecycle ops require it), with an active-instance counter, a
// published store, and an audit store wired for the workflow_audit tool.
func newLifecycleServer(t *testing.T) *lifecycleFixture {
	t.Helper()
	repo := filesystem.NewWorkflowRepo(t.TempDir())
	reg := newMemRegistry()
	counter := &memActiveCounter{active: map[uuid.UUID]int{}}
	store := &recordingMCPStore{}
	pubStore := &fakePublishedStore{pub: map[string]workflow.Publication{}}

	svc := workflow.NewWorkflowService(repo, pubStore)
	svc.SetRegistry(reg)
	svc.SetActiveInstanceCounter(counter)
	svc.SetAudit(audit.NewService(store))

	deps := &adaptmcp.Deps{
		Workflows:   repo,
		WorkflowSvc: svc,
		AuditSvc:    audit.NewService(store),
		FileReader:  &stubFileReader{},
	}
	srv := adaptmcp.NewServerWithDeps("test", "0.0.0", deps)
	adaptmcp.RegisterWorkflowTools(srv)
	return &lifecycleFixture{srv: srv, repo: repo, reg: reg, counter: counter, store: store}
}

// seedRegisteredPrivate creates a registered private workflow owned by owner and
// returns its stable id.
func (f *lifecycleFixture) seedRegisteredPrivate(t *testing.T, owner uuid.UUID, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	w := sampleManifest(name)
	w.ID = id
	require.NoError(t, f.repo.Create(ctxWithMCPIdentity(), workflow.Private(owner), name, w))
	require.NoError(t, f.reg.Register(context.Background(), workflow.RegistryEntry{
		ID: id, Name: name, Scope: workflow.ScopePrivate, OwnerID: owner,
	}))
	return id
}

// --- workflow_rename ---

func TestWorkflowTool_Rename_HappyPath(t *testing.T) {
	f := newLifecycleServer(t)
	f.seedRegisteredPrivate(t, mcpTestUserID, "alpha")

	result := callToolAdmin(t, f.srv, "workflow_rename", map[string]any{"name": "alpha", "newName": "beta"})
	assert.False(t, result.IsError, extractText(t, result))

	_, err := f.reg.ResolveID(context.Background(), "beta")
	require.NoError(t, err)
}

func TestWorkflowTool_Rename_NonAdmin_Forbidden(t *testing.T) {
	f := newLifecycleServer(t)
	f.seedRegisteredPrivate(t, mcpTestUserID, "alpha")

	result := callTool(t, f.srv, "workflow_rename", map[string]any{"name": "alpha", "newName": "beta"})
	assert.True(t, result.IsError)
	assert.Contains(t, extractText(t, result), "not authorized")
}

func TestWorkflowTool_Rename_TakenName(t *testing.T) {
	f := newLifecycleServer(t)
	f.seedRegisteredPrivate(t, mcpTestUserID, "alpha")
	f.seedRegisteredPrivate(t, mcpTestUserID, "beta")

	result := callToolAdmin(t, f.srv, "workflow_rename", map[string]any{"name": "alpha", "newName": "beta"})
	assert.True(t, result.IsError)
	assert.Contains(t, extractText(t, result), "already exists")
}

func TestWorkflowTool_Rename_InvalidName(t *testing.T) {
	f := newLifecycleServer(t)
	result := callToolAdmin(t, f.srv, "workflow_rename", map[string]any{"name": "alpha", "newName": "../escape"})
	assert.True(t, result.IsError)
	assert.Contains(t, extractText(t, result), "invalid")
}

func TestWorkflowTool_Rename_NotFound(t *testing.T) {
	f := newLifecycleServer(t)
	result := callToolAdmin(t, f.srv, "workflow_rename", map[string]any{"name": "ghost", "newName": "beta"})
	assert.True(t, result.IsError)
	assert.Contains(t, extractText(t, result), "not found")
}

func TestWorkflowTool_Rename_MissingName(t *testing.T) {
	f := newLifecycleServer(t)
	result := callToolAdmin(t, f.srv, "workflow_rename", map[string]any{"newName": "beta"})
	assert.True(t, result.IsError)
	assert.Contains(t, extractText(t, result), "name is required")
}

func TestWorkflowTool_Rename_MissingNewName(t *testing.T) {
	f := newLifecycleServer(t)
	result := callToolAdmin(t, f.srv, "workflow_rename", map[string]any{"name": "alpha"})
	assert.True(t, result.IsError)
	assert.Contains(t, extractText(t, result), "newName is required")
}

// --- workflow_unpublish ---

func TestWorkflowTool_Unpublish_HappyPath(t *testing.T) {
	f := newLifecycleServer(t)
	// Publish a private source first so there is a real publication record + shared
	// folder for Unpublish to remove (admin may publish to public).
	require.NoError(t, f.repo.Create(ctxWithMCPIdentity(), workflow.Private(mcpTestUserID), "src", sampleManifest("src")))
	require.False(t, callToolAdmin(t, f.srv, "workflow_publish", map[string]any{
		"name": "src", "publishedName": "pub", "visibility": "public",
	}).IsError)

	result := callToolAdmin(t, f.srv, "workflow_unpublish", map[string]any{"name": "pub"})
	assert.False(t, result.IsError, extractText(t, result))

	ok, err := f.repo.Exists(ctxWithMCPIdentity(), workflow.Shared(), "pub")
	require.NoError(t, err)
	assert.False(t, ok, "shared folder removed")
}

func TestWorkflowTool_Unpublish_UnknownName_NotFound(t *testing.T) {
	f := newLifecycleServer(t)
	result := callToolAdmin(t, f.srv, "workflow_unpublish", map[string]any{"name": "ghost"})
	assert.True(t, result.IsError)
	assert.Contains(t, extractText(t, result), "not found")
}

func TestWorkflowTool_Unpublish_NonAdmin_Forbidden(t *testing.T) {
	f := newLifecycleServer(t)
	result := callTool(t, f.srv, "workflow_unpublish", map[string]any{"name": "pub"})
	assert.True(t, result.IsError)
	assert.Contains(t, extractText(t, result), "not authorized")
}

// --- workflow_duplicate ---

func TestWorkflowTool_Duplicate_HappyPath(t *testing.T) {
	f := newLifecycleServer(t)
	f.seedRegisteredPrivate(t, mcpTestUserID, "alpha")

	result := callToolAdmin(t, f.srv, "workflow_duplicate", map[string]any{"name": "alpha", "newName": "alpha-copy"})
	assert.False(t, result.IsError, extractText(t, result))

	_, err := f.reg.ResolveID(context.Background(), "alpha-copy")
	require.NoError(t, err)
}

func TestWorkflowTool_Duplicate_NonAdmin_Forbidden(t *testing.T) {
	f := newLifecycleServer(t)
	f.seedRegisteredPrivate(t, mcpTestUserID, "alpha")

	result := callTool(t, f.srv, "workflow_duplicate", map[string]any{"name": "alpha", "newName": "alpha-copy"})
	assert.True(t, result.IsError)
	assert.Contains(t, extractText(t, result), "not authorized")
}

func TestWorkflowTool_Duplicate_NotFound(t *testing.T) {
	f := newLifecycleServer(t)
	result := callToolAdmin(t, f.srv, "workflow_duplicate", map[string]any{"name": "ghost", "newName": "copy"})
	assert.True(t, result.IsError)
	assert.Contains(t, extractText(t, result), "not found")
}

func TestWorkflowTool_Duplicate_TakenName(t *testing.T) {
	f := newLifecycleServer(t)
	f.seedRegisteredPrivate(t, mcpTestUserID, "alpha")
	f.seedRegisteredPrivate(t, mcpTestUserID, "taken")

	result := callToolAdmin(t, f.srv, "workflow_duplicate", map[string]any{"name": "alpha", "newName": "taken"})
	assert.True(t, result.IsError)
	assert.Contains(t, extractText(t, result), "already exists")
}

// --- workflow_archive / workflow_unarchive ---

func TestWorkflowTool_Archive_HappyPath(t *testing.T) {
	f := newLifecycleServer(t)
	id := f.seedRegisteredPrivate(t, mcpTestUserID, "alpha")

	result := callToolAdmin(t, f.srv, "workflow_archive", map[string]any{"name": "alpha"})
	assert.False(t, result.IsError, extractText(t, result))
	assert.True(t, f.reg.byID[id].Archived)
}

func TestWorkflowTool_Archive_NonAdmin_Forbidden(t *testing.T) {
	f := newLifecycleServer(t)
	f.seedRegisteredPrivate(t, mcpTestUserID, "alpha")

	result := callTool(t, f.srv, "workflow_archive", map[string]any{"name": "alpha"})
	assert.True(t, result.IsError)
	assert.Contains(t, extractText(t, result), "not authorized")
}

func TestWorkflowTool_Unarchive_HappyPath(t *testing.T) {
	f := newLifecycleServer(t)
	id := f.seedRegisteredPrivate(t, mcpTestUserID, "alpha")
	require.NoError(t, f.reg.SetArchived(context.Background(), id, true))

	result := callToolAdmin(t, f.srv, "workflow_unarchive", map[string]any{"name": "alpha"})
	assert.False(t, result.IsError, extractText(t, result))
	assert.False(t, f.reg.byID[id].Archived)
}

func TestWorkflowTool_Unarchive_NonAdmin_Forbidden(t *testing.T) {
	f := newLifecycleServer(t)
	f.seedRegisteredPrivate(t, mcpTestUserID, "alpha")

	result := callTool(t, f.srv, "workflow_unarchive", map[string]any{"name": "alpha"})
	assert.True(t, result.IsError)
	assert.Contains(t, extractText(t, result), "not authorized")
}

func TestWorkflowTool_Archive_UnknownName_NotFound(t *testing.T) {
	f := newLifecycleServer(t)
	result := callToolAdmin(t, f.srv, "workflow_archive", map[string]any{"name": "ghost"})
	assert.True(t, result.IsError)
	assert.Contains(t, extractText(t, result), "not found")
}

// --- workflow_transfer_ownership ---

func TestWorkflowTool_TransferOwnership_HappyPath(t *testing.T) {
	f := newLifecycleServer(t)
	id := f.seedRegisteredPrivate(t, mcpTestUserID, "alpha")
	newOwner := uuid.New()

	result := callToolAdmin(t, f.srv, "workflow_transfer_ownership", map[string]any{
		"name": "alpha", "newOwner": newOwner.String(),
	})
	assert.False(t, result.IsError, extractText(t, result))
	assert.Equal(t, newOwner, f.reg.byID[id].OwnerID)
}

func TestWorkflowTool_TransferOwnership_NonAdmin_Forbidden(t *testing.T) {
	f := newLifecycleServer(t)
	f.seedRegisteredPrivate(t, mcpTestUserID, "alpha")

	result := callTool(t, f.srv, "workflow_transfer_ownership", map[string]any{
		"name": "alpha", "newOwner": uuid.New().String(),
	})
	assert.True(t, result.IsError)
	assert.Contains(t, extractText(t, result), "not authorized")
}

func TestWorkflowTool_TransferOwnership_BadOwnerID(t *testing.T) {
	f := newLifecycleServer(t)
	f.seedRegisteredPrivate(t, mcpTestUserID, "alpha")

	result := callToolAdmin(t, f.srv, "workflow_transfer_ownership", map[string]any{
		"name": "alpha", "newOwner": "not-a-uuid",
	})
	assert.True(t, result.IsError)
	assert.Contains(t, extractText(t, result), "newOwner")
}

func TestWorkflowTool_TransferOwnership_NotFound(t *testing.T) {
	f := newLifecycleServer(t)
	result := callToolAdmin(t, f.srv, "workflow_transfer_ownership", map[string]any{
		"name": "ghost", "newOwner": mcpTestUserID.String(),
	})
	assert.True(t, result.IsError)
	assert.Contains(t, extractText(t, result), "not found")
}

// --- workflow_delete admin-gated + running guard ---

func TestWorkflowTool_Delete_RunningInstances_Error(t *testing.T) {
	f := newLifecycleServer(t)
	id := f.seedRegisteredPrivate(t, mcpTestUserID, "alpha")
	f.counter.active[id] = 1 // one live run

	result := callToolAdmin(t, f.srv, "workflow_delete", map[string]any{"name": "alpha"})
	assert.True(t, result.IsError)
	assert.Contains(t, extractText(t, result), "running instances")
}

// --- workflow_audit ---

func TestWorkflowTool_Audit_HappyPath_NewestFirst(t *testing.T) {
	f := newLifecycleServer(t)
	f.seedRegisteredPrivate(t, mcpTestUserID, "alpha")

	// Two ops against "alpha" → two audit events, newest first.
	require.False(t, callToolAdmin(t, f.srv, "workflow_archive", map[string]any{"name": "alpha"}).IsError)
	require.False(t, callToolAdmin(t, f.srv, "workflow_unarchive", map[string]any{"name": "alpha"}).IsError)

	result := callToolAdmin(t, f.srv, "workflow_audit", map[string]any{"name": "alpha"})
	assert.False(t, result.IsError, extractText(t, result))

	var events []audit.Event
	require.NoError(t, json.Unmarshal([]byte(extractText(t, result)), &events))
	require.Len(t, events, 2)
	assert.Equal(t, audit.ActionWorkflowUnarchive, events[0].Action, "newest event first")
	assert.Equal(t, audit.ActionWorkflowArchive, events[1].Action)
}

func TestWorkflowTool_Audit_UnknownWorkflow_EmptyList(t *testing.T) {
	f := newLifecycleServer(t)
	result := callToolAdmin(t, f.srv, "workflow_audit", map[string]any{"name": "ghost"})
	assert.False(t, result.IsError, extractText(t, result))

	var events []audit.Event
	require.NoError(t, json.Unmarshal([]byte(extractText(t, result)), &events))
	assert.Empty(t, events)
}

func TestWorkflowTool_Audit_NonAdmin_Forbidden(t *testing.T) {
	f := newLifecycleServer(t)
	result := callTool(t, f.srv, "workflow_audit", map[string]any{"name": "alpha"})
	assert.True(t, result.IsError)
	assert.Contains(t, extractText(t, result), "not authorized")
}

// --- registration ---

// TestRegisterWorkflowTools_RegistersLifecycleTools enumerates the lifecycle
// tools added in Phase 3 Task 4 and asserts each is registered.
func TestRegisterWorkflowTools_RegistersLifecycleTools(t *testing.T) {
	f := newLifecycleServer(t)
	want := []string{
		"workflow_rename",
		"workflow_unpublish",
		"workflow_duplicate",
		"workflow_archive",
		"workflow_unarchive",
		"workflow_transfer_ownership",
		"workflow_audit",
		"workflow_delete",
	}
	for _, name := range want {
		t.Run(name, func(t *testing.T) {
			// A registered tool returns a (possibly error) result; an unregistered
			// tool yields a JSON-RPC "tool not found" with an empty result content.
			result := callToolAdmin(t, f.srv, name, map[string]any{"name": "alpha", "newName": "beta", "newOwner": uuid.New().String()})
			require.NotNil(t, result)
			require.NotEmpty(t, result.Content, "tool %q must be registered (non-empty result)", name)
		})
	}
}
