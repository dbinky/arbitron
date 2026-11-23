package runner

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"reflect"
	"sync"
	"time"

	"github.com/google/uuid"
)

// AuthObservation is one auth-only probe result.
type AuthObservation struct{ State AuthState }

// CapacityObservation is what a read-only probe could observe about capacity.
// OrdinaryUsageAllowed is authoritative when non-nil (Codex); nil is unknown.
type CapacityObservation struct {
	ModelScope           string
	State                CapacityState
	ResetAt              *time.Time
	OrdinaryUsageAllowed *bool
	Windows              []LimitWindow
	Headroom             *float64
}

// ProbeResult is a read-only probe's verdict. Capacity nil = not observable.
type ProbeResult struct {
	HealthRevision uint64
	Auth           AuthState
	Capacity       *CapacityObservation
}

// SourceProber is read-only: it spends no tokens and holds no slot.
type SourceProber interface {
	Probe(ctx context.Context, s Source) (ProbeResult, error)
}

// ConfirmationProber spends a bounded number of tokens through the dispatcher
// (so it reserves a slot). nil error = capacity confirmed.
type ConfirmationProber interface {
	Confirm(ctx context.Context, s Source) error
}

// SourceAlertNotifier raises operator alerts on auth transitions.
type SourceAlertNotifier interface {
	SourceLoggedOut(ctx context.Context, s Source) error
	SourceRecovered(ctx context.Context, s Source) error
}

// MonitorDeps wires the HealthMonitor.
type MonitorDeps struct {
	Registry         Registry
	Probers          map[CredentialKind]SourceProber
	Confirm          ConfirmationProber
	Alerts           SourceAlertNotifier
	Events           *EventRecorder
	Notifier         *PoolNotifier
	Clock            func() time.Time
	Interval, Jitter time.Duration
	Rand             func() float64
}

// ErrConfirmationUnsupported is returned by a ConfirmationProber for a source
// kind it cannot confirm; probation then waits for the next real step.
var ErrConfirmationUnsupported = errors.New("runner: confirmation probe unsupported for this source kind")

// HealthMonitor owns per-source health transitions. All read-modify-write of a
// source's health happens under mu; probes and confirmations run outside it.
type HealthMonitor struct {
	d           MonitorDeps
	mu          sync.Mutex
	lastProbe   map[SourceID]time.Time
	lastConfirm map[SourceID]time.Time
	exhaustedAt map[SourceID]time.Time
	jitter      map[SourceID]time.Duration
	held        map[SourceID]bool // ordinaryUsageAllowed=false: keep exhausted past reset
	confirming  map[SourceID]bool
	wg          sync.WaitGroup
	loop        sync.WaitGroup
	startOnce   sync.Once
	remote      SourceProber
}

// NewHealthMonitor validates the wiring; any missing dependency panics at boot.
func NewHealthMonitor(d MonitorDeps) *HealthMonitor {
	if d.Registry == nil || d.Events == nil || d.Notifier == nil || d.Alerts == nil || d.Confirm == nil || d.Clock == nil || d.Interval <= 0 {
		panic("runner: NewHealthMonitor requires Registry, Events, Notifier, Alerts, Confirm, Clock and a positive Interval")
	}
	if d.Rand == nil {
		d.Rand = rand.Float64
	}
	if d.Jitter < 0 {
		d.Jitter = 0
	}
	if d.Probers == nil {
		d.Probers = map[CredentialKind]SourceProber{}
	}
	return &HealthMonitor{
		d: d, lastProbe: map[SourceID]time.Time{}, lastConfirm: map[SourceID]time.Time{},
		exhaustedAt: map[SourceID]time.Time{}, jitter: map[SourceID]time.Duration{},
		held: map[SourceID]bool{}, confirming: map[SourceID]bool{},
	}
}

// Start runs Tick periodically until ctx ends.
func (m *HealthMonitor) Start(ctx context.Context) {
	m.startOnce.Do(func() {
		m.loop.Add(1)
		go func() {
			defer m.loop.Done()
			every := m.d.Interval / 4
			if every < time.Second {
				every = time.Second
			}
			ticker := time.NewTicker(every)
			defer ticker.Stop()
			for {
				if err := m.Tick(ctx); err != nil && ctx.Err() == nil {
					slog.Warn("runner health tick failed", "error", err)
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	})
}

// Wait is called after lifecycle cancellation; no new ticks can add confirmations after loop exit.
func (m *HealthMonitor) Wait() { m.loop.Wait(); m.wg.Wait() }

// ApplyOutcome folds one classified outcome (failure from the dispatcher, or
// success from the health-reporting runner set) into the source's health.
func (m *HealthMonitor) ApplyOutcome(ctx context.Context, id SourceID, o SourceOutcome) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	src, err := m.d.Registry.GetSource(ctx, id)
	if err != nil {
		return err
	}
	now := m.d.Clock()
	h := src.Health
	switch o.Class {
	case OutcomeNone:
		h.Auth = AuthOK
		if h.Capacity == CapacityProbation && (h.ConfirmationModel == "" || o.Model == h.ConfirmationModel) {
			h.Capacity, h.ResetAt = CapacityAvailable, nil
			m.clearEpisode(id)
		}
		h.Congested, h.CongestedUntil = false, nil
		if o.HeadroomHint != nil {
			v := *o.HeadroomHint
			h.Headroom = &v
		}
		if len(o.Windows) > 0 {
			h.Windows = o.Windows
		}
	case OutcomeLoggedOut:
		h.Auth = AuthLoggedOut
	case OutcomeExhausted:
		if o.Model != "" {
			h.ConfirmationModel = o.Model
		}
		if hmIsSubscription(src.Kind) {
			m.markExhausted(&h, id, o.ResetAt, now, false)
			if len(o.Windows) > 0 {
				h.Windows = o.Windows
			}
		} else {
			hmMarkCongested(&h, now, o.RetryAfter, m.d.Interval)
		}
	case OutcomeTransient:
		hmMarkCongested(&h, now, o.RetryAfter, m.d.Interval)
	}
	h.LastOutcome = o.Class
	return m.commit(ctx, src, h, Cause{Kind: CauseOutcome, Outcome: o.Class}, now)
}

// Tick is one pass: due read-only probes, reset-driven probation, bounded
// confirmations and congestion expiry.
func (m *HealthMonitor) Tick(ctx context.Context) error {
	snap, err := m.d.Registry.Snapshot(ctx)
	if err != nil {
		return err
	}
	now := m.d.Clock()
	for _, s := range snap.Sources {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, err := m.probe(ctx, s, now, false); err != nil && !errors.Is(err, ErrSourceNotFound) {
			slog.Warn("runner health probe failed", "source", string(s.ID), "error", err)
		}
		if err := m.advance(ctx, s.ID, snap.SourceInFlight[s.ID], now); err != nil && !errors.Is(err, ErrSourceNotFound) {
			slog.Warn("runner health advance failed", "source", string(s.ID), "error", err)
		}
	}
	return nil
}

// ProbeNow runs the read-only probe immediately (operator action).
func (m *HealthMonitor) ProbeNow(ctx context.Context, id SourceID) (SourceHealth, error) {
	src, err := m.d.Registry.GetSource(ctx, id)
	if err != nil {
		return SourceHealth{}, err
	}
	return m.probe(ctx, src, m.d.Clock(), true)
}

func (m *HealthMonitor) probe(ctx context.Context, s Source, now time.Time, force bool) (SourceHealth, error) {
	m.mu.Lock()
	prober := m.d.Probers[s.Kind]
	if s.RunnerID != BuiltInRunnerID {
		prober = m.remote
	}
	m.mu.Unlock()
	if prober == nil {
		return s.Health, nil
	}
	m.mu.Lock()
	last, seen := m.lastProbe[s.ID]
	due := force || !seen || now.Sub(last) >= m.d.Interval
	if due {
		m.lastProbe[s.ID] = now
	}
	m.mu.Unlock()
	if !due {
		return s.Health, nil
	}
	res, perr := prober.Probe(ctx, s)
	if perr != nil {
		res = ProbeResult{}
	}
	res.HealthRevision = s.Health.Revision
	return m.ObserveProbe(ctx, s.ID, res)
}

// SetRemoteProber installs the transport-backed read-only probe producer.
func (m *HealthMonitor) SetRemoteProber(p SourceProber) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.remote = p
}

// ObserveProbe serializes local and remote observations with classified outcomes.
func (m *HealthMonitor) ObserveProbe(ctx context.Context, id SourceID, res ProbeResult) (SourceHealth, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.d.Clock()
	cur, err := m.d.Registry.GetSource(ctx, id)
	if err != nil {
		return SourceHealth{}, err
	}
	h := cur.Health
	t := now
	h.LastProbeAt = &t
	if res.HealthRevision == cur.Health.Revision {
		switch res.Auth {
		case AuthOK, AuthLoggedOut:
			h.Auth = res.Auth
		}
		if c := res.Capacity; c != nil {
			if len(c.Windows) > 0 {
				h.Windows = c.Windows
			}
			if c.Headroom != nil {
				v := *c.Headroom
				h.Headroom = &v
			}
			switch {
			case c.OrdinaryUsageAllowed == nil:
				delete(m.held, id) // unknown: never infer recovery from percentages
			case *c.OrdinaryUsageAllowed:
				if h.ConfirmationModel != "" && c.ModelScope != h.ConfirmationModel {
					break
				}
				delete(m.held, id)
				if h.Capacity != CapacityAvailable {
					h.Capacity, h.ResetAt = CapacityAvailable, nil
					m.clearEpisode(id)
				}
			case hmIsSubscription(cur.Kind):
				reset := c.ResetAt
				if reset == nil {
					reset = h.ResetAt
				}
				if c.ModelScope != "" {
					h.ConfirmationModel = c.ModelScope
				}
				m.markExhausted(&h, id, reset, now, true)
			}
		}
	}
	if err := m.commit(ctx, cur, h, Cause{Kind: CauseSystem}, now); err != nil {
		return SourceHealth{}, err
	}
	stored, err := m.d.Registry.GetSource(ctx, id)
	return stored.Health, err
}

func (m *HealthMonitor) advance(ctx context.Context, id SourceID, inFlight int, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, err := m.d.Registry.GetSource(ctx, id)
	if err != nil {
		return err
	}
	h := cur.Health
	switch h.Capacity {
	case CapacityExhausted:
		if m.held[id] {
			break
		}
		if h.ResetAt != nil {
			if !now.Before(h.ResetAt.Add(m.jitterFor(id))) {
				h.Capacity = CapacityProbation
			}
		} else {
			since, ok := m.exhaustedAt[id]
			if !ok {
				m.exhaustedAt[id] = now // learned after a restart: start the interval now
			} else if now.Sub(since) >= m.d.Interval {
				h.Capacity = CapacityProbation
			}
		}
	case CapacityProbation:
		last, seen := m.lastConfirm[id]
		if inFlight == 0 && !m.confirming[id] && (!seen || now.Sub(last) >= m.d.Interval) {
			m.lastConfirm[id] = now
			m.confirming[id] = true
			m.wg.Add(1)
			src := cur
			go func() {
				defer m.wg.Done()
				m.finishConfirm(ctx, src, m.d.Confirm.Confirm(ctx, src))
			}()
		}
	}
	if h.Congested && h.CongestedUntil != nil && !now.Before(*h.CongestedUntil) {
		h.Congested, h.CongestedUntil = false, nil
	}
	return m.commit(ctx, cur, h, Cause{Kind: CauseSystem}, now)
}

func (m *HealthMonitor) finishConfirm(ctx context.Context, src Source, err error) {
	m.mu.Lock()
	delete(m.confirming, src.ID)
	m.mu.Unlock()
	switch {
	case err == nil:
		_ = m.ApplyOutcome(ctx, src.ID, SourceOutcome{Class: OutcomeNone, Model: src.Health.ConfirmationModel})
	case errors.Is(err, ErrConfirmationUnsupported):
		// probation stays; the next real step decides
	default:
		if o := ClassifyError(err); o.Class == OutcomeExhausted || o.Class == OutcomeLoggedOut {
			_ = m.ApplyOutcome(ctx, src.ID, o)
		}
	}
}

// markExhausted starts (or refreshes) an exhaustion episode. held=true comes
// from ordinaryUsageAllowed=false and keeps the source exhausted past its reset.
func (m *HealthMonitor) markExhausted(h *SourceHealth, id SourceID, reset *time.Time, now time.Time, held bool) {
	zero := 0.0
	h.Headroom = &zero
	h.ResetAt = hmCopyTime(reset)
	if _, ok := m.exhaustedAt[id]; !ok || h.Capacity != CapacityExhausted {
		m.exhaustedAt[id] = now
		m.jitter[id] = time.Duration(m.d.Rand() * float64(m.d.Jitter))
	}
	if held {
		m.held[id] = true
		h.Capacity = CapacityExhausted
		return
	}
	if reset != nil && !reset.After(now) {
		h.Capacity = CapacityProbation // a reset already past goes straight to probation
		return
	}
	h.Capacity = CapacityExhausted
}

func (m *HealthMonitor) jitterFor(id SourceID) time.Duration {
	if j, ok := m.jitter[id]; ok {
		return j
	}
	j := time.Duration(m.d.Rand() * float64(m.d.Jitter))
	m.jitter[id] = j
	return j
}

func (m *HealthMonitor) clearEpisode(id SourceID) {
	delete(m.exhaustedAt, id)
	delete(m.jitter, id)
	delete(m.held, id)
}

// commit persists h when anything changed, and emits events/alerts only on
// material transitions (never for a bare LastProbeAt refresh).
func (m *HealthMonitor) commit(ctx context.Context, src Source, h SourceHealth, cause Cause, now time.Time) error {
	prev := src.Health
	if reflect.DeepEqual(prev, h) {
		return nil
	}
	if hmMaterial(prev, h) {
		h.Revision = prev.Revision + 1
	}
	if err := m.d.Registry.RecordHealth(ctx, src.ID, h); err != nil {
		return err
	}
	if !hmMaterial(prev, h) {
		return nil
	}
	m.d.Notifier.Notify()
	m.record(ctx, src, EventSourceHealthChanged, cause, h, now)
	if prev.Auth != AuthLoggedOut && h.Auth == AuthLoggedOut {
		m.record(ctx, src, EventSourceLoggedOut, cause, h, now)
		if err := m.d.Alerts.SourceLoggedOut(ctx, src); err != nil {
			slog.Warn("runner source logged-out alert failed", "source", string(src.ID), "error", err)
		}
	}
	authBack := prev.Auth == AuthLoggedOut && h.Auth == AuthOK
	capBack := prev.Capacity != CapacityAvailable && h.Capacity == CapacityAvailable
	if authBack || capBack {
		m.record(ctx, src, EventSourceRecovered, cause, h, now)
	}
	if authBack {
		if err := m.d.Alerts.SourceRecovered(ctx, src); err != nil {
			slog.Warn("runner source recovered alert failed", "source", string(src.ID), "error", err)
		}
	}
	return nil
}

func (m *HealthMonitor) record(ctx context.Context, src Source, kind string, cause Cause, h SourceHealth, now time.Time) {
	detail := map[string]any{"auth": string(h.Auth), "capacity": string(h.Capacity), "congested": h.Congested}
	if h.ResetAt != nil {
		detail["resetAt"] = h.ResetAt.UTC().Format(time.RFC3339)
	}
	m.d.Events.Record(ctx, PoolEvent{ID: uuid.New(), At: now, Kind: kind, RunnerID: src.RunnerID, SourceID: src.ID, Cause: cause, Detail: detail})
}

func hmMaterial(a, b SourceHealth) bool {
	return a.Auth != b.Auth || a.Capacity != b.Capacity || a.Congested != b.Congested ||
		a.LastOutcome != b.LastOutcome || !hmSameTime(a.ResetAt, b.ResetAt) || !hmSameFloat(a.Headroom, b.Headroom)
}

func hmIsSubscription(k CredentialKind) bool {
	return k == CredAnthropicSubscription || k == CredOpenAISubscription
}

func hmMarkCongested(h *SourceHealth, now time.Time, retryAfter, fallback time.Duration) {
	d := retryAfter
	if d <= 0 {
		d = fallback
	}
	until := now.Add(d)
	h.Congested, h.CongestedUntil = true, &until
}

func hmCopyTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := *t
	return &v
}

func hmSameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func hmSameFloat(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
