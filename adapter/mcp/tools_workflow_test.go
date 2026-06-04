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
	// Find the tool handler by calling through the MCPServer.
	// We use the server's ListTools to verify the tool exists, then call via MCPServer.
	req := gomcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args

	ctx := identity.WithIdentity(context.Background(), identity.Identity{
		UserID:   mcpTestUserID,
		Username: "tester",
		Role:     identity.RoleUser,
	})
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
		result := callTool(t, srv, "workflow_delete", map[string]any{"name": "to-delete"})

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
		result := callTool(t, srv, "workflow_delete", map[string]any{"name": "ghost"})

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
		result := callTool(t, srv, "workflow_delete", map[string]any{"name": "pub"})

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
	pub map[string]uuid.UUID
}

func (p *fakePublishedStore) PublisherOf(_ context.Context, name string) (uuid.UUID, bool, error) {
	id, ok := p.pub[name]
	return id, ok, nil
}
func (p *fakePublishedStore) Upsert(_ context.Context, name string, publisher uuid.UUID, _ string) error {
	if p.pub == nil {
		p.pub = map[string]uuid.UUID{}
	}
	p.pub[name] = publisher
	return nil
}
func (p *fakePublishedStore) Delete(_ context.Context, name string) error {
	delete(p.pub, name)
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
	store := &fakePublishedStore{pub: map[string]uuid.UUID{}}
	srv, repo := newPublishServer(t, store)

	// Seed the caller's private source under the test identity's user id.
	require.NoError(t, repo.Create(ctxWithMCPIdentity(), workflow.Private(mcpTestUserID), "src", sampleManifest("src")))

	result := callTool(t, srv, "workflow_publish", map[string]any{"name": "src", "publishedName": "pub"})
	assert.False(t, result.IsError, extractText(t, result))

	ok, err := repo.Exists(ctxWithMCPIdentity(), workflow.Shared(), "pub")
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestWorkflowTool_Publish_NotFound(t *testing.T) {
	store := &fakePublishedStore{pub: map[string]uuid.UUID{}}
	srv, _ := newPublishServer(t, store)

	result := callTool(t, srv, "workflow_publish", map[string]any{"name": "ghost", "publishedName": "pub"})
	assert.True(t, result.IsError)
	assert.Contains(t, extractText(t, result), "not found")
}

func TestWorkflowTool_Publish_InvalidName(t *testing.T) {
	store := &fakePublishedStore{pub: map[string]uuid.UUID{}}
	srv, _ := newPublishServer(t, store)

	result := callTool(t, srv, "workflow_publish", map[string]any{"name": "src", "publishedName": "../escape"})
	assert.True(t, result.IsError)
	assert.Contains(t, extractText(t, result), "invalid published name")
}

func TestWorkflowTool_Copy_HappyPath(t *testing.T) {
	store := &fakePublishedStore{pub: map[string]uuid.UUID{}}
	srv, repo := newPublishServer(t, store)

	require.NoError(t, repo.Create(ctxWithMCPIdentity(), workflow.Shared(), "orig", sampleManifest("orig")))

	result := callTool(t, srv, "workflow_copy", map[string]any{"name": "orig", "newName": "mine"})
	assert.False(t, result.IsError, extractText(t, result))

	ok, err := repo.Exists(ctxWithMCPIdentity(), workflow.Private(mcpTestUserID), "mine")
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestWorkflowTool_Copy_DuplicatePrivateName(t *testing.T) {
	store := &fakePublishedStore{pub: map[string]uuid.UUID{}}
	srv, repo := newPublishServer(t, store)

	require.NoError(t, repo.Create(ctxWithMCPIdentity(), workflow.Shared(), "orig", sampleManifest("orig")))
	require.NoError(t, repo.Create(ctxWithMCPIdentity(), workflow.Private(mcpTestUserID), "mine", sampleManifest("mine")))

	result := callTool(t, srv, "workflow_copy", map[string]any{"name": "orig", "newName": "mine"})
	assert.True(t, result.IsError)
	assert.Contains(t, extractText(t, result), "already exists")
}

func ctxWithMCPIdentity() context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{UserID: mcpTestUserID, Username: "tester", Role: identity.RoleUser})
}
