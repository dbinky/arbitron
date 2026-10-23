// Package queue implements the queue management domain service for Arbitron.
// It is the single authority on capacity decisions, queue evaluation, and dequeuing.
package queue

import (
	"context"
	"errors"
	"fmt"
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
	// childTerminal is an OPTIONAL, nil-safe hook invoked when this Manager
	// drives an instance to a terminal state. It exists because the
	// webhook-timeout path below fails an instance from INSIDE domain/queue,
	// which cannot import domain/engine to call the spawn wake directly. Same
	// optional-collaborator pattern as SetWebhookRegistry / SetScheduler.
	childTerminal func(ctx context.Context, instanceID uuid.UUID)
	// corruptRun is the engine's complete terminal path for a run whose stored
	// model pins cannot be decoded (instance.CorruptPinsError from an admission
	// or recovery read). Required in production: without it such a row would
	// head the queue forever, so evaluation reports the error instead.
	corruptRun    func(ctx context.Context, instanceID uuid.UUID) error
	mu            sync.Mutex
	notifyMu      sync.Mutex
	notifications sync.WaitGroup
	stopped       bool
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

// SetChildTerminalHook wires the callback invoked when this Manager drives an
// instance to a terminal state, so a waiting spawn parent can be woken. Nil
// (unset) is valid: the startup reconciler is the authority for correctness and
// recovers any parent this hook misses.
func (m *Manager) SetChildTerminalHook(h func(ctx context.Context, instanceID uuid.UUID)) {
	m.childTerminal = h
}

// SetCorruptRunHandler wires the engine's terminal path for runs whose model
// pins are corrupt (Engine.FailCorruptRun). Readers only report such runs; this
// Manager hands each reported ID to the handler and carries on.
func (m *Manager) SetCorruptRunHandler(h func(ctx context.Context, instanceID uuid.UUID) error) {
	m.corruptRun = h
}

// failCorrupt hands every run in an *instance.CorruptPinsError to the corrupt
// run handler. handled reports whether err was such a report.
func (m *Manager) failCorrupt(ctx context.Context, err error) (handled bool, failErr error) {
	var corrupt *instance.CorruptPinsError
	if !errors.As(err, &corrupt) {
		return false, nil
	}
	if m.corruptRun == nil {
		return true, fmt.Errorf("queue: no corrupt-run handler: %w", err)
	}
	for _, id := range corrupt.IDs {
		if ferr := m.corruptRun(ctx, id); ferr != nil {
			return true, fmt.Errorf("queue: failing run %s with corrupt model pins: %w", id, ferr)
		}
	}
	return true, nil
}

// notifyChildTerminal invokes the optional hook. Nil-safe.
func (m *Manager) notifyChildTerminal(ctx context.Context, instanceID uuid.UUID) {
	if m.childTerminal == nil {
		return
	}
	m.childTerminal(ctx, instanceID)
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
	return m.evaluateLocked(ctx)
}

func (m *Manager) evaluateLocked(ctx context.Context) error {
	for {
		next, err := m.queue.NextEligible(ctx, m.config.Queue.GlobalCap, m.config.Queue.PerUserCap)
		if handled, ferr := m.failCorrupt(ctx, err); handled {
			if ferr != nil {
				return ferr
			}
			continue // the corrupt head is terminal now; ask again
		}
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
// started=true when the instance is already running or begins immediately,
// false when it stays queued, has advanced past running, or a reload failure
// prevents confirming its scheduling state. The caller has already persisted it.
func (m *Manager) Enqueue(ctx context.Context, id uuid.UUID, ownerID uuid.UUID) (started bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Create commits a queued row before its caller reaches Enqueue. A capacity
	// notification may already have started it through Evaluate. Check under
	// the same lock as Evaluate so we never launch a second graph walker and
	// overwrite the first one's cancellation context, even if it finished fast.
	inst, err := m.instances.Get(ctx, id)
	if err != nil {
		if errors.Is(err, instance.ErrNotFound) {
			return false, fmt.Errorf("queue enqueue: load instance %s: %w", id, err)
		}
		// Admission committed at Create. Reporting failure now would hide a
		// runnable child from Spawn or turn an accepted group task into a launch
		// failure. Drain via the state-filtered queue query instead of starting
		// this unknown-state id; an evaluator may already have run it.
		slog.Warn("queue enqueue: reload failed; preserving committed admission", "instanceID", id, "error", err)
		if evalErr := m.evaluateLocked(ctx); evalErr != nil {
			slog.Error("queue enqueue: evaluation deferred until capacity notification or recovery", "instanceID", id, "error", evalErr)
		}
		return false, nil
	}
	if inst == nil {
		return false, fmt.Errorf("queue enqueue: instance %s: %w", id, instance.ErrNotFound)
	}
	if inst.State != instance.StateQueued {
		return inst.State == instance.StateRunning, nil
	}

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
	m.notifyMu.Lock()
	defer m.notifyMu.Unlock()
	if m.stopped {
		return
	}
	m.notifications.Add(1)
	go func() {
		defer m.notifications.Done()
		if err := m.Evaluate(context.Background()); err != nil {
			slog.Error("queue: async evaluation failed after capacity freed", "error", err)
		}
	}()
}

// Stop joins asynchronous capacity notifications before collaborators close.
func (m *Manager) Stop() {
	m.notifyMu.Lock()
	m.stopped = true
	m.notifyMu.Unlock()
	m.notifications.Wait()
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
		if err := m.instances.SetFailed(ctx, instanceID, failedNodeID); err != nil {
			return err
		}
		// This instance may be somebody's awaited child; this is the one terminal
		// transition that happens outside domain/engine.
		m.notifyChildTerminal(ctx, instanceID)
		return nil
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
