package mcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
)

// parityInstanceRepo is an in-memory InstanceRepository whose List honors the
// OwnerID filter, so the real engine's scoping can be exercised end-to-end.
type parityInstanceRepo struct {
	instances map[uuid.UUID]*instance.Instance
}

func (r *parityInstanceRepo) List(_ context.Context, f instance.ListFilters) ([]instance.InstanceSummary, error) {
	var out []instance.InstanceSummary
	for _, inst := range r.instances {
		if f.OwnerID != nil && inst.OwnerID != *f.OwnerID {
			continue
		}
		out = append(out, instance.InstanceSummary{ID: inst.ID, WorkflowName: inst.WorkflowName, State: inst.State})
	}
	return out, nil
}

func (r *parityInstanceRepo) Get(_ context.Context, id uuid.UUID) (*instance.Instance, error) {
	inst, ok := r.instances[id]
	if !ok {
		return nil, instance.ErrNotFound
	}
	return inst, nil
}
func (r *parityInstanceRepo) Create(context.Context, *instance.Instance) error { return nil }
func (r *parityInstanceRepo) UpdateState(context.Context, uuid.UUID, instance.State) error {
	return nil
}
func (r *parityInstanceRepo) UpdateVariables(context.Context, uuid.UUID, map[string]any) error {
	return nil
}
func (r *parityInstanceRepo) UpdateCurrentNode(context.Context, uuid.UUID, string) error { return nil }
func (r *parityInstanceRepo) SetFailed(context.Context, uuid.UUID, string) error         { return nil }
func (r *parityInstanceRepo) SetCompleted(context.Context, uuid.UUID) error              { return nil }
func (r *parityInstanceRepo) ListRunning(context.Context) ([]instance.Instance, error) {
	return nil, nil
}
func (r *parityInstanceRepo) UpdatePriority(context.Context, uuid.UUID, instance.Priority) error {
	return nil
}
func (r *parityInstanceRepo) SetWaiting(context.Context, uuid.UUID, *time.Time) error { return nil }
func (r *parityInstanceRepo) SetWebhookFields(context.Context, uuid.UUID, *string, *string) error {
	return nil
}
func (r *parityInstanceRepo) SetWebhookReceived(context.Context, uuid.UUID, time.Time) error {
	return nil
}
func (r *parityInstanceRepo) Stats(context.Context, string, time.Time, time.Time) (*instance.WorkflowStats, error) {
	return nil, nil
}

var _ instance.InstanceRepository = (*parityInstanceRepo)(nil)

// TestMCPRestParity_InstanceList asserts that for the same non-admin identity,
// an MCP instance_list call returns the same scoped set as the REST
// GET /api/v1/instances endpoint, proving both surfaces inherit the domain
// service scoping (Tasks 1-2) rather than re-implementing it.
func TestMCPRestParity_InstanceList(t *testing.T) {
	ownerA := uuid.New()
	ownerB := uuid.New()
	instA := &instance.Instance{ID: uuid.New(), OwnerID: ownerA, WorkflowName: "wf-a", State: instance.StateRunning}
	instB := &instance.Instance{ID: uuid.New(), OwnerID: ownerB, WorkflowName: "wf-b", State: instance.StateRunning}
	repo := &parityInstanceRepo{instances: map[uuid.UUID]*instance.Instance{instA.ID: instA, instB.ID: instB}}

	// A real engine whose ListInstances does the scoping. The other deps are nil
	// because only ListInstances is exercised.
	eng := engine.NewEngine(nil, repo, nil, nil, nil, map[workflow.NodeType]engine.NodeExecutor{}, nil, nil, nil, "")

	idCtx := identity.WithIdentity(context.Background(), identity.Identity{UserID: ownerA, Username: "a", Role: identity.RoleUser})

	// --- MCP surface ---
	deps := &adaptmcp.Deps{Engine: eng, Instances: repo}
	srv := adaptmcp.NewServerWithDeps("test", "0.0.0", deps)
	adaptmcp.RegisterInstanceTools(srv)
	mcpSummaries := callListTool(t, srv, idCtx)

	// --- REST surface ---
	restSummaries := callListREST(t, eng, repo, idCtx)

	// Both must be scoped to ownerA only (instA), never instB.
	require.Len(t, mcpSummaries, 1)
	require.Len(t, restSummaries, 1)
	assert.Equal(t, instA.ID.String(), mcpSummaries[0]["id"])
	assert.Equal(t, instA.ID.String(), restSummaries[0]["id"])

	// The two surfaces serialize the enriched summary differently (MCP marshals
	// the domain struct with PascalCase keys; REST maps to a snake_case DTO with
	// a flattened submitter and a per-user "dismissed" flag). Parity is about the
	// domain-scoped set, not the JSON shape, so compare the scoping-relevant fields
	// (id, workflowName, state) that both surfaces carry with identical keys.
	for _, s := range restSummaries {
		delete(s, "dismissed")
	}
	assert.Equal(t, mcpSummaries[0]["id"], restSummaries[0]["id"], "MCP and REST return the same scoped instance id")
	assert.Equal(t, mcpSummaries[0]["workflowName"], restSummaries[0]["workflowName"], "workflowName parity")
	assert.Equal(t, mcpSummaries[0]["state"], restSummaries[0]["state"], "state parity")
}

func callListTool(t *testing.T, srv *adaptmcp.Server, ctx context.Context) []map[string]any {
	t.Helper()
	req := gomcp.CallToolRequest{}
	req.Params.Name = "instance_list"
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
	require.False(t, resp.Result.IsError)
	var summaries []map[string]any
	require.NoError(t, json.Unmarshal([]byte(extractText(t, &resp.Result)), &summaries))
	return summaries
}

// TestMCPRestParity_InstanceGetNonOwner pins the FA-33 read-scoping invariant:
// when a non-owner, non-admin calls GetInstance, both the MCP tool (instance_get)
// and the REST endpoint (GET /instances/{id}) must return the identical not-found
// response — neither surface leaks that the instance exists.
func TestMCPRestParity_InstanceGetNonOwner(t *testing.T) {
	owner := uuid.New()
	nonOwner := uuid.New()
	inst := &instance.Instance{ID: uuid.New(), OwnerID: owner, WorkflowName: "wf-owner", State: instance.StateRunning}
	repo := &parityInstanceRepo{instances: map[uuid.UUID]*instance.Instance{inst.ID: inst}}

	// Real engine: GetInstance enforces canView(ctx, inst.OwnerID).
	eng := engine.NewEngine(nil, repo, nil, nil, nil, map[workflow.NodeType]engine.NodeExecutor{}, nil, nil, nil, "")

	nonOwnerCtx := identity.WithIdentity(context.Background(), identity.Identity{
		UserID: nonOwner, Username: "b", Role: identity.RoleUser,
	})

	// --- MCP surface ---
	deps := &adaptmcp.Deps{Engine: eng, Instances: repo}
	srv := adaptmcp.NewServerWithDeps("test", "0.0.0", deps)
	adaptmcp.RegisterInstanceTools(srv)
	req := gomcp.CallToolRequest{}
	req.Params.Name = "instance_get"
	req.Params.Arguments = map[string]any{"id": inst.ID.String()}
	result := srv.MCPServer().HandleMessage(nonOwnerCtx, mustMarshal(t, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": req.Params,
	}))
	data, _ := json.Marshal(result)
	var mcpResp struct {
		Result gomcp.CallToolResult `json:"result"`
	}
	require.NoError(t, json.Unmarshal(data, &mcpResp))
	assert.True(t, mcpResp.Result.IsError, "non-owner must get a tool error on instance_get")
	assert.Contains(t, extractText(t, &mcpResp.Result), "not found",
		"MCP non-owner error must be 'not found', not a different message that leaks existence")

	// --- REST surface ---
	h := adapthttp.NewInstanceHandler(repo, nil, nil, eng)
	r := chi.NewRouter()
	r.Get("/api/v1/instances/{id}", func(w http.ResponseWriter, req *http.Request) {
		// Inject identity into the chi request context (preserves URL params).
		ctx := identity.WithIdentity(req.Context(), identity.Identity{
			UserID: nonOwner, Username: "b", Role: identity.RoleUser,
		})
		h.Get(w, req.WithContext(ctx))
	})
	restReq := httptest.NewRequest(http.MethodGet, "/api/v1/instances/"+inst.ID.String(), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, restReq)
	assert.Equal(t, http.StatusNotFound, w.Code,
		"REST non-owner must get 404, not 403 (existence must not be leaked)")
	var restErrResp adapthttp.ErrorResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&restErrResp))
	assert.Equal(t, "NOT_FOUND", restErrResp.Code,
		"REST non-owner error code must be NOT_FOUND, identical to a missing instance")
}

func callListREST(t *testing.T, eng *engine.Engine, repo instance.InstanceRepository, ctx context.Context) []map[string]any {
	t.Helper()
	h := adapthttp.NewInstanceHandler(repo, nil, nil, eng)
	r := chi.NewRouter()
	r.Get("/api/v1/instances", func(w http.ResponseWriter, req *http.Request) {
		h.List(w, req.WithContext(ctx))
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	var summaries []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&summaries))
	return summaries
}
