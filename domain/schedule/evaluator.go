package schedule

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/notification"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// Clock is the time seam the evaluator drives. Tests inject a fake clock so all
// time-dependent behavior is deterministic; the evaluator NEVER calls time.Now
// directly.
type Clock interface{ Now() time.Time }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Trigger fires a workflow as the (context-injected) creator. The engine's
// TriggerWithDepartment satisfies it.
type Trigger interface {
	TriggerWithDepartment(ctx context.Context, workflowName string, variables map[string]any, priority, department string) (uuid.UUID, bool, error)
}

// ActiveRunChecker reports whether the schedule's workflow has a live run for
// the creator. The SQLite instance repo satisfies it via HasActiveRun.
type ActiveRunChecker interface {
	HasActiveRun(ctx context.Context, workflowName string, ownerID uuid.UUID) (bool, error)
}

// IdentityResolver builds the creator identity (with departments) for context
// injection so resolveStamp's department check passes. The user service
// satisfies it.
type IdentityResolver interface {
	IdentityForUser(ctx context.Context, userID uuid.UUID) (identity.Identity, error)
}

// Alerter raises a schedule_skipped / schedule_missed notification.
type Alerter interface {
	Alert(ctx context.Context, s Schedule, t notification.Type) error
}

// DefaultInterval is the evaluator's tick cadence: coarse enough to be cheap,
// fine enough for minute-granularity cron.
const DefaultInterval = 30 * time.Second

// Evaluator is the single in-process service that fires due enabled schedules.
// Its run loop is our own (not robfig/cron's runner) so it composes with the
// engine + the composition root's graceful shutdown. All time-dependent
// behavior flows through the injected Clock.
type Evaluator struct {
	repo     ScheduleRepository
	trigger  Trigger
	active   ActiveRunChecker
	idents   IdentityResolver
	alerter  Alerter
	clock    Clock
	interval time.Duration
	logger   *slog.Logger

	// Shutdown is a single context-based path. Start derives a cancellable
	// context whose cancel is stored in cancel; Stop calls it (guarded by
	// stopOnce so a double-Stop or a Stop after the loop already exited via the
	// parent ctx cannot close anything twice) and waits on doneCh.
	stopOnce sync.Once
	cancel   context.CancelFunc
	doneCh   chan struct{}
}

// Option configures an Evaluator.
type Option func(*Evaluator)

// WithClock injects the clock (default realClock).
func WithClock(c Clock) Option { return func(e *Evaluator) { e.clock = c } }

// WithInterval sets the tick interval (default DefaultInterval).
func WithInterval(d time.Duration) Option { return func(e *Evaluator) { e.interval = d } }

// WithLogger sets the logger (default slog.Default).
func WithLogger(l *slog.Logger) Option { return func(e *Evaluator) { e.logger = l } }

// NewEvaluator constructs an Evaluator. The clock defaults to realClock and the
// interval to DefaultInterval unless overridden via options.
func NewEvaluator(repo ScheduleRepository, trigger Trigger, active ActiveRunChecker, idents IdentityResolver, alerter Alerter, opts ...Option) *Evaluator {
	e := &Evaluator{
		repo:     repo,
		trigger:  trigger,
		active:   active,
		idents:   idents,
		alerter:  alerter,
		clock:    realClock{},
		interval: DefaultInterval,
		logger:   slog.Default(),
		doneCh:   make(chan struct{}),
	}
	for _, opt := range opts {
		opt(e)
	}
	if e.clock == nil {
		e.clock = realClock{}
	}
	if e.interval <= 0 {
		e.interval = DefaultInterval
	}
	if e.logger == nil {
		e.logger = slog.Default()
	}
	return e
}

// prevAnchor returns the reference instant from which the next fire is computed:
// the last fire time, or the creation time when never fired.
func prevAnchor(s Schedule) time.Time {
	if s.LastFiredAt != nil {
		return *s.LastFiredAt
	}
	return s.CreatedAt
}

// Tick is the unit-testable core: it fires every due enabled schedule once. The
// loop calls it on each ticker beat. A per-schedule error (corrupt cron,
// unresolvable creator, trigger failure) is logged and skipped so one bad
// schedule never crashes the loop or blocks the others.
func (e *Evaluator) Tick(ctx context.Context) error {
	schedules, err := e.repo.ListEnabled(ctx)
	if err != nil {
		return err
	}
	now := e.clock.Now()
	for i := range schedules {
		s := schedules[i]
		next, err := NextFire(s, prevAnchor(s))
		if err != nil {
			// Defense-in-depth: the management surface prevents persisting an
			// invalid cron, but a corrupt row must not crash the loop.
			e.logger.Error("schedule: skipping schedule with invalid cron/timezone",
				"scheduleID", s.ID, "error", err)
			continue
		}
		// due when next <= now.
		if next.After(now) {
			continue
		}
		busy, err := e.active.HasActiveRun(ctx, s.WorkflowName, s.CreatorID)
		if err != nil {
			e.logger.Error("schedule: active-run check failed; skipping occurrence",
				"scheduleID", s.ID, "error", err)
			continue
		}
		if busy {
			// Collision: skip this occurrence, alert, advance position so the same
			// occurrence is not retried (and re-alerted) every tick. No instance.
			if alertErr := e.alerter.Alert(ctx, s, notification.TypeScheduleSkipped); alertErr != nil {
				e.logger.Error("schedule: failed to raise skip alert", "scheduleID", s.ID, "error", alertErr)
			}
			if setErr := e.repo.SetLastFired(ctx, s.ID, next); setErr != nil {
				e.logger.Error("schedule: failed to advance position after skip", "scheduleID", s.ID, "error", setErr)
			}
			continue
		}
		e.fireOne(ctx, s, next)
	}
	return nil
}

// fireOne triggers the schedule's workflow as the creator and records the fire
// position. A missing creator or workflow alerts and is skipped; the position is
// still advanced on a deleted-workflow failure so the schedule does not hot-loop.
func (e *Evaluator) fireOne(ctx context.Context, s Schedule, fireTime time.Time) {
	id, err := e.idents.IdentityForUser(ctx, s.CreatorID)
	if err != nil {
		// Creator gone: cannot attribute the run. Skip without advancing so the
		// occurrence is retried if the creator is restored shortly; alert so the
		// operator sees the run did not happen.
		e.logger.Error("schedule: cannot resolve creator identity; skipping fire",
			"scheduleID", s.ID, "creatorID", s.CreatorID, "error", err)
		if alertErr := e.alerter.Alert(ctx, s, notification.TypeScheduleMissed); alertErr != nil {
			e.logger.Error("schedule: failed to raise miss alert", "scheduleID", s.ID, "error", alertErr)
		}
		return
	}
	fireCtx := identity.WithIdentity(ctx, id)
	_, _, err = e.trigger.TriggerWithDepartment(fireCtx, s.WorkflowName, s.Variables, "normal", s.Department)
	if err != nil {
		if errors.Is(err, workflow.ErrNotFound) {
			// Deleted workflow: alert and advance position so the schedule does not
			// hot-loop. The schedule is left enabled (the operator decides whether
			// to disable it) per the design's simplest documented choice.
			e.logger.Warn("schedule: workflow no longer exists; alerting and advancing",
				"scheduleID", s.ID, "workflow", s.WorkflowName)
			if alertErr := e.alerter.Alert(ctx, s, notification.TypeScheduleMissed); alertErr != nil {
				e.logger.Error("schedule: failed to raise miss alert", "scheduleID", s.ID, "error", alertErr)
			}
			if setErr := e.repo.SetLastFired(ctx, s.ID, fireTime); setErr != nil {
				e.logger.Error("schedule: failed to advance position after deleted-workflow", "scheduleID", s.ID, "error", setErr)
			}
			return
		}
		// Transient trigger failure (e.g. capacity/validation): log and leave the
		// position so a later tick retries.
		e.logger.Error("schedule: trigger failed", "scheduleID", s.ID, "workflow", s.WorkflowName, "error", err)
		return
	}
	if setErr := e.repo.SetLastFired(ctx, s.ID, fireTime); setErr != nil {
		e.logger.Error("schedule: failed to record fire position", "scheduleID", s.ID, "error", setErr)
	}
}

// Recover applies the no-backfill misfire policy once at startup, before Start.
// For each enabled schedule whose next fire (from its persisted position) is
// already in the past — occurrences missed during downtime — it does NOT fire,
// advances the position to the latest missed occurrence, and raises one
// schedule_missed alert. Schedules whose next fire is in the future are left
// untouched.
func (e *Evaluator) Recover(ctx context.Context) error {
	schedules, err := e.repo.ListEnabled(ctx)
	if err != nil {
		return err
	}
	now := e.clock.Now()
	for i := range schedules {
		s := schedules[i]
		next, err := NextFire(s, prevAnchor(s))
		if err != nil {
			e.logger.Error("schedule: recovery skipping schedule with invalid cron/timezone",
				"scheduleID", s.ID, "error", err)
			continue
		}
		if next.After(now) {
			continue // next fire is in the future: normal, nothing missed.
		}
		// Occurrences were missed during downtime. No backfill: advance the
		// position to the latest occurrence at or before now, so the next tick
		// computes the next FUTURE occurrence and fires it normally.
		last := lastOccurrenceBefore(s, now)
		if setErr := e.repo.SetLastFired(ctx, s.ID, last); setErr != nil {
			e.logger.Error("schedule: recovery failed to advance position", "scheduleID", s.ID, "error", setErr)
			continue
		}
		if alertErr := e.alerter.Alert(ctx, s, notification.TypeScheduleMissed); alertErr != nil {
			e.logger.Error("schedule: recovery failed to raise miss alert", "scheduleID", s.ID, "error", alertErr)
		}
	}
	return nil
}

// maxOccurrenceWalk caps the forward NextFire walk in lastOccurrenceBefore. It
// is a guard against an unbounded walk when a schedule has been disabled/down
// for a very long time: a per-minute cron tolerates an outage of roughly
// maxOccurrenceWalk minutes (~11.5 days) before the walk is capped. The start
// anchor is additionally bounded to now-occurrenceLookback (see below) so the
// walk length is governed by the lookback window, not by how stale the
// persisted position is.
const maxOccurrenceWalk = 100000

// occurrenceLookback bounds how far back the walk begins. A schedule that has
// been disabled or whose process was down far longer than this still resolves
// in a bounded number of NextFire calls: the start anchor is max(prevAnchor,
// now-occurrenceLookback). It is sized to comfortably exceed any realistic
// single outage at minute granularity.
const occurrenceLookback = 60 * 24 * time.Hour // 60 days

// lastOccurrenceBefore returns the latest scheduled occurrence at or before now,
// by iterating NextFire from the schedule's (lookback-bounded) position. It
// NEVER returns now: now is almost never an actual cron occurrence, and
// persisting it as LastFiredAt would shift every subsequent fire off the
// intended schedule. If the walk hits its cap it returns the LAST occurrence
// found so far; if iteration cannot find any occurrence it returns the start
// anchor (a real prior instant) rather than now.
func lastOccurrenceBefore(s Schedule, now time.Time) time.Time {
	// Bound the start anchor so a long-disabled schedule cannot burn the full
	// cap walking from a stale position; we only need the LATEST occurrence
	// before now, so starting at most occurrenceLookback back is sufficient.
	start := prevAnchor(s)
	if floor := now.Add(-occurrenceLookback); floor.After(start) {
		start = floor
	}
	last := start
	cur := start
	for i := 0; i < maxOccurrenceWalk; i++ {
		next, err := NextFire(s, cur)
		if err != nil {
			return last
		}
		if next.After(now) {
			return last
		}
		last = next
		cur = next
	}
	return last
}

// Start launches the loop goroutine: a ticker at the configured interval, each
// beat calling Tick. Shutdown flows through a single context: Start derives a
// cancellable child of ctx so the loop exits on EITHER the parent ctx being
// cancelled OR Stop calling the stored cancel. Mirrors queue.Scheduler's
// lifecycle.
func (e *Evaluator) Start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	e.cancel = cancel
	go e.loop(runCtx)
}

// Stop signals the loop to exit (via the context cancel stored by Start) and
// waits for it to drain. It is idempotent: a second Stop, or a Stop after the
// loop already exited because the parent context was cancelled, is a safe no-op
// — there is no close-of-closed-channel panic. Mirrors queue.Scheduler.Stop().
func (e *Evaluator) Stop() {
	e.stopOnce.Do(func() {
		if e.cancel == nil {
			return // Start was never called; nothing to wait on.
		}
		e.cancel()
		<-e.doneCh
	})
}

func (e *Evaluator) loop(ctx context.Context) {
	defer close(e.doneCh)
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := e.Tick(ctx); err != nil {
				e.logger.Error("schedule: tick failed", "error", err)
			}
		}
	}
}
