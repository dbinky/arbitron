package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
)

// fakeGraphStateService answers per instance id (unknown id => ErrNotFound,
// exactly like the reader) and records the identity each call carried.
type fakeGraphStateService struct {
	mu      sync.Mutex
	states  map[uuid.UUID]*engine.GraphState
	errs    map[uuid.UUID]error
	callers []uuid.UUID
}

func newFakeGraphStateService() *fakeGraphStateService {
	return &fakeGraphStateService{states: map[uuid.UUID]*engine.GraphState{}, errs: map[uuid.UUID]error{}}
}

func (f *fakeGraphStateService) GraphState(ctx context.Context, id uuid.UUID) (*engine.GraphState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if who, ok := identity.FromContext(ctx); ok {
		f.callers = append(f.callers, who.UserID)
	}
	if err := f.errs[id]; err != nil {
		return nil, err
	}
	gs, ok := f.states[id]
	if !ok {
		return nil, fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
	}
	return gs, nil
}

func sampleGraphState(root, child uuid.UUID) *engine.GraphState {
	runID := uuid.New()
	return &engine.GraphState{
		RootInstanceID: root, Version: "0123456789abcdef0123456789abcdef", Active: true,
		Contexts: []engine.ExecutionContext{
			{InstanceID: root, Kind: engine.ContextKindRoot, State: "waiting", Graph: map[string]any{"name": "fanout"},
				Nodes: []engine.LiveNodeState{{InstanceID: root, NodeID: "g", State: engine.LiveStateWaiting, Reason: "waiting for parallel group"}}},
			{InstanceID: child, ParentInstanceID: &root, GroupRunID: &runID, TaskKey: "a", Kind: engine.ContextKindBranch,
				State: "running", Blocking: true,
				Nodes: []engine.LiveNodeState{{InstanceID: child, NodeID: "a1", State: engine.LiveStateRunning}}},
		},
		Groups: []engine.GroupPanel{{
			GroupRunID: runID, InstanceID: root, NodeID: "g", Form: workflow.GroupFormBranches, State: "running",
			Tasks:    []engine.GroupPanelTask{{Key: "a", Kind: "branch", Origin: "a1", ChildInstanceID: &child, LiveStatus: "running", Blocking: true}},
			Progress: engine.GroupProgress{Total: 1, Active: 1}, WaitingReasons: []string{"waiting_for_tasks:a"},
		}},
	}
}

func graphStateRequest(h *adapthttp.InstanceHandler, id string, who *identity.Identity) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/instances/"+id+"/graph-state", nil)
	if who != nil {
		req = req.WithContext(identity.WithIdentity(req.Context(), *who))
	}
	req = withChiURLParam(req, "id", id)
	w := httptest.NewRecorder()
	h.GraphState(w, req)
	return w
}

func TestInstanceHandler_GraphState_ReturnsAggregateJSON(t *testing.T) {
	h, _, _, _, _ := newInstanceHandler()
	fake := newFakeGraphStateService()
	root, child := uuid.New(), uuid.New()
	fake.states[root] = sampleGraphState(root, child)
	h.SetGraphState(fake)

	w := graphStateRequest(h, root.String(), nil)

	require.Equal(t, http.StatusOK, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, root.String(), body["rootInstanceId"])
	assert.Equal(t, "0123456789abcdef0123456789abcdef", body["version"])
	assert.Equal(t, true, body["active"])
	ctxs := body["contexts"].([]any)
	require.Len(t, ctxs, 2)
	node := ctxs[1].(map[string]any)["nodes"].([]any)[0].(map[string]any)
	assert.Equal(t, child.String(), node["instanceId"], "nodes are namespaced by instance on the wire")
	assert.Equal(t, "a1", node["nodeId"])
	groups := body["groups"].([]any)
	require.Len(t, groups, 1)
	assert.Equal(t, []any{"waiting_for_tasks:a"}, groups[0].(map[string]any)["waitingReasons"])
}

// The graph-state endpoint must reveal exactly what GET /instances/{id} reveals
// for the same service error — in particular an unauthorized root is a 404
// indistinguishable from an absent one.
func TestInstanceHandler_GraphState_ErrorParityWithGet(t *testing.T) {
	for name, svcErr := range map[string]error{
		"not found / not visible": fmt.Errorf("instance x: %w", instance.ErrNotFound),
		"forbidden":               fmt.Errorf("instance x: %w", instance.ErrForbidden),
		"infrastructure":          errors.New("database is locked"),
	} {
		t.Run(name, func(t *testing.T) {
			id := uuid.New()
			h, _, _, _, svc := newInstanceHandler()
			svc.getFn = func(context.Context, uuid.UUID) (*instance.Instance, error) { return nil, svcErr }
			fake := newFakeGraphStateService()
			fake.errs[id] = svcErr
			h.SetGraphState(fake)

			getReq := withChiURLParam(httptest.NewRequest(http.MethodGet, "/instances/"+id.String(), nil), "id", id.String())
			getW := httptest.NewRecorder()
			h.Get(getW, getReq)
			gsW := graphStateRequest(h, id.String(), nil)

			assert.Equal(t, getW.Code, gsW.Code)
			var getBody, gsBody map[string]any
			require.NoError(t, json.Unmarshal(getW.Body.Bytes(), &getBody))
			require.NoError(t, json.Unmarshal(gsW.Body.Bytes(), &gsBody))
			assert.Equal(t, getBody["code"], gsBody["code"])
			assert.Equal(t, getBody["error"], gsBody["error"], "same message (ErrorResponse.Error), no existence leak")
			assert.NotContains(t, gsW.Body.String(), "database is locked", "internal details stay server-side")
		})
	}
}

func TestInstanceHandler_GraphState_InvalidID(t *testing.T) {
	h, _, _, _, _ := newInstanceHandler()
	h.SetGraphState(newFakeGraphStateService())

	w := graphStateRequest(h, "not-a-uuid", nil)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "BAD_REQUEST")
}

func TestInstanceHandler_GraphState_PassesRequestIdentity(t *testing.T) {
	h, _, _, _, _ := newInstanceHandler()
	fake := newFakeGraphStateService()
	root := uuid.New()
	fake.states[root] = sampleGraphState(root, uuid.New())
	h.SetGraphState(fake)
	who := identity.Identity{UserID: uuid.New(), Username: "viewer", Role: identity.RoleUser}

	w := graphStateRequest(h, root.String(), &who)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, []uuid.UUID{who.UserID}, fake.callers, "authorization runs under the caller's identity")
}

func TestInstanceHandler_GraphState_UnwiredIsNotImplemented(t *testing.T) {
	h, _, _, _, _ := newInstanceHandler()

	w := graphStateRequest(h, uuid.New().String(), nil)

	assert.Equal(t, http.StatusNotImplemented, w.Code)
}

func TestRouter_GraphStateRoute_RegisteredOnlyWhenReaderWired(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()
	root := uuid.New()
	fake := newFakeGraphStateService()
	fake.states[root] = sampleGraphState(root, uuid.New())

	wired := adapthttp.NewRouterFromDeps(adapthttp.RouterDeps{
		Service: svc, Workflows: wfRepo, Instances: instRepo, NodeExecs: nodeExecRepo,
		Snapshots: snapRepo, FileReader: fileReader, EventBus: bus, Config: cfg,
		QueueManager: qm, QueueRepo: qr, GraphState: fake,
	})
	w := httptest.NewRecorder()
	authedRouter(wired).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/instances/"+root.String()+"/graph-state", nil))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, []uuid.UUID{routerTestUserID}, fake.callers)

	unwired := adapthttp.NewRouterFromDeps(adapthttp.RouterDeps{
		Service: svc, Workflows: wfRepo, Instances: instRepo, NodeExecs: nodeExecRepo,
		Snapshots: snapRepo, FileReader: fileReader, EventBus: bus, Config: cfg,
		QueueManager: qm, QueueRepo: qr,
	})
	w = httptest.NewRecorder()
	authedRouter(unwired).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/instances/"+root.String()+"/graph-state", nil))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestRouter_GroupLayoutRouteRegistered(t *testing.T) {
	wfRepo, instRepo, nodeExecRepo, snapRepo, svc, fileReader, bus, cfg, qm, qr := newRouterTestDeps()
	wfRepo.workflows["fanout"] = groupLayoutWorkflow()
	router := adapthttp.NewRouterFromDeps(adapthttp.RouterDeps{
		Service: svc, Workflows: wfRepo, Instances: instRepo, NodeExecs: nodeExecRepo,
		Snapshots: snapRepo, FileReader: fileReader, EventBus: bus, Config: cfg,
		QueueManager: qm, QueueRepo: qr,
	})
	w := httptest.NewRecorder()
	authedRouter(router).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/workflows/fanout/group-layout", nil))

	require.Equal(t, http.StatusOK, w.Code)
	var g workflow.GroupGraph
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &g))
	assert.Contains(t, g.Groups, "g")
}
