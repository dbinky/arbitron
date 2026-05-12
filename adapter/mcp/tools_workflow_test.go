package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/domain/workflow"
)

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

func (s *stubWorkflowRepo) List(ctx context.Context) ([]workflow.WorkflowSummary, error) {
	if s.listFn != nil {
		return s.listFn(ctx)
	}
	return nil, nil
}

func (s *stubWorkflowRepo) Load(ctx context.Context, name string) (*workflow.Workflow, error) {
	if s.loadFn != nil {
		return s.loadFn(ctx, name)
	}
	return nil, nil
}

func (s *stubWorkflowRepo) Create(ctx context.Context, name string, wf *workflow.Workflow) error {
	if s.createFn != nil {
		return s.createFn(ctx, name, wf)
	}
	return nil
}

func (s *stubWorkflowRepo) Update(ctx context.Context, name string, wf *workflow.Workflow) error {
	if s.updateFn != nil {
		return s.updateFn(ctx, name, wf)
	}
	return nil
}

func (s *stubWorkflowRepo) Delete(ctx context.Context, name string) error {
	if s.deleteFn != nil {
		return s.deleteFn(ctx, name)
	}
	return nil
}

func (s *stubWorkflowRepo) WriteFile(ctx context.Context, name, relativePath string, content []byte) error {
	if s.writeFileFn != nil {
		return s.writeFileFn(ctx, name, relativePath, content)
	}
	return nil
}

func (s *stubWorkflowRepo) DeleteFile(ctx context.Context, name, relativePath string) error {
	if s.deleteFileFn != nil {
		return s.deleteFileFn(ctx, name, relativePath)
	}
	return nil
}

func (s *stubWorkflowRepo) ListFiles(ctx context.Context, name string) ([]string, error) {
	if s.listFilesFn != nil {
		return s.listFilesFn(ctx, name)
	}
	return nil, nil
}

func (s *stubWorkflowRepo) SaveNodePositions(_ context.Context, _ string, _ map[string]workflow.NodePosition) error {
	return nil
}

type stubFileReader struct {
	readFileFn   func(workflowName, path string) ([]byte, error)
	fileExistsFn func(workflowName, path string) bool
}

func (s *stubFileReader) ReadFile(workflowName, relativePath string) ([]byte, error) {
	if s.readFileFn != nil {
		return s.readFileFn(workflowName, relativePath)
	}
	return nil, nil
}

func (s *stubFileReader) ResolvePath(_, _ string) (string, error) { return "", nil }

func (s *stubFileReader) FileExists(workflowName, relativePath string) bool {
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

	result := srv.MCPServer().HandleMessage(context.Background(), mustMarshal(t, map[string]any{
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
}

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
}

func TestWorkflowWriteFile(t *testing.T) {
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

	t.Run("domain error returned as tool error", func(t *testing.T) {
		repo := &stubWorkflowRepo{
			deleteFileFn: func(_ context.Context, _, _ string) error {
				return errors.New("not found")
			},
		}
		srv := newWorkflowServer(repo, &stubFileReader{})
		result := callTool(t, srv, "workflow_delete_file", map[string]any{"name": "wf1", "path": "x"})

		assert.True(t, result.IsError)
	})
}
