// adapter/mcp/tools_runner_key_test.go
package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/audit"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/runner"
)

type mcpKeyAudit struct {
	mu      sync.Mutex
	actions []string
}

func (a *mcpKeyAudit) Record(_ context.Context, action, target, _ string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.actions = append(a.actions, action+" "+target)
}

type mcpKeyRevoker struct{ ids []runner.RunnerID }

func (r *mcpKeyRevoker) RevokeRunner(_ context.Context, id runner.RunnerID) error {
	r.ids = append(r.ids, id)
	return nil
}

func runnerKeyToolServer(t *testing.T) (*Server, *mcpKeyAudit, *mcpKeyRevoker) {
	t.Helper()
	db, err := adaptsqlite.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, adaptsqlite.RunMigrations(db))
	a, rv := &mcpKeyAudit{}, &mcpKeyRevoker{}
	svc := runner.NewKeyService(runner.KeyServiceDeps{Store: adaptsqlite.NewRunnerKeyRepo(db),
		Registry: adaptsqlite.NewRunnerRegistryRepo(db), Audit: a, Revoker: rv, Clock: time.Now, DefaultStepLimit: 8})
	return NewServerWithDeps("test", "0.0.0", &Deps{RunnerKeys: svc}), a, rv
}

func runnerKeyAdminCtx() context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{UserID: uuid.New(), Username: "root", Role: identity.RoleAdmin})
}

func TestRunnerKeyTools_Lifecycle(t *testing.T) {
	srv, a, rv := runnerKeyToolServer(t)
	ctx := runnerKeyAdminCtx()

	res, err := runnerKeyCreateHandler(srv)(ctx, callReq(map[string]any{"name": "mac-a"}))
	require.NoError(t, err)
	require.False(t, res.IsError, toolText(t, res))
	body := toolText(t, res)
	assert.Contains(t, body, `"plaintext":"ark_`)
	assert.NotContains(t, strings.ToLower(body), "hash")

	listRes, err := runnerKeyListHandler(srv)(ctx, callReq(nil))
	require.NoError(t, err)
	var keys []map[string]any
	require.NoError(t, json.Unmarshal([]byte(toolText(t, listRes)), &keys))
	require.Len(t, keys, 1)
	assert.NotContains(t, toolText(t, listRes), "plaintext")
	id := keys[0]["id"].(string)

	noAck, _ := runnerKeyRevokeHandler(srv)(ctx, callReq(map[string]any{"id": id}))
	assert.True(t, noAck.IsError)
	assert.Contains(t, toolText(t, noAck), "acknowledge")
	assert.Empty(t, rv.ids)

	revRes, err := runnerKeyRevokeHandler(srv)(ctx, callReq(map[string]any{"id": id, "acknowledge": true}))
	require.NoError(t, err)
	require.False(t, revRes.IsError, toolText(t, revRes))
	assert.Equal(t, []runner.RunnerID{"mac-a"}, rv.ids)
	assert.Equal(t, []string{audit.ActionRunnerKeyCreate + " mac-a", audit.ActionRunnerKeyRevoke + " mac-a"}, a.actions)
}

func TestRunnerKeyTools_AuthzAndValidation(t *testing.T) {
	srv, a, _ := runnerKeyToolServer(t)
	user := userCtx(uuid.New())

	res, _ := runnerKeyCreateHandler(srv)(user, callReq(map[string]any{"name": "mac-a"}))
	assert.True(t, res.IsError)
	assert.Contains(t, toolText(t, res), "admin role required")
	res, _ = runnerKeyListHandler(srv)(user, callReq(nil))
	assert.True(t, res.IsError)
	res, _ = runnerKeyCreateHandler(srv)(context.Background(), callReq(map[string]any{"name": "mac-a"}))
	assert.Contains(t, toolText(t, res), "authentication required")

	admin := runnerKeyAdminCtx()
	res, _ = runnerKeyCreateHandler(srv)(admin, callReq(map[string]any{"name": "Bad Name"}))
	assert.True(t, res.IsError)
	res, _ = runnerKeyRevokeHandler(srv)(admin, callReq(map[string]any{"id": "nope", "acknowledge": true}))
	assert.True(t, res.IsError)
	res, _ = runnerKeyRevokeHandler(srv)(admin, callReq(map[string]any{"id": uuid.NewString(), "acknowledge": true}))
	assert.Contains(t, toolText(t, res), "not found")
	assert.Empty(t, a.actions)

	unwired := NewServerWithDeps("test", "0.0.0", &Deps{})
	res, _ = runnerKeyListHandler(unwired)(admin, callReq(nil))
	assert.Contains(t, toolText(t, res), "not available")
}

func TestRunnerKeyTools_Registered(t *testing.T) {
	srv := NewServerWithDeps("test", "0.0.0", &Deps{})
	RegisterAllTools(srv)
	names := map[string]bool{}
	for _, tool := range srv.ListTools() {
		names[tool.Name] = true
	}
	for _, n := range []string{"runner_key_create", "runner_key_list", "runner_key_revoke"} {
		assert.True(t, names[n], n)
	}
}
