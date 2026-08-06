package queue_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/queue"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Mock implementations
// ---------------------------------------------------------------------------

type mockQueueRepo struct {
	mu                     sync.Mutex
	runningCount           int
	ownerCounts            map[uuid.UUID]int
	queuedCount            int
	waitingCount           int
	eligible               []*instance.Instance
	eligibleIdx            int
	setQueuedCalls         []uuid.UUID
	waitingWithTimer       []instance.Instance
	waitingWithWebhook     []instance.Instance
	queuedList             []instance.Instance
	listQueuedErr          error
	listQueuedHook         func() // fired inside ListQueued, before returning (test seam)
	countRunningErr        error
	countRunningByOwnerErr error
	nextEligibleErr        error
	setQueuedErr           error
}

func newMockQueueRepo() *mockQueueRepo {
	return &mockQueueRepo{
		ownerCounts: make(map[uuid.UUID]int),
	}
}

func (r *mockQueueRepo) CountRunning(_ context.Context) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runningCount, r.countRunningErr
}

func (r *mockQueueRepo) CountRunningByOwner(_ context.Context, ownerID uuid.UUID) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ownerCounts[ownerID], r.countRunningByOwnerErr
}

func (r *mockQueueRepo) CountQueued(_ context.Context) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.queuedCount, nil
}

func (r *mockQueueRepo) CountWaiting(_ context.Context) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.waitingCount, nil
}

func (r *mockQueueRepo) NextEligible(_ context.Context, _ int, _ int) (*instance.Instance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.nextEligibleErr != nil {
		return nil, r.nextEligibleErr
	}
	if r.eligibleIdx >= len(r.eligible) {
		return nil, nil
	}
	inst := r.eligible[r.eligibleIdx]
	r.eligibleIdx++
	return inst, nil
}

func (r *mockQueueRepo) SetQueuedIfWaiting(ctx context.Context, id uuid.UUID, at time.Time) (bool, error) {
	if err := r.SetQueued(ctx, id, at); err != nil {
		return false, err
	}
	return true, nil
}

func (r *mockQueueRepo) SetQueued(_ context.Context, id uuid.UUID, _ time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.setQueuedErr != nil {
		return r.setQueuedErr
	}
	r.setQueuedCalls = append(r.setQueuedCalls, id)
	return nil
}

func (r *mockQueueRepo) ListWaitingWithTimer(_ context.Context) ([]instance.Instance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.waitingWithTimer, nil
}

func (r *mockQueueRepo) ListWaitingWithWebhook(_ context.Context) ([]instance.Instance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.waitingWithWebhook, nil
}

func (r *mockQueueRepo) ListQueued(_ context.Context) ([]instance.Instance, error) {
	r.mu.Lock()
	hook := r.listQueuedHook
	list := r.queuedList
	err := r.listQueuedErr
	r.mu.Unlock()
	if hook != nil {
		hook()
	}
	return list, err
}

type mockInstanceRepo struct {
	mu           sync.Mutex
	instances    map[uuid.UUID]*instance.Instance
	setFailedErr error
}

func newMockInstanceRepo() *mockInstanceRepo {
	return &mockInstanceRepo{instances: make(map[uuid.UUID]*instance.Instance)}
}

func (r *mockInstanceRepo) Create(_ context.Context, inst *instance.Instance) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.instances[inst.ID] = inst
	return nil
}

func (r *mockInstanceRepo) Get(_ context.Context, id uuid.UUID) (*instance.Instance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	inst, ok := r.instances[id]
	if !ok {
		return nil, fmt.Errorf("instance not found: %s", id)
	}
	return inst, nil
}

func (r *mockInstanceRepo) List(_ context.Context, _ instance.ListFilters) ([]instance.InstanceSummary, error) {
	return nil, nil
}
func (r *mockInstanceRepo) UpdateState(_ context.Context, _ uuid.UUID, _ instance.State) error {
	return nil
}
func (r *mockInstanceRepo) UpdateVariables(_ context.Context, _ uuid.UUID, _ map[string]any) error {
	return nil
}
func (r *mockInstanceRepo) UpdateCurrentNode(_ context.Context, _ uuid.UUID, _ string) error {
	return nil
}
func (r *mockInstanceRepo) SetFailed(_ context.Context, id uuid.UUID, failedNodeID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.setFailedErr != nil {
		return r.setFailedErr
	}
	if inst, ok := r.instances[id]; ok {
		inst.State = instance.StateFailed
		inst.FailedNodeID = &failedNodeID
	}
	return nil
}
func (r *mockInstanceRepo) SetCompleted(_ context.Context, _ uuid.UUID) error { return nil }
func (r *mockInstanceRepo) ListRunning(_ context.Context) ([]instance.Instance, error) {
	return nil, nil
}
func (r *mockInstanceRepo) UpdatePriority(_ context.Context, _ uuid.UUID, _ instance.Priority) error {
	return nil
}

func (r *mockInstanceRepo) SetWaiting(_ context.Context, id uuid.UUID, waitingUntil *time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	inst, ok := r.instances[id]
	if !ok {
		return fmt.Errorf("instance not found: %s", id)
	}
	if waitingUntil != nil {
		inst.State = instance.StateWaiting
	}
	inst.WaitingUntil = waitingUntil
	return nil
}

func (r *mockInstanceRepo) SetWebhookFields(_ context.Context, id uuid.UUID, token *string, secretHash *string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	inst, ok := r.instances[id]
	if !ok {
		return fmt.Errorf("instance not found: %s", id)
	}
	inst.WebhookToken = token
	inst.WebhookSecretHash = secretHash
	return nil
}

func (r *mockInstanceRepo) SetWebhookReceived(_ context.Context, id uuid.UUID, receivedAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	inst, ok := r.instances[id]
	if !ok {
		return fmt.Errorf("instance not found: %s", id)
	}
	inst.WebhookReceivedAt = &receivedAt
	return nil
}

func (r *mockInstanceRepo) Stats(_ context.Context, _ string, _ time.Time, _ time.Time) (*instance.WorkflowStats, error) {
	return &instance.WorkflowStats{}, nil
}

type mockStarter struct {
	mu        sync.Mutex
	calls     []uuid.UUID
	errOnCall map[uuid.UUID]error
}

func newMockStarter() *mockStarter {
	return &mockStarter{errOnCall: make(map[uuid.UUID]error)}
}

func (s *mockStarter) StartInstance(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, id)
	if err, ok := s.errOnCall[id]; ok {
		return err
	}
	return nil
}

func (s *mockStarter) getCalls() []uuid.UUID {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]uuid.UUID, len(s.calls))
	copy(cp, s.calls)
	return cp
}

// ---------------------------------------------------------------------------
// Tests: CanStart
// ---------------------------------------------------------------------------

func TestCanStart_UnderBothCaps(t *testing.T) {
	qr := newMockQueueRepo()
	qr.runningCount = 2
	ownerID := uuid.New()
	qr.ownerCounts[ownerID] = 1

	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 20, PerUserCap: 5}}
	m := queue.NewManager(qr, newMockInstanceRepo(), cfg, newMockStarter())

	canStart, err := m.CanStart(context.Background(), ownerID)
	require.NoError(t, err)
	assert.True(t, canStart)
}

func TestCanStart_AtGlobalCap(t *testing.T) {
	qr := newMockQueueRepo()
	qr.runningCount = 20

	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 20, PerUserCap: 5}}
	m := queue.NewManager(qr, newMockInstanceRepo(), cfg, newMockStarter())

	canStart, err := m.CanStart(context.Background(), uuid.New())
	require.NoError(t, err)
	assert.False(t, canStart)
}

func TestCanStart_AtPerUserCap(t *testing.T) {
	qr := newMockQueueRepo()
	qr.runningCount = 2
	ownerID := uuid.New()
	qr.ownerCounts[ownerID] = 5

	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 20, PerUserCap: 5}}
	m := queue.NewManager(qr, newMockInstanceRepo(), cfg, newMockStarter())

	canStart, err := m.CanStart(context.Background(), ownerID)
	require.NoError(t, err)
	assert.False(t, canStart)
}

func TestCanStart_GlobalCapUnlimited(t *testing.T) {
	qr := newMockQueueRepo()
	qr.runningCount = 999

	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 0, PerUserCap: 5}}
	m := queue.NewManager(qr, newMockInstanceRepo(), cfg, newMockStarter())

	canStart, err := m.CanStart(context.Background(), uuid.New())
	require.NoError(t, err)
	assert.True(t, canStart)
}

func TestCanStart_PerUserCapUnlimited(t *testing.T) {
	qr := newMockQueueRepo()
	qr.runningCount = 2
	ownerID := uuid.New()
	qr.ownerCounts[ownerID] = 999

	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 20, PerUserCap: 0}}
	m := queue.NewManager(qr, newMockInstanceRepo(), cfg, newMockStarter())

	canStart, err := m.CanStart(context.Background(), ownerID)
	require.NoError(t, err)
	assert.True(t, canStart)
}

// ---------------------------------------------------------------------------
// Tests: Evaluate
// ---------------------------------------------------------------------------

func TestEvaluate_StartsMultipleEligible(t *testing.T) {
	id1 := uuid.New()
	id2 := uuid.New()
	qr := newMockQueueRepo()
	qr.eligible = []*instance.Instance{
		{ID: id1, State: instance.StateQueued},
		{ID: id2, State: instance.StateQueued},
	}

	starter := newMockStarter()
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	m := queue.NewManager(qr, newMockInstanceRepo(), cfg, starter)

	err := m.Evaluate(context.Background())
	require.NoError(t, err)

	calls := starter.getCalls()
	assert.Len(t, calls, 2)
	assert.Equal(t, id1, calls[0])
	assert.Equal(t, id2, calls[1])
}

func TestEvaluate_ReturnsErrorWhenStartFails(t *testing.T) {
	id1 := uuid.New()
	id2 := uuid.New()
	qr := newMockQueueRepo()
	qr.eligible = []*instance.Instance{
		{ID: id1, State: instance.StateQueued},
		{ID: id2, State: instance.StateQueued},
	}

	starter := newMockStarter()
	starter.errOnCall[id1] = fmt.Errorf("start failed")

	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	m := queue.NewManager(qr, newMockInstanceRepo(), cfg, starter)

	err := m.Evaluate(context.Background())
	require.Error(t, err)

	// Only the first instance was attempted; loop stops on error to avoid
	// holding the mutex indefinitely when a persistent failure occurs.
	calls := starter.getCalls()
	assert.Len(t, calls, 1)
	assert.Equal(t, id1, calls[0])
}

func TestEvaluate_NoEligible(t *testing.T) {
	qr := newMockQueueRepo()
	// No eligible instances (empty)

	starter := newMockStarter()
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	m := queue.NewManager(qr, newMockInstanceRepo(), cfg, starter)

	err := m.Evaluate(context.Background())
	require.NoError(t, err)

	calls := starter.getCalls()
	assert.Empty(t, calls)
}

// ---------------------------------------------------------------------------
// Tests: Enqueue
// ---------------------------------------------------------------------------

func TestEnqueue_StartsImmediately(t *testing.T) {
	qr := newMockQueueRepo()
	qr.runningCount = 0

	starter := newMockStarter()
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	m := queue.NewManager(qr, newMockInstanceRepo(), cfg, starter)

	id := uuid.New()
	ownerID := uuid.New()
	started, err := m.Enqueue(context.Background(), id, ownerID)
	require.NoError(t, err)
	assert.True(t, started)

	calls := starter.getCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, id, calls[0])
}

func TestEnqueue_StaysQueued(t *testing.T) {
	qr := newMockQueueRepo()
	qr.runningCount = 20 // at global cap

	starter := newMockStarter()
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 20, PerUserCap: 5}}
	m := queue.NewManager(qr, newMockInstanceRepo(), cfg, starter)

	id := uuid.New()
	ownerID := uuid.New()
	started, err := m.Enqueue(context.Background(), id, ownerID)
	require.NoError(t, err)
	assert.False(t, started)

	calls := starter.getCalls()
	assert.Empty(t, calls)
}

// ---------------------------------------------------------------------------
// Tests: NotifyCapacityFreed
// ---------------------------------------------------------------------------

func TestNotifyCapacityFreed_TriggersEvaluate(t *testing.T) {
	id1 := uuid.New()
	qr := newMockQueueRepo()
	qr.eligible = []*instance.Instance{
		{ID: id1, State: instance.StateQueued},
	}

	starter := newMockStarter()
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	m := queue.NewManager(qr, newMockInstanceRepo(), cfg, starter)

	m.NotifyCapacityFreed(context.Background())

	// Wait a bit for the async goroutine to run
	time.Sleep(100 * time.Millisecond)

	calls := starter.getCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, id1, calls[0])
}

// ---------------------------------------------------------------------------
// Tests: OnTimerFired (webhook timeout)
// ---------------------------------------------------------------------------

type mockWebhookRegistry struct {
	mu              sync.Mutex
	deactivateCalls []string
	registerCalls   []string
}

func (r *mockWebhookRegistry) Register(token string, _ uuid.UUID, _ string, _ *string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.registerCalls = append(r.registerCalls, token)
}

func (r *mockWebhookRegistry) Deactivate(token string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deactivateCalls = append(r.deactivateCalls, token)
}

func TestOnTimerFired_WebhookTimeout_DeactivatesAndFails(t *testing.T) {
	id := uuid.New()
	token := "webhook-token-123"
	nodeID := "webhook-node"

	instRepo := newMockInstanceRepo()
	instRepo.mu.Lock()
	instRepo.instances[id] = &instance.Instance{
		ID:            id,
		State:         instance.StateWaiting,
		WebhookToken:  &token,
		CurrentNodeID: &nodeID,
	}
	instRepo.mu.Unlock()

	qr := newMockQueueRepo()
	starter := newMockStarter()
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	registry := &mockWebhookRegistry{}

	m := queue.NewManager(qr, instRepo, cfg, starter)
	m.SetWebhookRegistry(registry)

	err := m.OnTimerFired(context.Background(), id, queue.TimerTypeWebhook)
	require.NoError(t, err)

	// Token deactivated.
	registry.mu.Lock()
	assert.Contains(t, registry.deactivateCalls, token)
	registry.mu.Unlock()

	// Instance failed.
	instRepo.mu.Lock()
	inst := instRepo.instances[id]
	assert.Equal(t, instance.StateFailed, inst.State)
	instRepo.mu.Unlock()
}

func TestOnTimerFired_WebhookTimeout_InvokesChildTerminalHook(t *testing.T) {
	id := uuid.New()
	token := "webhook-token-hook"
	nodeID := "webhook-node"

	instRepo := newMockInstanceRepo()
	instRepo.mu.Lock()
	instRepo.instances[id] = &instance.Instance{
		ID:            id,
		State:         instance.StateWaiting,
		WebhookToken:  &token,
		CurrentNodeID: &nodeID,
	}
	instRepo.mu.Unlock()

	qr := newMockQueueRepo()
	starter := newMockStarter()
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	registry := &mockWebhookRegistry{}

	m := queue.NewManager(qr, instRepo, cfg, starter)
	m.SetWebhookRegistry(registry)

	var hookCalls int
	var hookID uuid.UUID
	m.SetChildTerminalHook(func(_ context.Context, instanceID uuid.UUID) {
		hookCalls++
		hookID = instanceID
	})

	err := m.OnTimerFired(context.Background(), id, queue.TimerTypeWebhook)
	require.NoError(t, err)

	assert.Equal(t, 1, hookCalls, "the LIVE webhook-timeout path must wake the wired child-terminal hook")
	assert.Equal(t, id, hookID, "the hook must receive the timed-out instance's own ID")
}

func TestOnTimerFired_WebhookTimeout_InstanceNotWaiting_NoOp(t *testing.T) {
	id := uuid.New()
	token := "webhook-token-456"

	instRepo := newMockInstanceRepo()
	instRepo.mu.Lock()
	instRepo.instances[id] = &instance.Instance{
		ID:           id,
		State:        instance.StateCancelled, // already cancelled
		WebhookToken: &token,
	}
	instRepo.mu.Unlock()

	qr := newMockQueueRepo()
	starter := newMockStarter()
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	registry := &mockWebhookRegistry{}

	m := queue.NewManager(qr, instRepo, cfg, starter)
	m.SetWebhookRegistry(registry)

	err := m.OnTimerFired(context.Background(), id, queue.TimerTypeWebhook)
	require.NoError(t, err)

	// Token should NOT be deactivated (instance not waiting).
	registry.mu.Lock()
	assert.Empty(t, registry.deactivateCalls)
	registry.mu.Unlock()

	// Instance state unchanged.
	instRepo.mu.Lock()
	inst := instRepo.instances[id]
	assert.Equal(t, instance.StateCancelled, inst.State)
	instRepo.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Tests: CanStart error cases
// ---------------------------------------------------------------------------

func TestCanStart_CountRunningError(t *testing.T) {
	qr := newMockQueueRepo()
	qr.countRunningErr = fmt.Errorf("db error")

	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 20, PerUserCap: 5}}
	m := queue.NewManager(qr, newMockInstanceRepo(), cfg, newMockStarter())

	_, err := m.CanStart(context.Background(), uuid.New())
	require.Error(t, err)
}

func TestCanStart_CountRunningByOwnerError(t *testing.T) {
	qr := newMockQueueRepo()
	qr.runningCount = 1
	qr.countRunningByOwnerErr = fmt.Errorf("db error")

	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 20, PerUserCap: 5}}
	m := queue.NewManager(qr, newMockInstanceRepo(), cfg, newMockStarter())

	_, err := m.CanStart(context.Background(), uuid.New())
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// Tests: Evaluate error cases
// ---------------------------------------------------------------------------

func TestEvaluate_NextEligibleError(t *testing.T) {
	qr := newMockQueueRepo()
	qr.nextEligibleErr = fmt.Errorf("db error")

	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	m := queue.NewManager(qr, newMockInstanceRepo(), cfg, newMockStarter())

	err := m.Evaluate(context.Background())
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// Tests: Enqueue error cases
// ---------------------------------------------------------------------------

func TestEnqueue_CanStartError(t *testing.T) {
	qr := newMockQueueRepo()
	qr.countRunningErr = fmt.Errorf("db error")

	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	m := queue.NewManager(qr, newMockInstanceRepo(), cfg, newMockStarter())

	_, err := m.Enqueue(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err)
}

func TestEnqueue_StartFails(t *testing.T) {
	qr := newMockQueueRepo()
	qr.runningCount = 0

	id := uuid.New()
	starter := newMockStarter()
	starter.errOnCall[id] = fmt.Errorf("start failed")

	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	m := queue.NewManager(qr, newMockInstanceRepo(), cfg, starter)

	_, err := m.Enqueue(context.Background(), id, uuid.New())
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// Tests: OnTimerFired — TimerTypeDelay
// ---------------------------------------------------------------------------

func TestOnTimerFired_TimerDelay_EnqueuesAndEvaluates(t *testing.T) {
	id := uuid.New()
	nextID := uuid.New()

	instRepo := newMockInstanceRepo()
	instRepo.mu.Lock()
	instRepo.instances[id] = &instance.Instance{
		ID:    id,
		State: instance.StateWaiting,
	}
	instRepo.mu.Unlock()

	qr := newMockQueueRepo()
	qr.eligible = []*instance.Instance{
		{ID: nextID, State: instance.StateQueued},
	}

	starter := newMockStarter()
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	m := queue.NewManager(qr, instRepo, cfg, starter)

	err := m.OnTimerFired(context.Background(), id, queue.TimerTypeDelay)
	require.NoError(t, err)

	// SetQueued should have been called for the timer-expired instance.
	qr.mu.Lock()
	assert.Contains(t, qr.setQueuedCalls, id)
	qr.mu.Unlock()

	// Evaluate should have started the next eligible instance.
	calls := starter.getCalls()
	assert.Contains(t, calls, nextID)
}

func TestOnTimerFired_TimerDelay_InstanceNotWaiting_NoOp(t *testing.T) {
	id := uuid.New()

	instRepo := newMockInstanceRepo()
	instRepo.mu.Lock()
	instRepo.instances[id] = &instance.Instance{
		ID:    id,
		State: instance.StatePaused,
	}
	instRepo.mu.Unlock()

	qr := newMockQueueRepo()
	starter := newMockStarter()
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	m := queue.NewManager(qr, instRepo, cfg, starter)

	err := m.OnTimerFired(context.Background(), id, queue.TimerTypeDelay)
	require.NoError(t, err)

	qr.mu.Lock()
	assert.Empty(t, qr.setQueuedCalls)
	qr.mu.Unlock()

	assert.Empty(t, starter.getCalls())
}

func TestOnTimerFired_WebhookTimeout_SetFailedError(t *testing.T) {
	id := uuid.New()
	token := "webhook-token-789"
	nodeID := "webhook-node"

	instRepo := newMockInstanceRepo()
	instRepo.mu.Lock()
	instRepo.instances[id] = &instance.Instance{
		ID:            id,
		State:         instance.StateWaiting,
		WebhookToken:  &token,
		CurrentNodeID: &nodeID,
	}
	instRepo.setFailedErr = fmt.Errorf("db write failed")
	instRepo.mu.Unlock()

	qr := newMockQueueRepo()
	starter := newMockStarter()
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	registry := &mockWebhookRegistry{}

	m := queue.NewManager(qr, instRepo, cfg, starter)
	m.SetWebhookRegistry(registry)

	err := m.OnTimerFired(context.Background(), id, queue.TimerTypeWebhook)
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// Tests: UpdateConfig
// ---------------------------------------------------------------------------

func TestUpdateConfig_UpdatesCaps(t *testing.T) {
	qr := newMockQueueRepo()
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 20, PerUserCap: 5}}
	m := queue.NewManager(qr, newMockInstanceRepo(), cfg, newMockStarter())

	m.UpdateConfig(50, 10)

	// New global cap is 50, running count 30 should be under cap now.
	qr.mu.Lock()
	qr.runningCount = 30
	qr.mu.Unlock()
	canStart, err := m.CanStart(context.Background(), uuid.New())
	require.NoError(t, err)
	assert.True(t, canStart)
}

func TestUpdateConfig_UnlimitedCaps(t *testing.T) {
	qr := newMockQueueRepo()
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 20, PerUserCap: 5}}
	m := queue.NewManager(qr, newMockInstanceRepo(), cfg, newMockStarter())

	m.UpdateConfig(0, 0) // 0 = unlimited

	qr.mu.Lock()
	qr.runningCount = 9999
	ownerID := uuid.New()
	qr.ownerCounts[ownerID] = 9999
	qr.mu.Unlock()

	canStart, err := m.CanStart(context.Background(), ownerID)
	require.NoError(t, err)
	assert.True(t, canStart)
}
