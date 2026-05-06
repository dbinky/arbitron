// Package queue implements the queue management domain service for Arbitron.
// It is the single authority on capacity decisions, queue evaluation, and dequeuing.
package queue

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// InstanceStarter starts a queued instance. Implemented by the Engine.
type InstanceStarter interface {
	StartInstance(ctx context.Context, id uuid.UUID) error
}

// WebhookRegistryPort defines the webhook registry operations the Manager needs.
type WebhookRegistryPort interface {
	Register(token string, instanceID uuid.UUID, nodeID string, secretHash *string)
	Deactivate(token string)
}

// TimerSchedulerPort defines the timer scheduling operations the Manager needs.
type TimerSchedulerPort interface {
	Schedule(instanceID uuid.UUID, waitingUntil time.Time, timerType string)
	Cancel(instanceID uuid.UUID)
}

// Manager is the queue management domain service. It is the single authority
// on capacity decisions, queue evaluation, and dequeuing.
type Manager struct {
	queue           instance.QueueRepository
	instances       instance.InstanceRepository
	config          *config.Config
	starter         InstanceStarter
	webhookRegistry WebhookRegistryPort
	scheduler       TimerSchedulerPort
	mu              sync.Mutex
}

// NewManager constructs a new Manager with all required dependencies.
func NewManager(
	queue instance.QueueRepository,
	instances instance.InstanceRepository,
	cfg *config.Config,
	starter InstanceStarter,
) *Manager {
	return &Manager{
		queue:     queue,
		instances: instances,
		config:    cfg,
		starter:   starter,
	}
}

// SetWebhookRegistry sets the webhook registry on the manager.
func (m *Manager) SetWebhookRegistry(reg WebhookRegistryPort) {
	m.webhookRegistry = reg
}

// SetScheduler sets the timer scheduler on the manager. This is used during
// initialization to break circular dependencies.
func (m *Manager) SetScheduler(s TimerSchedulerPort) {
	m.scheduler = s
}

// CanStart checks if a given owner can start an instance given current capacity.
func (m *Manager) CanStart(ctx context.Context, ownerID uuid.UUID) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.canStartLocked(ctx, ownerID)
}

// canStartLocked performs the capacity check assuming m.mu is already held.
func (m *Manager) canStartLocked(ctx context.Context, ownerID uuid.UUID) (bool, error) {
	globalCap := m.config.Queue.GlobalCap
	perUserCap := m.config.Queue.PerUserCap

	// Check global cap (skip if 0 = unlimited)
	if globalCap > 0 {
		running, err := m.queue.CountRunning(ctx)
		if err != nil {
			return false, err
		}
		if running >= globalCap {
			return false, nil
		}
	}
	// Check per-user cap (skip if 0 = unlimited)
	if perUserCap > 0 {
		userRunning, err := m.queue.CountRunningByOwner(ctx, ownerID)
		if err != nil {
			return false, err
		}
		if userRunning >= perUserCap {
			return false, nil
		}
	}
	return true, nil
}

// Evaluate is the queue drain loop. It starts as many eligible instances as
// capacity allows.
func (m *Manager) Evaluate(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for {
		next, err := m.queue.NextEligible(ctx, m.config.Queue.GlobalCap, m.config.Queue.PerUserCap)
		if err != nil {
			return err
		}
		if next == nil {
			return nil // no more eligible
		}

		if err := m.starter.StartInstance(ctx, next.ID); err != nil {
			slog.Error("queue evaluate: failed to start instance", "instanceID", next.ID, "error", err)
			return err
		}
	}
}

// Enqueue is called by Trigger to add an instance to the queue. It returns
// started=true when the instance begins running immediately, false when it
// stays in queued state.
func (m *Manager) Enqueue(ctx context.Context, id uuid.UUID, ownerID uuid.UUID) (started bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	canStart, err := m.canStartLocked(ctx, ownerID)
	if err != nil {
		return false, err
	}

	if canStart {
		if err := m.starter.StartInstance(ctx, id); err != nil {
			return false, err
		}
		return true, nil
	}

	// Instance stays in queued state (already set by Trigger)
	return false, nil
}

// NotifyCapacityFreed is called when an instance leaves running state.
// It triggers asynchronous queue evaluation.
func (m *Manager) NotifyCapacityFreed(_ context.Context) {
	go func() {
		if err := m.Evaluate(context.Background()); err != nil {
			slog.Error("queue: async evaluation failed after capacity freed", "error", err)
		}
	}()
}

// OnTimerFired implements TimerFiredHandler on the Manager.
// When a timer fires, if the instance is still in waiting state, it transitions
// to queued and triggers queue evaluation.
func (m *Manager) OnTimerFired(ctx context.Context, instanceID uuid.UUID, timerType string) error {
	inst, err := m.instances.Get(ctx, instanceID)
	if err != nil {
		return err
	}

	// Only transition if still in waiting state.
	if inst.State != instance.StateWaiting {
		return nil
	}

	switch timerType {
	case TimerTypeDelay:
		now := time.Now()
		if err := m.queue.SetQueued(ctx, instanceID, now); err != nil {
			return err
		}
		return m.Evaluate(ctx)

	case TimerTypeWebhook:
		// Webhook timeout: deactivate endpoint, fail the instance.
		if m.webhookRegistry != nil && inst.WebhookToken != nil {
			m.webhookRegistry.Deactivate(*inst.WebhookToken)
		}
		if err := m.instances.SetWebhookFields(ctx, instanceID, nil, nil); err != nil {
			slog.Error("queue: failed to clear webhook fields on timeout", "instanceID", instanceID, "error", err)
		}
		failedNodeID := ""
		if inst.CurrentNodeID != nil {
			failedNodeID = *inst.CurrentNodeID
		}
		return m.instances.SetFailed(ctx, instanceID, failedNodeID)
	}
	return nil
}

// UpdateConfig updates the queue capacity configuration at runtime.
func (m *Manager) UpdateConfig(globalCap, perUserCap int) {
	m.mu.Lock()
	m.config.Queue.GlobalCap = globalCap
	m.config.Queue.PerUserCap = perUserCap
	m.mu.Unlock()
}
