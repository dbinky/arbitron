package hubtest

import (
	"context"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/runner"
	"sync"
	"time"
)

type ObserveCall struct {
	Source runner.SourceID
	Result runner.ProbeResult
}

func (f *FakeHealth) ObserveProbe(ctx context.Context, src runner.SourceID, res runner.ProbeResult) (runner.SourceHealth, error) {
	f.mu.Lock()
	f.observed = append(f.observed, ObserveCall{src, res})
	f.mu.Unlock()
	s, err := f.reg.GetSource(ctx, src)
	if err != nil {
		return runner.SourceHealth{}, err
	}
	h := s.Health
	switch res.Auth {
	case runner.AuthOK, runner.AuthLoggedOut:
		h.Auth = res.Auth
	}
	if c := res.Capacity; c != nil && c.OrdinaryUsageAllowed != nil && !*c.OrdinaryUsageAllowed {
		h.Capacity, h.ResetAt = runner.CapacityExhausted, c.ResetAt
	}
	return h, f.reg.RecordHealth(ctx, src, h)
}
func (f *FakeHealth) Observations() []ObserveCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ObserveCall(nil), f.observed...)
}

type RecordingAlerts struct {
	mu                   sync.Mutex
	loggedOut, recovered int
}

func (a *RecordingAlerts) SourceLoggedOut(context.Context, runner.Source) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.loggedOut++
	return nil
}
func (a *RecordingAlerts) SourceRecovered(context.Context, runner.Source) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.recovered++
	return nil
}
func (a *RecordingAlerts) LoggedOut() int { a.mu.Lock(); defer a.mu.Unlock(); return a.loggedOut }

type NopConfirmer struct{}

func (NopConfirmer) Confirm(context.Context, runner.Source) error {
	return runner.ErrConfirmationUnsupported
}
func (p *Pool) NewMonitor(alerts runner.SourceAlertNotifier) *runner.HealthMonitor {
	return runner.NewHealthMonitor(runner.MonitorDeps{Registry: p.Registry, Confirm: NopConfirmer{}, Alerts: alerts, Events: p.Events, Notifier: p.Notifier, Clock: p.Clock.Now, Interval: time.Minute, Rand: func() float64 { return 0 }})
}
func (p *Pool) NewHubWithHealth(cfg runner.HubConfig, h runner.HubHealth) *runner.Hub {
	return runner.NewHub(runner.HubDeps{Inbox: adaptsqlite.NewRemoteInboxRepo(p.DB), Registry: p.Registry, Ledger: p.Ledger, Events: p.Events, Health: h, Notifier: p.Notifier, Clock: p.Clock.Now, Config: cfg})
}
