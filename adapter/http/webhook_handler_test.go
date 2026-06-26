package http_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/arbitron/arbitron/domain/instance"
)

// ---------------------------------------------------------------------------
// Mocks for webhook handler tests
// ---------------------------------------------------------------------------

type mockWebhookInstanceRepo struct {
	mu                    sync.Mutex
	instances             map[uuid.UUID]*instance.Instance
	setWebhookReceivedErr error
}

func newMockWebhookInstanceRepo() *mockWebhookInstanceRepo {
	return &mockWebhookInstanceRepo{instances: make(map[uuid.UUID]*instance.Instance)}
}

func (r *mockWebhookInstanceRepo) Create(_ context.Context, inst *instance.Instance) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.instances[inst.ID] = inst
	return nil
}

func (r *mockWebhookInstanceRepo) Get(_ context.Context, id uuid.UUID) (*instance.Instance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	inst, ok := r.instances[id]
	if !ok {
		return nil, fmt.Errorf("instance not found: %s", id)
	}
	cp := *inst
	return &cp, nil
}

func (r *mockWebhookInstanceRepo) List(_ context.Context, _ instance.ListFilters) ([]instance.InstanceSummary, error) {
	return nil, nil
}
func (r *mockWebhookInstanceRepo) UpdateState(_ context.Context, _ uuid.UUID, _ instance.State) error {
	return nil
}
func (r *mockWebhookInstanceRepo) UpdateVariables(_ context.Context, _ uuid.UUID, _ map[string]any) error {
	return nil
}
func (r *mockWebhookInstanceRepo) UpdateCurrentNode(_ context.Context, _ uuid.UUID, _ string) error {
	return nil
}
func (r *mockWebhookInstanceRepo) SetFailed(_ context.Context, _ uuid.UUID, _ string) error {
	return nil
}
func (r *mockWebhookInstanceRepo) SetCompleted(_ context.Context, _ uuid.UUID) error { return nil }
func (r *mockWebhookInstanceRepo) ListRunning(_ context.Context) ([]instance.Instance, error) {
	return nil, nil
}
func (r *mockWebhookInstanceRepo) UpdatePriority(_ context.Context, _ uuid.UUID, _ instance.Priority) error {
	return nil
}
func (r *mockWebhookInstanceRepo) SetWaiting(_ context.Context, _ uuid.UUID, _ *time.Time) error {
	return nil
}
func (r *mockWebhookInstanceRepo) SetWebhookFields(_ context.Context, _ uuid.UUID, _ *string, _ *string) error {
	return nil
}
func (r *mockWebhookInstanceRepo) SetWebhookReceived(_ context.Context, id uuid.UUID, receivedAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.setWebhookReceivedErr != nil {
		return r.setWebhookReceivedErr
	}
	inst, ok := r.instances[id]
	if !ok {
		return fmt.Errorf("instance not found: %s", id)
	}
	inst.WebhookReceivedAt = &receivedAt
	return nil
}

func (r *mockWebhookInstanceRepo) Stats(_ context.Context, _ string, _ time.Time, _ time.Time) (*instance.WorkflowStats, error) {
	return &instance.WorkflowStats{}, nil
}

type mockWebhookNodeExecRepo struct {
	mu         sync.Mutex
	executions map[string]*instance.NodeExecution // keyed by instanceID+nodeID
}

func newMockWebhookNodeExecRepo() *mockWebhookNodeExecRepo {
	return &mockWebhookNodeExecRepo{executions: make(map[string]*instance.NodeExecution)}
}

func (r *mockWebhookNodeExecRepo) Create(_ context.Context, exec *instance.NodeExecution) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := exec.InstanceID.String() + ":" + exec.NodeID
	r.executions[key] = exec
	return nil
}

func (r *mockWebhookNodeExecRepo) Update(_ context.Context, exec *instance.NodeExecution) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := exec.InstanceID.String() + ":" + exec.NodeID
	r.executions[key] = exec
	return nil
}

func (r *mockWebhookNodeExecRepo) ListByInstance(_ context.Context, instanceID uuid.UUID) ([]instance.NodeExecution, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []instance.NodeExecution
	for _, e := range r.executions {
		if e.InstanceID == instanceID {
			result = append(result, *e)
		}
	}
	return result, nil
}

func (r *mockWebhookNodeExecRepo) GetByInstanceAndNode(_ context.Context, instanceID uuid.UUID, nodeID string) (*instance.NodeExecution, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := instanceID.String() + ":" + nodeID
	exec, ok := r.executions[key]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	return exec, nil
}

func (r *mockWebhookNodeExecRepo) AggregateByInstances(_ context.Context, _ []uuid.UUID) (map[uuid.UUID]instance.NodeExecutionAggregate, error) {
	return map[uuid.UUID]instance.NodeExecutionAggregate{}, nil
}

type mockWebhookScheduler struct {
	mu          sync.Mutex
	cancelCalls []uuid.UUID
}

func (s *mockWebhookScheduler) Cancel(instanceID uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelCalls = append(s.cancelCalls, instanceID)
}

type mockWebhookQueueRepo struct {
	mu             sync.Mutex
	setQueuedCalls []uuid.UUID
}

func (r *mockWebhookQueueRepo) CountRunning(_ context.Context) (int, error) { return 0, nil }
func (r *mockWebhookQueueRepo) CountRunningByOwner(_ context.Context, _ uuid.UUID) (int, error) {
	return 0, nil
}
func (r *mockWebhookQueueRepo) CountQueued(_ context.Context) (int, error)  { return 0, nil }
func (r *mockWebhookQueueRepo) CountWaiting(_ context.Context) (int, error) { return 0, nil }
func (r *mockWebhookQueueRepo) NextEligible(_ context.Context, _ int, _ int) (*instance.Instance, error) {
	return nil, nil
}
func (r *mockWebhookQueueRepo) SetQueued(_ context.Context, id uuid.UUID, _ time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setQueuedCalls = append(r.setQueuedCalls, id)
	return nil
}
func (r *mockWebhookQueueRepo) ListWaitingWithTimer(_ context.Context) ([]instance.Instance, error) {
	return nil, nil
}
func (r *mockWebhookQueueRepo) ListWaitingWithWebhook(_ context.Context) ([]instance.Instance, error) {
	return nil, nil
}
func (r *mockWebhookQueueRepo) ListQueued(_ context.Context) ([]instance.Instance, error) {
	return nil, nil
}

type mockWebhookQueueNotifier struct {
	mu    sync.Mutex
	calls int
}

func (n *mockWebhookQueueNotifier) NotifyCapacityFreed(_ context.Context) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls++
}

// ---------------------------------------------------------------------------
// Helper to create a test request against the handler
// ---------------------------------------------------------------------------

func makeWebhookRequest(handler *adapthttp.WebhookHandler, token string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/wait/"+token, bytes.NewReader(body))
	} else {
		req = httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/wait/"+token, nil)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	// Use chi router context for URL params.
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("token", token)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	handler.ReceiveWebhook(rr, req)
	return rr
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestWebhookHandler_ValidPOST_JSONBody_200(t *testing.T) {
	registry := adapthttp.NewWebhookRegistry()
	instRepo := newMockWebhookInstanceRepo()
	nodeExecRepo := newMockWebhookNodeExecRepo()
	scheduler := &mockWebhookScheduler{}
	queueRepo := &mockWebhookQueueRepo{}
	queueMgr := &mockWebhookQueueNotifier{}

	instanceID := uuid.New()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateWaiting,
	}

	token := "test-token-123"
	registry.Register(token, instanceID, "webhook-node", nil)

	// Create node execution record.
	nodeExecRepo.Create(context.Background(), &instance.NodeExecution{
		ID:         uuid.New(),
		InstanceID: instanceID,
		NodeID:     "webhook-node",
		Status:     instance.NodeStatusSucceeded,
		StartedAt:  time.Now(),
	})

	handler := adapthttp.NewWebhookHandler(registry, instRepo, nodeExecRepo, scheduler, queueRepo, queueMgr)

	payload := map[string]any{"key": "value"}
	body, _ := json.Marshal(payload)
	rr := makeWebhookRequest(handler, token, body, nil)

	assert.Equal(t, http.StatusOK, rr.Code)

	// Verify payload stored.
	exec, _ := nodeExecRepo.GetByInstanceAndNode(context.Background(), instanceID, "webhook-node")
	require.NotNil(t, exec)
	assert.Equal(t, "value", exec.Output["key"])

	// Verify instance transitions to queued.
	queueRepo.mu.Lock()
	assert.Contains(t, queueRepo.setQueuedCalls, instanceID)
	queueRepo.mu.Unlock()

	// Verify queue notified.
	queueMgr.mu.Lock()
	assert.Equal(t, 1, queueMgr.calls)
	queueMgr.mu.Unlock()
}

func TestWebhookHandler_ValidPOST_EmptyBody_200(t *testing.T) {
	registry := adapthttp.NewWebhookRegistry()
	instRepo := newMockWebhookInstanceRepo()
	nodeExecRepo := newMockWebhookNodeExecRepo()
	scheduler := &mockWebhookScheduler{}
	queueRepo := &mockWebhookQueueRepo{}
	queueMgr := &mockWebhookQueueNotifier{}

	instanceID := uuid.New()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateWaiting,
	}

	token := "empty-body-token"
	registry.Register(token, instanceID, "webhook-node", nil)

	nodeExecRepo.Create(context.Background(), &instance.NodeExecution{
		ID:         uuid.New(),
		InstanceID: instanceID,
		NodeID:     "webhook-node",
		Status:     instance.NodeStatusSucceeded,
		StartedAt:  time.Now(),
	})

	handler := adapthttp.NewWebhookHandler(registry, instRepo, nodeExecRepo, scheduler, queueRepo, queueMgr)

	rr := makeWebhookRequest(handler, token, nil, nil)
	assert.Equal(t, http.StatusOK, rr.Code)

	// Output should be empty map.
	exec, _ := nodeExecRepo.GetByInstanceAndNode(context.Background(), instanceID, "webhook-node")
	require.NotNil(t, exec)
	assert.Equal(t, map[string]any{}, exec.Output)
}

func TestWebhookHandler_ValidPOST_NonJSONBody_200(t *testing.T) {
	registry := adapthttp.NewWebhookRegistry()
	instRepo := newMockWebhookInstanceRepo()
	nodeExecRepo := newMockWebhookNodeExecRepo()
	scheduler := &mockWebhookScheduler{}
	queueRepo := &mockWebhookQueueRepo{}
	queueMgr := &mockWebhookQueueNotifier{}

	instanceID := uuid.New()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateWaiting,
	}

	token := "text-body-token"
	registry.Register(token, instanceID, "webhook-node", nil)

	nodeExecRepo.Create(context.Background(), &instance.NodeExecution{
		ID:         uuid.New(),
		InstanceID: instanceID,
		NodeID:     "webhook-node",
		Status:     instance.NodeStatusSucceeded,
		StartedAt:  time.Now(),
	})

	handler := adapthttp.NewWebhookHandler(registry, instRepo, nodeExecRepo, scheduler, queueRepo, queueMgr)

	rr := makeWebhookRequest(handler, token, []byte("just plain text"), nil)
	assert.Equal(t, http.StatusOK, rr.Code)

	exec, _ := nodeExecRepo.GetByInstanceAndNode(context.Background(), instanceID, "webhook-node")
	require.NotNil(t, exec)
	assert.Equal(t, map[string]any{}, exec.Output)
}

func TestWebhookHandler_UnknownToken_404(t *testing.T) {
	registry := adapthttp.NewWebhookRegistry()
	instRepo := newMockWebhookInstanceRepo()
	nodeExecRepo := newMockWebhookNodeExecRepo()
	scheduler := &mockWebhookScheduler{}
	queueRepo := &mockWebhookQueueRepo{}
	queueMgr := &mockWebhookQueueNotifier{}

	handler := adapthttp.NewWebhookHandler(registry, instRepo, nodeExecRepo, scheduler, queueRepo, queueMgr)

	rr := makeWebhookRequest(handler, "unknown-token", nil, nil)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestWebhookHandler_InvalidSecret_401(t *testing.T) {
	registry := adapthttp.NewWebhookRegistry()
	instRepo := newMockWebhookInstanceRepo()
	nodeExecRepo := newMockWebhookNodeExecRepo()
	scheduler := &mockWebhookScheduler{}
	queueRepo := &mockWebhookQueueRepo{}
	queueMgr := &mockWebhookQueueNotifier{}

	instanceID := uuid.New()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateWaiting,
	}

	token := "secret-token"
	secretHash := "correct-hash"
	registry.Register(token, instanceID, "webhook-node", &secretHash)

	handler := adapthttp.NewWebhookHandler(registry, instRepo, nodeExecRepo, scheduler, queueRepo, queueMgr)

	rr := makeWebhookRequest(handler, token, nil, map[string]string{"X-Webhook-Secret": "wrong-secret"})
	assert.Equal(t, http.StatusUnauthorized, rr.Code)

	// Token must still be active — failed auth must not consume the one-time token.
	_, ok := registry.Lookup(token)
	assert.True(t, ok, "token should remain active after failed authentication")
}

func TestWebhookHandler_MissingSecretHeader_401(t *testing.T) {
	registry := adapthttp.NewWebhookRegistry()
	instRepo := newMockWebhookInstanceRepo()
	nodeExecRepo := newMockWebhookNodeExecRepo()
	scheduler := &mockWebhookScheduler{}
	queueRepo := &mockWebhookQueueRepo{}
	queueMgr := &mockWebhookQueueNotifier{}

	instanceID := uuid.New()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateWaiting,
	}

	token := "secret-token"
	secretHash := "correct-hash"
	registry.Register(token, instanceID, "webhook-node", &secretHash)

	handler := adapthttp.NewWebhookHandler(registry, instRepo, nodeExecRepo, scheduler, queueRepo, queueMgr)

	rr := makeWebhookRequest(handler, token, nil, nil)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)

	// Token must still be active — failed auth must not consume the one-time token.
	_, ok := registry.Lookup(token)
	assert.True(t, ok, "token should remain active after missing secret header")
}

func TestWebhookHandler_NoSecretConfigured_SkipsValidation(t *testing.T) {
	registry := adapthttp.NewWebhookRegistry()
	instRepo := newMockWebhookInstanceRepo()
	nodeExecRepo := newMockWebhookNodeExecRepo()
	scheduler := &mockWebhookScheduler{}
	queueRepo := &mockWebhookQueueRepo{}
	queueMgr := &mockWebhookQueueNotifier{}

	instanceID := uuid.New()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateWaiting,
	}

	token := "no-secret-token"
	registry.Register(token, instanceID, "webhook-node", nil) // no secret

	nodeExecRepo.Create(context.Background(), &instance.NodeExecution{
		ID:         uuid.New(),
		InstanceID: instanceID,
		NodeID:     "webhook-node",
		Status:     instance.NodeStatusSucceeded,
		StartedAt:  time.Now(),
	})

	handler := adapthttp.NewWebhookHandler(registry, instRepo, nodeExecRepo, scheduler, queueRepo, queueMgr)

	rr := makeWebhookRequest(handler, token, nil, nil)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestWebhookHandler_CancelledInstance_410(t *testing.T) {
	registry := adapthttp.NewWebhookRegistry()
	instRepo := newMockWebhookInstanceRepo()
	nodeExecRepo := newMockWebhookNodeExecRepo()
	scheduler := &mockWebhookScheduler{}
	queueRepo := &mockWebhookQueueRepo{}
	queueMgr := &mockWebhookQueueNotifier{}

	instanceID := uuid.New()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateCancelled,
	}

	token := "cancelled-token"
	registry.Register(token, instanceID, "webhook-node", nil)

	handler := adapthttp.NewWebhookHandler(registry, instRepo, nodeExecRepo, scheduler, queueRepo, queueMgr)

	rr := makeWebhookRequest(handler, token, nil, nil)
	assert.Equal(t, http.StatusGone, rr.Code)
}

func TestWebhookHandler_CompletedInstance_410(t *testing.T) {
	registry := adapthttp.NewWebhookRegistry()
	instRepo := newMockWebhookInstanceRepo()
	nodeExecRepo := newMockWebhookNodeExecRepo()
	scheduler := &mockWebhookScheduler{}
	queueRepo := &mockWebhookQueueRepo{}
	queueMgr := &mockWebhookQueueNotifier{}

	instanceID := uuid.New()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateCompleted,
	}

	token := "completed-token"
	registry.Register(token, instanceID, "webhook-node", nil)

	handler := adapthttp.NewWebhookHandler(registry, instRepo, nodeExecRepo, scheduler, queueRepo, queueMgr)

	rr := makeWebhookRequest(handler, token, nil, nil)
	assert.Equal(t, http.StatusGone, rr.Code)
}

func TestWebhookHandler_PausedInstance_StoresPayload(t *testing.T) {
	registry := adapthttp.NewWebhookRegistry()
	instRepo := newMockWebhookInstanceRepo()
	nodeExecRepo := newMockWebhookNodeExecRepo()
	scheduler := &mockWebhookScheduler{}
	queueRepo := &mockWebhookQueueRepo{}
	queueMgr := &mockWebhookQueueNotifier{}

	instanceID := uuid.New()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StatePaused,
	}

	token := "paused-token"
	registry.Register(token, instanceID, "webhook-node", nil)

	nodeExecRepo.Create(context.Background(), &instance.NodeExecution{
		ID:         uuid.New(),
		InstanceID: instanceID,
		NodeID:     "webhook-node",
		Status:     instance.NodeStatusSucceeded,
		StartedAt:  time.Now(),
	})

	handler := adapthttp.NewWebhookHandler(registry, instRepo, nodeExecRepo, scheduler, queueRepo, queueMgr)

	payload := map[string]any{"data": "hello"}
	body, _ := json.Marshal(payload)
	rr := makeWebhookRequest(handler, token, body, nil)

	assert.Equal(t, http.StatusOK, rr.Code)

	// Payload stored.
	exec, _ := nodeExecRepo.GetByInstanceAndNode(context.Background(), instanceID, "webhook-node")
	require.NotNil(t, exec)
	assert.Equal(t, "hello", exec.Output["data"])

	// Token deactivated.
	_, ok := registry.Lookup(token)
	assert.False(t, ok)

	// No state transition for paused instance.
	queueRepo.mu.Lock()
	assert.NotContains(t, queueRepo.setQueuedCalls, instanceID)
	queueRepo.mu.Unlock()
}

func TestWebhookHandler_DeactivatesToken(t *testing.T) {
	registry := adapthttp.NewWebhookRegistry()
	instRepo := newMockWebhookInstanceRepo()
	nodeExecRepo := newMockWebhookNodeExecRepo()
	scheduler := &mockWebhookScheduler{}
	queueRepo := &mockWebhookQueueRepo{}
	queueMgr := &mockWebhookQueueNotifier{}

	instanceID := uuid.New()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateWaiting,
	}

	token := "deactivate-token"
	registry.Register(token, instanceID, "webhook-node", nil)

	nodeExecRepo.Create(context.Background(), &instance.NodeExecution{
		ID:         uuid.New(),
		InstanceID: instanceID,
		NodeID:     "webhook-node",
		Status:     instance.NodeStatusSucceeded,
		StartedAt:  time.Now(),
	})

	handler := adapthttp.NewWebhookHandler(registry, instRepo, nodeExecRepo, scheduler, queueRepo, queueMgr)

	rr := makeWebhookRequest(handler, token, nil, nil)
	assert.Equal(t, http.StatusOK, rr.Code)

	// Second request should get 404.
	rr2 := makeWebhookRequest(handler, token, nil, nil)
	assert.Equal(t, http.StatusNotFound, rr2.Code)
}

func TestWebhookHandler_CancelsTimeoutTimer(t *testing.T) {
	registry := adapthttp.NewWebhookRegistry()
	instRepo := newMockWebhookInstanceRepo()
	nodeExecRepo := newMockWebhookNodeExecRepo()
	scheduler := &mockWebhookScheduler{}
	queueRepo := &mockWebhookQueueRepo{}
	queueMgr := &mockWebhookQueueNotifier{}

	instanceID := uuid.New()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateWaiting,
	}

	token := "timeout-cancel-token"
	registry.Register(token, instanceID, "webhook-node", nil)

	nodeExecRepo.Create(context.Background(), &instance.NodeExecution{
		ID:         uuid.New(),
		InstanceID: instanceID,
		NodeID:     "webhook-node",
		Status:     instance.NodeStatusSucceeded,
		StartedAt:  time.Now(),
	})

	handler := adapthttp.NewWebhookHandler(registry, instRepo, nodeExecRepo, scheduler, queueRepo, queueMgr)

	makeWebhookRequest(handler, token, nil, nil)

	scheduler.mu.Lock()
	assert.Contains(t, scheduler.cancelCalls, instanceID)
	scheduler.mu.Unlock()
}

func TestWebhookHandler_TriggersQueueEvaluation(t *testing.T) {
	registry := adapthttp.NewWebhookRegistry()
	instRepo := newMockWebhookInstanceRepo()
	nodeExecRepo := newMockWebhookNodeExecRepo()
	scheduler := &mockWebhookScheduler{}
	queueRepo := &mockWebhookQueueRepo{}
	queueMgr := &mockWebhookQueueNotifier{}

	instanceID := uuid.New()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateWaiting,
	}

	token := "queue-eval-token"
	registry.Register(token, instanceID, "webhook-node", nil)

	nodeExecRepo.Create(context.Background(), &instance.NodeExecution{
		ID:         uuid.New(),
		InstanceID: instanceID,
		NodeID:     "webhook-node",
		Status:     instance.NodeStatusSucceeded,
		StartedAt:  time.Now(),
	})

	handler := adapthttp.NewWebhookHandler(registry, instRepo, nodeExecRepo, scheduler, queueRepo, queueMgr)

	makeWebhookRequest(handler, token, nil, nil)

	queueMgr.mu.Lock()
	assert.Equal(t, 1, queueMgr.calls)
	queueMgr.mu.Unlock()
}

func TestWebhookHandler_ValidSecret_200(t *testing.T) {
	// Happy path: correct X-Webhook-Secret header passes HMAC validation.
	registry := adapthttp.NewWebhookRegistry()
	instRepo := newMockWebhookInstanceRepo()
	nodeExecRepo := newMockWebhookNodeExecRepo()
	scheduler := &mockWebhookScheduler{}
	queueRepo := &mockWebhookQueueRepo{}
	queueMgr := &mockWebhookQueueNotifier{}

	instanceID := uuid.New()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateWaiting,
	}

	token := "hmac-test-token"
	secret := "my-secret"
	// Compute the expected hash the same way the executor does: HMAC(key=token, msg=secret).
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte(secret))
	secretHash := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	registry.Register(token, instanceID, "webhook-node", &secretHash)

	nodeExecRepo.Create(context.Background(), &instance.NodeExecution{
		ID:         uuid.New(),
		InstanceID: instanceID,
		NodeID:     "webhook-node",
		Status:     instance.NodeStatusSucceeded,
		StartedAt:  time.Now(),
	})

	handler := adapthttp.NewWebhookHandler(registry, instRepo, nodeExecRepo, scheduler, queueRepo, queueMgr)

	rr := makeWebhookRequest(handler, token, nil, map[string]string{"X-Webhook-Secret": secret})
	assert.Equal(t, http.StatusOK, rr.Code)

	queueRepo.mu.Lock()
	assert.Contains(t, queueRepo.setQueuedCalls, instanceID, "should transition to queued on valid secret")
	queueRepo.mu.Unlock()
}

func TestWebhookHandler_NodeExecNotFound_StillSucceeds(t *testing.T) {
	// If no node execution record exists for this node, the payload update is
	// skipped but the webhook still succeeds (the exec record is supplementary).
	registry := adapthttp.NewWebhookRegistry()
	instRepo := newMockWebhookInstanceRepo()
	nodeExecRepo := newMockWebhookNodeExecRepo()
	scheduler := &mockWebhookScheduler{}
	queueRepo := &mockWebhookQueueRepo{}
	queueMgr := &mockWebhookQueueNotifier{}

	instanceID := uuid.New()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateWaiting,
	}

	token := "no-exec-token"
	registry.Register(token, instanceID, "webhook-node", nil)
	// No node execution record created.

	handler := adapthttp.NewWebhookHandler(registry, instRepo, nodeExecRepo, scheduler, queueRepo, queueMgr)

	rr := makeWebhookRequest(handler, token, []byte(`{"data": "ok"}`), nil)
	assert.Equal(t, http.StatusOK, rr.Code)

	queueRepo.mu.Lock()
	assert.Contains(t, queueRepo.setQueuedCalls, instanceID)
	queueRepo.mu.Unlock()
}

func TestWebhookHandler_SetQueuedError_LogsButReturns200(t *testing.T) {
	// If SetQueued fails, the error is logged but 200 is still returned
	// (the webhook was accepted; the state transition failure is an infra issue).
	registry := adapthttp.NewWebhookRegistry()
	instRepo := newMockWebhookInstanceRepo()
	nodeExecRepo := newMockWebhookNodeExecRepo()
	scheduler := &mockWebhookScheduler{}
	queueMgr := &mockWebhookQueueNotifier{}

	// Use a queue repo that fails on SetQueued.
	failingQueueRepo := &mockFailingQueueRepo{}

	instanceID := uuid.New()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateWaiting,
	}

	token := "set-queued-fail-token"
	registry.Register(token, instanceID, "webhook-node", nil)

	handler := adapthttp.NewWebhookHandler(registry, instRepo, nodeExecRepo, scheduler, failingQueueRepo, queueMgr)

	rr := makeWebhookRequest(handler, token, nil, nil)
	assert.Equal(t, http.StatusOK, rr.Code)

	// Queue NOT notified because SetQueued failed.
	queueMgr.mu.Lock()
	assert.Equal(t, 0, queueMgr.calls)
	queueMgr.mu.Unlock()

	// Token still deactivated (webhook was received).
	_, ok := registry.Lookup(token)
	assert.False(t, ok)
}

// mockFailingQueueRepo fails on SetQueued.
type mockFailingQueueRepo struct{ mockWebhookQueueRepo }

func (r *mockFailingQueueRepo) SetQueued(_ context.Context, _ uuid.UUID, _ time.Time) error {
	return fmt.Errorf("db write failed")
}

func TestWebhookHandler_InstanceGetFails_404(t *testing.T) {
	// Registry lookup succeeds but instances.Get fails (e.g., DB down after registration).
	// Handler must return 404.
	registry := adapthttp.NewWebhookRegistry()
	instRepo := newMockWebhookInstanceRepo() // empty — Get will fail
	nodeExecRepo := newMockWebhookNodeExecRepo()
	scheduler := &mockWebhookScheduler{}
	queueRepo := &mockWebhookQueueRepo{}
	queueMgr := &mockWebhookQueueNotifier{}

	// Register a token for an instance that is NOT in the repo.
	missingID := uuid.New()
	token := "ghost-instance-token"
	registry.Register(token, missingID, "webhook-node", nil)

	handler := adapthttp.NewWebhookHandler(registry, instRepo, nodeExecRepo, scheduler, queueRepo, queueMgr)

	rr := makeWebhookRequest(handler, token, nil, nil)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestWebhookHandler_SetWebhookReceivedFails_StillReturns200(t *testing.T) {
	// SetWebhookReceived failure is logged but the webhook response is still 200
	// (the webhook was accepted; the timestamp is advisory).
	registry := adapthttp.NewWebhookRegistry()
	instRepo := newMockWebhookInstanceRepo()
	instRepo.setWebhookReceivedErr = fmt.Errorf("db write failed")
	nodeExecRepo := newMockWebhookNodeExecRepo()
	scheduler := &mockWebhookScheduler{}
	queueRepo := &mockWebhookQueueRepo{}
	queueMgr := &mockWebhookQueueNotifier{}

	instanceID := uuid.New()
	instRepo.instances[instanceID] = &instance.Instance{
		ID:    instanceID,
		State: instance.StateWaiting,
	}

	token := "recv-fail-token"
	registry.Register(token, instanceID, "webhook-node", nil)

	handler := adapthttp.NewWebhookHandler(registry, instRepo, nodeExecRepo, scheduler, queueRepo, queueMgr)

	rr := makeWebhookRequest(handler, token, nil, nil)
	assert.Equal(t, http.StatusOK, rr.Code)

	// SetQueued should still have been called (state transition proceeds despite SetWebhookReceived failure).
	queueRepo.mu.Lock()
	assert.Contains(t, queueRepo.setQueuedCalls, instanceID)
	queueRepo.mu.Unlock()
}
