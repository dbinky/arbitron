package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"
	"time"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/adapter/renderreview"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/layout"
)

// SINK = the layout_revisions row. The renderer never returns until the stack
// closes, so a Create/Update response that arrived at all proves the real HTTP
// and MCP entry points return before arrangement, and the non-terminal row for
// each distinct revision proves each save requested arrangement exactly once.
func TestLayoutE2E_HTTPAndMCPSavesReturnBeforeArrangementAndRequestOnce(t *testing.T) {
	old := layoutRenderReviewerFactory
	t.Cleanup(func() { layoutRenderReviewerFactory = old })
	renderer := &layoutE2EGatedRenderer{entered: make(chan layout.MeasureRequest, 8)}
	layoutRenderReviewerFactory = func(*config.Config, *slog.Logger) (layout.Renderer, layout.Reviewer, func(), error) {
		return renderer, renderreview.NewReadabilityReviewer(nil), nil, nil
	}
	e := newLayoutE2EEnv(t, layoutE2EOptions{})
	ds, closeRoot := e.boot(t)
	t.Cleanup(closeRoot)
	router := newLayoutE2ERouter(t, ds)

	within := func(what string, fn func()) {
		t.Helper()
		done := make(chan struct{})
		go func() { defer close(done); fn() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s waited for arrangement", what)
		}
	}
	body := func(name, label string) string {
		b, err := json.Marshal(layoutE2EWorkflow(name, label))
		require.NoError(t, err)
		return string(b)
	}
	assertPending := func(name string, wantRows int) string {
		t.Helper()
		rev := layoutE2ESavedRevision(t, e.privatePath(name))
		rec, err := ds.layout.store.Get(context.Background(), rev)
		require.NoError(t, err)
		require.Contains(t, []layout.State{layout.StatePending, layout.StateInProgress}, rec.State, "renderer is blocked, so the row cannot be terminal")
		require.Equal(t, wantRows, layoutE2ECountRows(t, ds.db, name))
		return rev
	}

	var code int
	var resp map[string]any
	within("HTTP create", func() {
		code, resp = layoutE2EDo(t, router, http.MethodPost, "/api/v1/workflows/", body("http-async", "First"))
	})
	require.Equal(t, http.StatusCreated, code, "%v", resp)
	first := assertPending("http-async", 1)
	select {
	case <-renderer.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("worker was never fed by the HTTP create")
	}

	within("HTTP update", func() {
		code, resp = layoutE2EDo(t, router, http.MethodPut, "/api/v1/workflows/http-async", body("http-async", "Second"))
	})
	require.Equal(t, http.StatusOK, code, "%v", resp)
	second := assertPending("http-async", 2)
	require.NotEqual(t, first, second, "each save gets its own server-stamped revision")

	srv := adaptmcp.NewServerWithDeps("layout-e2e", "test", ds.mcpDeps())
	adaptmcp.RegisterAllTools(srv)
	manifest := body("mcp-async", "Registered tool input")
	call := func(tool string, args map[string]any) {
		request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
		require.NoError(t, err)
		within("MCP "+tool, func() {
			b, err := json.Marshal(srv.MCPServer().HandleMessage(adminCtxForLocalUser(t, ds), request))
			require.NoError(t, err)
			var rpc struct {
				Result *gomcp.CallToolResult `json:"result"`
				Error  any                   `json:"error"`
			}
			require.NoError(t, json.Unmarshal(b, &rpc))
			require.Nil(t, rpc.Error)
			require.NotNil(t, rpc.Result)
			require.False(t, rpc.Result.IsError, string(b))
		})
	}
	call("workflow_create", map[string]any{"manifest": manifest})
	mcpFirst := assertPending("mcp-async", 1)
	call("workflow_update", map[string]any{"name": "mcp-async", "manifest": body("mcp-async", "Updated")})
	mcpSecond := assertPending("mcp-async", 2)
	require.NotEqual(t, mcpFirst, mcpSecond)
}
