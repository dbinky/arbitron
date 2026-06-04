package mcp_test

// authz_e2e_test.go — end-to-end adapter-level authorization tests.
//
// These tests wire the REAL engine (not a stub) so that domain-layer scoping
// rules are exercised through the MCP handler exactly as they would be in
// production. A non-admin identity is injected into the tool context (as the
// auth middleware would in org mode) and each tool call must respect the
// owner-based isolation rules.
//
// Covered: instance_get, instance_pause, trace_get, variables_get,
//          and instance_list (parity-confirmed in parity_test.go).

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
)

// authzInstanceRepo is a minimal in-memory InstanceRepository that the real
// engine can use. Get returns the stored instance or ErrNotFound; List honors
// the OwnerID filter. All other methods are no-ops.
type authzInstanceRepo struct {
	instances map[uuid.UUID]*instance.Instance
}

func newAuthzInstanceRepo(insts ...*instance.Instance) *authzInstanceRepo {
	r := &authzInstanceRepo{instances: make(map[uuid.UUID]*instance.Instance)}
	for _, i := range insts {
		r.instances[i.ID] = i
	}
	return r
}

func (r *authzInstanceRepo) Get(_ context.Context, id uuid.UUID) (*instance.Instance, error) {
	inst, ok := r.instances[id]
	if !ok {
		return nil, instance.ErrNotFound
	}
	return inst, nil
}

func (r *authzInstanceRepo) List(_ context.Context, f instance.ListFilters) ([]instance.InstanceSummary, error) {
	var out []instance.InstanceSummary
	for _, inst := range r.instances {
		if f.OwnerID != nil && inst.OwnerID != *f.OwnerID {
			continue
		}
		out = append(out, instance.InstanceSummary{ID: inst.ID, WorkflowName: inst.WorkflowName, State: inst.State})
	}
	return out, nil
}

func (r *authzInstanceRepo) Create(_ context.Context, _ *instance.Instance) error { return nil }
func (r *authzInstanceRepo) UpdateState(_ context.Context, _ uuid.UUID, _ instance.State) error {
	return nil
}
func (r *authzInstanceRepo) UpdateVariables(_ context.Context, _ uuid.UUID, _ map[string]any) error {
	return nil
}
func (r *authzInstanceRepo) UpdateCurrentNode(_ context.Context, _ uuid.UUID, _ string) error {
	return nil
}
func (r *authzInstanceRepo) SetFailed(_ context.Context, _ uuid.UUID, _ string) error { return nil }
func (r *authzInstanceRepo) SetCompleted(_ context.Context, _ uuid.UUID) error         { return nil }
func (r *authzInstanceRepo) ListRunning(_ context.Context) ([]instance.Instance, error) {
	return nil, nil
}
func (r *authzInstanceRepo) UpdatePriority(_ context.Context, _ uuid.UUID, _ instance.Priority) error {
	return nil
}
func (r *authzInstanceRepo) SetWaiting(_ context.Context, _ uuid.UUID, _ *time.Time) error { return nil }
func (r *authzInstanceRepo) SetWebhookFields(_ context.Context, _ uuid.UUID, _ *string, _ *string) error {
	return nil
}
func (r *authzInstanceRepo) SetWebhookReceived(_ context.Context, _ uuid.UUID, _ time.Time) error {
	return nil
}
func (r *authzInstanceRepo) Stats(_ context.Context, _ string, _, _ time.Time) (*instance.WorkflowStats, error) {
	return nil, nil
}

var _ instance.InstanceRepository = (*authzInstanceRepo)(nil)

// authzNodeExecRepo is a no-op node execution repo for tests that exercise the
// authz gate. Once the gate passes, the engine calls ListByInstance — returning
// an empty slice is correct and avoids a nil-pointer panic.
type authzNodeExecRepo struct{}

func (r *authzNodeExecRepo) Create(_ context.Context, _ *instance.NodeExecution) error { return nil }
func (r *authzNodeExecRepo) Update(_ context.Context, _ *instance.NodeExecution) error { return nil }
func (r *authzNodeExecRepo) ListByInstance(_ context.Context, _ uuid.UUID) ([]instance.NodeExecution, error) {
	return []instance.NodeExecution{}, nil
}
func (r *authzNodeExecRepo) GetByInstanceAndNode(_ context.Context, _ uuid.UUID, _ string) (*instance.NodeExecution, error) {
	return nil, instance.ErrNotFound
}

var _ instance.NodeExecutionRepository = (*authzNodeExecRepo)(nil)

// newAuthzEngine builds a real engine with the given instance repo and minimal
// no-op deps. The authz gate runs before sub-resource repos are touched, so
// for cross-user requests the sub-resource repos are never called.
func newAuthzEngine(repo *authzInstanceRepo) *engine.Engine {
	return engine.NewEngine(
		nil, repo, &authzNodeExecRepo{}, nil, nil,
		map[workflow.NodeType]engine.NodeExecutor{},
		nil, nil, nil,
	)
}

// newAuthzMCPServer builds an MCP server wired to the real engine.
func newAuthzMCPServer(repo *authzInstanceRepo) *adaptmcp.Server {
	eng := newAuthzEngine(repo)
	deps := &adaptmcp.Deps{Engine: eng, Instances: repo}
	srv := adaptmcp.NewServerWithDeps("test-authz", "0.0.0", deps)
	adaptmcp.RegisterInstanceTools(srv)
	adaptmcp.RegisterTraceTools(srv)
	return srv
}

// callToolCtx calls an MCP tool with the given context (which carries identity).
func callToolCtx(t *testing.T, srv *adaptmcp.Server, ctx context.Context, name string, args map[string]any) *gomcp.CallToolResult {
	t.Helper()
	req := gomcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args

	result := srv.MCPServer().HandleMessage(ctx, mustMarshal(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  req.Params,
	}))

	data, err := json.Marshal(result)
	require.NoError(t, err)
	var resp struct {
		Result gomcp.CallToolResult `json:"result"`
	}
	require.NoError(t, json.Unmarshal(data, &resp))
	return &resp.Result
}

func nonAdminIdentityCtx(userID uuid.UUID) context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{
		UserID:   userID,
		Username: "alice",
		Role:     identity.RoleUser,
	})
}

func adminIdentityCtx(userID uuid.UUID) context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{
		UserID:   userID,
		Username: "admin",
		Role:     identity.RoleAdmin,
	})
}

func newTestInstance(ownerID uuid.UUID, wfName string) *instance.Instance {
	return &instance.Instance{
		ID:           uuid.New(),
		OwnerID:      ownerID,
		WorkflowName: wfName,
		State:        instance.StateRunning,
		Variables:    map[string]any{"k": "v"},
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
}

// ---------------------------------------------------------------------------
// instance_get — non-admin cannot read another user's instance
// ---------------------------------------------------------------------------

func TestMCPAuthzE2E_InstanceGet(t *testing.T) {
	ownerA := uuid.New()
	ownerB := uuid.New()
	instA := newTestInstance(ownerA, "wf-a")

	repo := newAuthzInstanceRepo(instA)
	srv := newAuthzMCPServer(repo)

	t.Run("owner can get their own instance", func(t *testing.T) {
		result := callToolCtx(t, srv, nonAdminIdentityCtx(ownerA), "instance_get", map[string]any{"id": instA.ID.String()})
		assert.False(t, result.IsError, "owner must be able to read their own instance")
	})

	t.Run("non-owner non-admin gets tool error (existence not leaked)", func(t *testing.T) {
		result := callToolCtx(t, srv, nonAdminIdentityCtx(ownerB), "instance_get", map[string]any{"id": instA.ID.String()})
		assert.True(t, result.IsError, "non-owner must receive tool error")
	})

	t.Run("admin can get any instance", func(t *testing.T) {
		result := callToolCtx(t, srv, adminIdentityCtx(uuid.New()), "instance_get", map[string]any{"id": instA.ID.String()})
		assert.False(t, result.IsError, "admin must be able to read any instance")
	})
}

// ---------------------------------------------------------------------------
// instance_pause — non-admin cannot intervene on another user's instance
// ---------------------------------------------------------------------------

func TestMCPAuthzE2E_InstancePause(t *testing.T) {
	ownerA := uuid.New()
	ownerB := uuid.New()
	instA := newTestInstance(ownerA, "wf-a")

	repo := newAuthzInstanceRepo(instA)
	srv := newAuthzMCPServer(repo)

	t.Run("non-owner non-admin gets tool error for pause", func(t *testing.T) {
		result := callToolCtx(t, srv, nonAdminIdentityCtx(ownerB), "instance_pause", map[string]any{"id": instA.ID.String()})
		assert.True(t, result.IsError, "non-owner must not be able to pause another user's instance")
	})
}

// ---------------------------------------------------------------------------
// trace_get — non-admin cannot read another user's trace
// ---------------------------------------------------------------------------

func TestMCPAuthzE2E_TraceGet(t *testing.T) {
	ownerA := uuid.New()
	ownerB := uuid.New()
	instA := newTestInstance(ownerA, "wf-a")

	repo := newAuthzInstanceRepo(instA)
	srv := newAuthzMCPServer(repo)

	t.Run("owner can read their own trace", func(t *testing.T) {
		result := callToolCtx(t, srv, nonAdminIdentityCtx(ownerA), "trace_get", map[string]any{"id": instA.ID.String()})
		assert.False(t, result.IsError, "owner must be able to read their own trace")
	})

	t.Run("non-owner gets tool error (existence not leaked)", func(t *testing.T) {
		result := callToolCtx(t, srv, nonAdminIdentityCtx(ownerB), "trace_get", map[string]any{"id": instA.ID.String()})
		assert.True(t, result.IsError, "non-owner must receive tool error for trace read")
	})

	t.Run("admin can read any trace", func(t *testing.T) {
		result := callToolCtx(t, srv, adminIdentityCtx(uuid.New()), "trace_get", map[string]any{"id": instA.ID.String()})
		assert.False(t, result.IsError, "admin must be able to read any trace")
	})
}

// ---------------------------------------------------------------------------
// variables_get — non-admin cannot read another user's variables
// ---------------------------------------------------------------------------

func TestMCPAuthzE2E_VariablesGet(t *testing.T) {
	ownerA := uuid.New()
	ownerB := uuid.New()
	instA := newTestInstance(ownerA, "wf-a")

	repo := newAuthzInstanceRepo(instA)
	srv := newAuthzMCPServer(repo)

	t.Run("owner can read their own variables", func(t *testing.T) {
		result := callToolCtx(t, srv, nonAdminIdentityCtx(ownerA), "variables_get", map[string]any{"id": instA.ID.String()})
		assert.False(t, result.IsError, "owner must be able to read their own variables")
	})

	t.Run("non-owner gets tool error (existence not leaked)", func(t *testing.T) {
		result := callToolCtx(t, srv, nonAdminIdentityCtx(ownerB), "variables_get", map[string]any{"id": instA.ID.String()})
		assert.True(t, result.IsError, "non-owner must not see another user's variables")
	})

	t.Run("admin can read any variables", func(t *testing.T) {
		result := callToolCtx(t, srv, adminIdentityCtx(uuid.New()), "variables_get", map[string]any{"id": instA.ID.String()})
		assert.False(t, result.IsError, "admin must be able to read any variables")
	})
}

// ---------------------------------------------------------------------------
// instance_list — non-admin sees only their own instances
// ---------------------------------------------------------------------------

func TestMCPAuthzE2E_InstanceList(t *testing.T) {
	ownerA := uuid.New()
	ownerB := uuid.New()
	instA := newTestInstance(ownerA, "wf-a")
	instB := newTestInstance(ownerB, "wf-b")

	repo := newAuthzInstanceRepo(instA, instB)
	srv := newAuthzMCPServer(repo)

	t.Run("non-admin sees only own instances", func(t *testing.T) {
		result := callToolCtx(t, srv, nonAdminIdentityCtx(ownerA), "instance_list", nil)
		require.False(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, instA.ID.String(), "ownerA must see their own instance")
		assert.NotContains(t, text, instB.ID.String(), "ownerA must not see ownerB's instance")
	})

	t.Run("admin sees all instances", func(t *testing.T) {
		result := callToolCtx(t, srv, adminIdentityCtx(uuid.New()), "instance_list", nil)
		require.False(t, result.IsError)
		text := extractText(t, result)
		assert.Contains(t, text, instA.ID.String())
		assert.Contains(t, text, instB.ID.String())
	})
}
