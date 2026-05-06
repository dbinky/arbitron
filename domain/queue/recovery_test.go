package queue_test

import (
	"context"
	"fmt"
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
// Tests: RecoverTimers
// ---------------------------------------------------------------------------

func TestRecoverTimers_PastDue_TransitionsToQueued(t *testing.T) {
	id := uuid.New()
	pastDue := time.Now().Add(-1 * time.Minute)

	qr := newMockQueueRepo()
	qr.waitingWithTimer = []instance.Instance{
		{ID: id, State: instance.StateWaiting, WaitingUntil: &pastDue},
	}

	starter := newMockStarter()
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	m := queue.NewManager(qr, newMockInstanceRepo(), cfg, starter)

	handler := &mockTimerHandler{}
	scheduler := queue.NewScheduler(handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler.Start(ctx)
	defer scheduler.Stop()

	err := m.RecoverTimers(context.Background(), scheduler)
	require.NoError(t, err)

	qr.mu.Lock()
	assert.Contains(t, qr.setQueuedCalls, id)
	qr.mu.Unlock()
}

func TestRecoverTimers_FuturePending_RegistersWithScheduler(t *testing.T) {
	id := uuid.New()
	future := time.Now().Add(1 * time.Hour)

	qr := newMockQueueRepo()
	qr.waitingWithTimer = []instance.Instance{
		{ID: id, State: instance.StateWaiting, WaitingUntil: &future},
	}

	starter := newMockStarter()
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	m := queue.NewManager(qr, newMockInstanceRepo(), cfg, starter)

	handler := &mockTimerHandler{}
	scheduler := queue.NewScheduler(handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler.Start(ctx)
	defer scheduler.Stop()

	err := m.RecoverTimers(context.Background(), scheduler)
	require.NoError(t, err)

	// The scheduler should have the timer registered.
	assert.Equal(t, 1, scheduler.Pending())
}

func TestRecoverTimers_MultipleMixed(t *testing.T) {
	pastID := uuid.New()
	futureID := uuid.New()
	pastDue := time.Now().Add(-1 * time.Minute)
	future := time.Now().Add(1 * time.Hour)

	qr := newMockQueueRepo()
	qr.waitingWithTimer = []instance.Instance{
		{ID: pastID, State: instance.StateWaiting, WaitingUntil: &pastDue},
		{ID: futureID, State: instance.StateWaiting, WaitingUntil: &future},
	}

	starter := newMockStarter()
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	m := queue.NewManager(qr, newMockInstanceRepo(), cfg, starter)

	handler := &mockTimerHandler{}
	scheduler := queue.NewScheduler(handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler.Start(ctx)
	defer scheduler.Stop()

	err := m.RecoverTimers(context.Background(), scheduler)
	require.NoError(t, err)

	// Past-due should be queued.
	qr.mu.Lock()
	assert.Contains(t, qr.setQueuedCalls, pastID)
	assert.NotContains(t, qr.setQueuedCalls, futureID)
	qr.mu.Unlock()

	// Future should be scheduled.
	assert.Equal(t, 1, scheduler.Pending())
}

func TestRecoverTimers_NoWaitingInstances(t *testing.T) {
	qr := newMockQueueRepo()
	// Empty — no waiting instances.

	starter := newMockStarter()
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	m := queue.NewManager(qr, newMockInstanceRepo(), cfg, starter)

	handler := &mockTimerHandler{}
	scheduler := queue.NewScheduler(handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler.Start(ctx)
	defer scheduler.Stop()

	err := m.RecoverTimers(context.Background(), scheduler)
	require.NoError(t, err)
}

func TestRecoverTimers_TriggersEvaluation(t *testing.T) {
	pastID := uuid.New()
	pastDue := time.Now().Add(-1 * time.Minute)

	// Set up a queued eligible instance that should be started during evaluation.
	eligibleID := uuid.New()
	qr := newMockQueueRepo()
	qr.waitingWithTimer = []instance.Instance{
		{ID: pastID, State: instance.StateWaiting, WaitingUntil: &pastDue},
	}
	qr.eligible = []*instance.Instance{
		{ID: eligibleID, State: instance.StateQueued},
	}

	starter := newMockStarter()
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	m := queue.NewManager(qr, newMockInstanceRepo(), cfg, starter)

	handler := &mockTimerHandler{}
	scheduler := queue.NewScheduler(handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler.Start(ctx)
	defer scheduler.Stop()

	err := m.RecoverTimers(context.Background(), scheduler)
	require.NoError(t, err)

	// Evaluate should have tried to start the eligible instance.
	calls := starter.getCalls()
	assert.Contains(t, calls, eligibleID)
}

// TestRecoverTimers_WebhookInstanceNotInList verifies the DB-level fix (DB-002):
// ListWaitingWithTimer excludes instances with webhook_token set. This test
// ensures that RecoverTimers correctly handles an empty timer list even when
// webhook-wait instances exist (they are handled exclusively by RecoverWebhooks).
func TestRecoverTimers_WebhookInstanceNotInList(t *testing.T) {
	qr := newMockQueueRepo()
	// Deliberately empty: the DB query filters out webhook instances.
	// A webhook-wait instance with past-due timeout is NOT returned here.
	qr.waitingWithTimer = []instance.Instance{}

	instRepo := newMockInstanceRepo()
	pastDue := time.Now().Add(-2 * time.Minute)
	token := "wh-token-excluded"
	id := uuid.New()
	instRepo.mu.Lock()
	instRepo.instances[id] = &instance.Instance{
		ID:           id,
		State:        instance.StateWaiting,
		WebhookToken: &token,
		WaitingUntil: &pastDue,
	}
	instRepo.mu.Unlock()

	starter := newMockStarter()
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	m := queue.NewManager(qr, instRepo, cfg, starter)

	handler := &mockTimerHandler{}
	scheduler := queue.NewScheduler(handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler.Start(ctx)
	defer scheduler.Stop()

	err := m.RecoverTimers(context.Background(), scheduler)
	require.NoError(t, err)

	// Webhook instance must NOT be queued by RecoverTimers.
	qr.mu.Lock()
	assert.NotContains(t, qr.setQueuedCalls, id, "webhook instance must not be queued by RecoverTimers")
	qr.mu.Unlock()

	// Instance must still be in waiting state.
	inst, _ := instRepo.Get(context.Background(), id)
	require.NotNil(t, inst)
	assert.Equal(t, instance.StateWaiting, inst.State)
}

// ---------------------------------------------------------------------------
// Tests: RecoverWebhooks
// ---------------------------------------------------------------------------

func TestRecoverWebhooks_ReRegistersToken(t *testing.T) {
	id := uuid.New()
	token := "recover-token-1"
	nodeID := "webhook-node"

	qr := newMockQueueRepo()
	qr.waitingWithWebhook = []instance.Instance{
		{
			ID:            id,
			State:         instance.StateWaiting,
			WebhookToken:  &token,
			CurrentNodeID: &nodeID,
			WaitingUntil:  nil, // no timeout
		},
	}

	instRepo := newMockInstanceRepo()
	starter := newMockStarter()
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	registry := &mockWebhookRegistry{}

	m := queue.NewManager(qr, instRepo, cfg, starter)
	m.SetWebhookRegistry(registry)

	handler := &mockTimerHandler{}
	scheduler := queue.NewScheduler(handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler.Start(ctx)
	defer scheduler.Stop()

	err := m.RecoverWebhooks(ctx, scheduler)
	require.NoError(t, err)

	// Token should be re-registered.
	registry.mu.Lock()
	assert.Contains(t, registry.registerCalls, token)
	registry.mu.Unlock()
}

func TestRecoverWebhooks_TimedOut_FailsInstance(t *testing.T) {
	id := uuid.New()
	token := "recover-token-timeout"
	nodeID := "webhook-node"
	pastDue := time.Now().Add(-2 * time.Minute)

	qr := newMockQueueRepo()
	qr.waitingWithWebhook = []instance.Instance{
		{
			ID:            id,
			State:         instance.StateWaiting,
			WebhookToken:  &token,
			CurrentNodeID: &nodeID,
			WaitingUntil:  &pastDue, // expired during downtime
		},
	}

	instRepo := newMockInstanceRepo()
	instRepo.mu.Lock()
	instRepo.instances[id] = &instance.Instance{
		ID:           id,
		State:        instance.StateWaiting,
		WebhookToken: &token,
	}
	instRepo.mu.Unlock()

	starter := newMockStarter()
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	registry := &mockWebhookRegistry{}

	m := queue.NewManager(qr, instRepo, cfg, starter)
	m.SetWebhookRegistry(registry)

	handler := &mockTimerHandler{}
	scheduler := queue.NewScheduler(handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler.Start(ctx)
	defer scheduler.Stop()

	err := m.RecoverWebhooks(ctx, scheduler)
	require.NoError(t, err)

	// Token should be deactivated.
	registry.mu.Lock()
	assert.Contains(t, registry.deactivateCalls, token)
	registry.mu.Unlock()

	// Instance should be failed.
	instRepo.mu.Lock()
	inst := instRepo.instances[id]
	assert.Equal(t, instance.StateFailed, inst.State)
	instRepo.mu.Unlock()
}

func TestRecoverTimers_NilWaitingUntil_Skipped(t *testing.T) {
	id := uuid.New()
	qr := newMockQueueRepo()
	qr.waitingWithTimer = []instance.Instance{
		{ID: id, State: instance.StateWaiting, WaitingUntil: nil}, // nil — should be skipped
	}

	starter := newMockStarter()
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	m := queue.NewManager(qr, newMockInstanceRepo(), cfg, starter)

	handler := &mockTimerHandler{}
	scheduler := queue.NewScheduler(handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler.Start(ctx)
	defer scheduler.Stop()

	err := m.RecoverTimers(context.Background(), scheduler)
	require.NoError(t, err)

	qr.mu.Lock()
	assert.Empty(t, qr.setQueuedCalls, "nil WaitingUntil should be skipped")
	qr.mu.Unlock()
	assert.Equal(t, 0, scheduler.Pending())
}

func TestRecoverTimers_SetQueuedError_ContinuesAndNoError(t *testing.T) {
	pastID := uuid.New()
	futureID := uuid.New()
	pastDue := time.Now().Add(-time.Minute)
	future := time.Now().Add(time.Hour)

	qr := newMockQueueRepo()
	qr.waitingWithTimer = []instance.Instance{
		{ID: pastID, State: instance.StateWaiting, WaitingUntil: &pastDue},
		{ID: futureID, State: instance.StateWaiting, WaitingUntil: &future},
	}
	// SetQueued fails — past-due re-queue attempt fails.
	qr.setQueuedErr = fmt.Errorf("db error")

	starter := newMockStarter()
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	m := queue.NewManager(qr, newMockInstanceRepo(), cfg, starter)

	handler := &mockTimerHandler{}
	scheduler := queue.NewScheduler(handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler.Start(ctx)
	defer scheduler.Stop()

	// RecoverTimers should return no error (SetQueued errors are logged, not propagated).
	err := m.RecoverTimers(context.Background(), scheduler)
	require.NoError(t, err)

	// Future timer should still have been registered with scheduler.
	assert.Equal(t, 1, scheduler.Pending())
}

func TestRecoverWebhooks_NilToken_Skipped(t *testing.T) {
	id := uuid.New()
	nodeID := "webhook-node"
	qr := newMockQueueRepo()
	qr.waitingWithWebhook = []instance.Instance{
		{ID: id, State: instance.StateWaiting, WebhookToken: nil, CurrentNodeID: &nodeID},
	}

	registry := &mockWebhookRegistry{}
	starter := newMockStarter()
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}

	m := queue.NewManager(qr, newMockInstanceRepo(), cfg, starter)
	m.SetWebhookRegistry(registry)

	handler := &mockTimerHandler{}
	scheduler := queue.NewScheduler(handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler.Start(ctx)
	defer scheduler.Stop()

	err := m.RecoverWebhooks(ctx, scheduler)
	require.NoError(t, err)

	registry.mu.Lock()
	assert.Empty(t, registry.registerCalls, "nil token should be skipped")
	registry.mu.Unlock()
}

func TestRecoverWebhooks_NoTimeout_OnlyReRegisters(t *testing.T) {
	id := uuid.New()
	token := "no-timeout-token"
	nodeID := "webhook-node"

	qr := newMockQueueRepo()
	// Webhook wait with no WaitingUntil — no timeout configured.
	qr.waitingWithWebhook = []instance.Instance{
		{
			ID:            id,
			State:         instance.StateWaiting,
			WebhookToken:  &token,
			CurrentNodeID: &nodeID,
			WaitingUntil:  nil, // no timeout
		},
	}

	registry := &mockWebhookRegistry{}
	starter := newMockStarter()
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}

	m := queue.NewManager(qr, newMockInstanceRepo(), cfg, starter)
	m.SetWebhookRegistry(registry)

	handler := &mockTimerHandler{}
	scheduler := queue.NewScheduler(handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler.Start(ctx)
	defer scheduler.Stop()

	err := m.RecoverWebhooks(ctx, scheduler)
	require.NoError(t, err)

	// Token should be re-registered but no scheduler timer (no timeout).
	registry.mu.Lock()
	assert.Contains(t, registry.registerCalls, token)
	assert.Empty(t, registry.deactivateCalls)
	registry.mu.Unlock()
	assert.Equal(t, 0, scheduler.Pending())
}

// TestRecoverWebhooks_PendingTimeout_RegistersWithScheduler verifies that an
// instance with WaitingUntil in the future re-registers both the webhook token
// and the timeout timer.
func TestRecoverWebhooks_PendingTimeout_RegistersWithScheduler(t *testing.T) {
	id := uuid.New()
	token := "recover-token-pending"
	nodeID := "webhook-node"
	future := time.Now().Add(1 * time.Hour) // timeout still pending

	qr := newMockQueueRepo()
	qr.waitingWithWebhook = []instance.Instance{
		{
			ID:            id,
			State:         instance.StateWaiting,
			WebhookToken:  &token,
			CurrentNodeID: &nodeID,
			WaitingUntil:  &future,
		},
	}

	instRepo := newMockInstanceRepo()
	starter := newMockStarter()
	cfg := &config.Config{Queue: config.QueueConfig{GlobalCap: 10, PerUserCap: 5}}
	registry := &mockWebhookRegistry{}

	m := queue.NewManager(qr, instRepo, cfg, starter)
	m.SetWebhookRegistry(registry)

	handler := &mockTimerHandler{}
	scheduler := queue.NewScheduler(handler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler.Start(ctx)
	defer scheduler.Stop()

	err := m.RecoverWebhooks(ctx, scheduler)
	require.NoError(t, err)

	// Token should be re-registered (not deactivated).
	registry.mu.Lock()
	assert.Contains(t, registry.registerCalls, token)
	assert.NotContains(t, registry.deactivateCalls, token)
	registry.mu.Unlock()

	// Scheduler should have the timeout registered.
	assert.Equal(t, 1, scheduler.Pending())
}
