// adapter/mcp/runner_key_parity_test.go
package mcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/runner"
)

type parityAudit struct{}

func (parityAudit) Record(context.Context, string, string, string) {}

type parityRevoker struct{}

func (parityRevoker) RevokeRunner(context.Context, runner.RunnerID) error { return nil }

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func callRunnerKeyTool(t *testing.T, srv *adaptmcp.Server, ctx context.Context, name string, args map[string]any) string {
	t.Helper()
	req := gomcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	result := srv.MCPServer().HandleMessage(ctx, mustMarshal(t, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": req.Params,
	}))
	data, err := json.Marshal(result)
	require.NoError(t, err)
	var resp struct {
		Result gomcp.CallToolResult `json:"result"`
	}
	require.NoError(t, json.Unmarshal(data, &resp))
	require.False(t, resp.Result.IsError, extractText(t, &resp.Result))
	return extractText(t, &resp.Result)
}

func TestRunnerKeyParity_RESTAndMCPShareShapes(t *testing.T) {
	db, err := adaptsqlite.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, adaptsqlite.RunMigrations(db))
	svc := runner.NewKeyService(runner.KeyServiceDeps{Store: adaptsqlite.NewRunnerKeyRepo(db),
		Registry: adaptsqlite.NewRunnerRegistryRepo(db), Audit: parityAudit{}, Revoker: parityRevoker{}, Clock: time.Now, DefaultStepLimit: 8})
	admin := identity.Identity{UserID: uuid.New(), Username: "root", Role: identity.RoleAdmin}
	ctx := identity.WithIdentity(context.Background(), admin)

	srv := adaptmcp.NewServerWithDeps("test", "0.0.0", &adaptmcp.Deps{RunnerKeys: svc})
	adaptmcp.RegisterRunnerKeyTools(srv)
	var mcpCreate map[string]any
	require.NoError(t, json.Unmarshal([]byte(callRunnerKeyTool(t, srv, ctx, "runner_key_create", map[string]any{"name": "mac-mcp"})), &mcpCreate))

	router := adapthttp.NewRouterFromDeps(adapthttp.RouterDeps{Config: &config.Config{}, RunnerKeys: svc})
	asAdmin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		router.ServeHTTP(w, r.WithContext(identity.WithIdentity(r.Context(), admin)))
	})
	rec := httptest.NewRecorder()
	asAdmin.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/runners/keys", strings.NewReader(`{"name":"mac-rest"}`)))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var restCreate map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &restCreate))

	assert.Equal(t, sortedKeys(restCreate), sortedKeys(mcpCreate), "create envelope parity")
	assert.Equal(t, sortedKeys(restCreate["key"].(map[string]any)), sortedKeys(mcpCreate["key"].(map[string]any)), "key view parity")

	var mcpList []map[string]any
	require.NoError(t, json.Unmarshal([]byte(callRunnerKeyTool(t, srv, ctx, "runner_key_list", nil)), &mcpList))
	rec = httptest.NewRecorder()
	asAdmin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/runners/keys", nil))
	var restList []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &restList))
	require.Len(t, mcpList, 2)
	require.Len(t, restList, 2)
	for i := range restList {
		assert.Equal(t, restList[i], mcpList[i], "same keys, same values, same order")
	}
}
