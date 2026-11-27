// domain/runner/service_drain_test.go
package runner_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/runner/hubtest"
	"github.com/arbitron/arbitron/domain/audit"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type drainAudit struct {
	mu  sync.Mutex
	got []string
}

func (a *drainAudit) Record(_ context.Context, action, target, _ string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.got = append(a.got, action+" "+target)
}

type seeAll struct{ hidden map[uuid.UUID]bool }

func (v seeAll) CanView(_ context.Context, id uuid.UUID) bool { return !v.hidden[id] }

type okProber struct{}

func (okProber) Probe(context.Context, runner.Source) (runner.ProbeResult, error) {
	return runner.ProbeResult{Auth: runner.AuthOK}, nil
}

type noConfirm struct{}

func (noConfirm) Confirm(context.Context, runner.Source) error {
	return runner.ErrConfirmationUnsupported
}

type noAlerts struct{}

func (noAlerts) SourceLoggedOut(context.Context, runner.Source) error { return nil }
func (noAlerts) SourceRecovered(context.Context, runner.Source) error { return nil }

func newDrainService(t *testing.T, r *drainRig, vis seeAll, a *drainAudit) *runner.Service {
	t.Helper()
	p := r.pool
	mon := runner.NewHealthMonitor(runner.MonitorDeps{Registry: p.Registry, Probers: map[runner.CredentialKind]runner.SourceProber{runner.CredAnthropicSubscription: okProber{}},
		Confirm: noConfirm{}, Alerts: noAlerts{}, Events: p.Events, Notifier: p.Notifier, Clock: p.Clock.Now, Interval: time.Hour})
	svc := runner.NewService(runner.ServiceDeps{Registry: p.Registry, Ledger: p.Ledger, Events: p.Log, Recorder: p.Events, Audit: a,
		Visibility: vis, Monitor: mon, Notifier: p.Notifier, Clock: p.Clock.Now})
	svc.SetDrainController(r.ctrl)
	return svc
}

func asRole(role identity.Role) context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{UserID: uuid.New(), Username: "root", Role: role})
}

func TestServiceDrain_AdminOnlyAuditedAndShownOnRunnerGet(t *testing.T) {
	r := newDrainRig(t)
	r.online(t, "mac-1")
	r.online(t, "mac-2")
	run, a := r.homedRun(t)
	aud := &drainAudit{}
	svc := newDrainService(t, r, seeAll{}, aud)

	_, err := svc.Drain(asRole(identity.RoleUser), "mac-1")
	require.ErrorIs(t, err, identity.ErrForbidden)
	_, err = svc.Drain(context.Background(), "mac-1")
	require.ErrorIs(t, err, identity.ErrUnauthenticated)
	_, err = svc.Drain(asRole(identity.RoleAdmin), runner.BuiltInRunnerID)
	require.Error(t, err)

	p, err := svc.Drain(asRole(identity.RoleAdmin), "mac-1")
	require.NoError(t, err)
	assert.Equal(t, 1, p.ActiveSteps)
	assert.False(t, p.Complete)
	rn, _, err := r.pool.Registry.GetRunner(context.Background(), "mac-1")
	require.NoError(t, err)
	assert.True(t, rn.Draining, "placement excludes it from now on")
	assert.Contains(t, aud.got, audit.ActionRunnerDrain+" mac-1")
	assert.Contains(t, r.pool.EventKinds(t), runner.EventRunnerDraining)

	d, err := svc.Get(asRole(identity.RoleUser), "mac-1")
	require.NoError(t, err)
	require.NotNil(t, d.Drain, "runner_get carries drain progress")
	assert.Equal(t, 1, d.Drain.HomedWorkspaces)

	require.NoError(t, r.pool.Ledger.Complete(context.Background(), a.Fence, "result"))
	p, err = svc.Drain(asRole(identity.RoleAdmin), "mac-1") // idempotent re-call reports progress
	require.NoError(t, err)
	assert.True(t, p.Complete)
	h, _ := r.repo.GetHome(context.Background(), run)
	assert.Equal(t, runner.RunnerID("mac-2"), h.RunnerID)
	assert.Equal(t, []string{audit.ActionRunnerDrain + " mac-1", audit.ActionRunnerDrain + " mac-1"}, aud.got,
		"every call is audited; refused calls are not")

	views, err := svc.List(asRole(identity.RoleUser))
	require.NoError(t, err)
	for _, v := range views {
		if v.Runner.ID == "mac-1" {
			require.NotNil(t, v.Drain)
			assert.True(t, v.Drain.Complete)
		} else {
			assert.Nil(t, v.Drain, "only draining runners report drain progress")
		}
	}
}

func TestServiceDrain_BlockersHideRunsTheCallerCannotView(t *testing.T) {
	r := newDrainRig(t)
	r.online(t, "mac-1")
	run, a := r.homedRun(t)
	require.NoError(t, r.pool.Ledger.Complete(context.Background(), a.Fence, "result"))
	svc := newDrainService(t, r, seeAll{hidden: map[uuid.UUID]bool{run: true}}, &drainAudit{})

	p, err := svc.Drain(asRole(identity.RoleAdmin), "mac-1")
	require.NoError(t, err)
	require.Len(t, p.Blocked, 1)
	assert.Nil(t, p.Blocked[0].RunID, "a run the caller cannot view is not named")
	assert.Equal(t, "anthropic_subscription:anthropic", p.Blocked[0].Group, "the missing capacity is still named")
	detail, err := svc.Get(asRole(identity.RoleUser), "mac-1")
	require.NoError(t, err)
	require.NotNil(t, detail.Drain)
	require.Len(t, detail.Drain.Blocked, 1)
	assert.Nil(t, detail.Drain.Blocked[0].RunID)
	views, err := svc.List(asRole(identity.RoleUser))
	require.NoError(t, err)
	for _, view := range views {
		if view.Runner.ID == "mac-1" {
			require.NotNil(t, view.Drain)
			require.Len(t, view.Drain.Blocked, 1)
			assert.Nil(t, view.Drain.Blocked[0].RunID)
		}
	}
}

func TestDrain_BuiltInActiveSessionBlocksWithoutRejectingRequest(t *testing.T) {
	r := newDrainRig(t)
	r.pool.SeedBuiltIn(t, 2, hubtest.BuiltInAnthropic())
	r.sessions.n[runner.BuiltInRunnerID] = 1
	aud := &drainAudit{}
	svc := newDrainService(t, r, seeAll{}, aud)
	progress, err := svc.Drain(asRole(identity.RoleAdmin), runner.BuiltInRunnerID)
	require.NoError(t, err)
	assert.False(t, progress.Complete)
	assert.Equal(t, 1, progress.Sessions)
	require.Len(t, progress.Blocked, 1)
	assert.Equal(t, runner.DrainKindSession, progress.Blocked[0].Kind)
	assert.Equal(t, []string{audit.ActionRunnerDrain + " builtin"}, aud.got)
	detail, err := svc.Get(asRole(identity.RoleUser), runner.BuiltInRunnerID)
	require.NoError(t, err)
	assert.True(t, detail.Runner.Draining)
	require.NotNil(t, detail.Drain)
	assert.Equal(t, progress, *detail.Drain)
}

func TestServiceDrain_WithoutControllerIsUnavailable(t *testing.T) {
	r := newDrainRig(t)
	p := r.pool
	mon := runner.NewHealthMonitor(runner.MonitorDeps{Registry: p.Registry, Probers: map[runner.CredentialKind]runner.SourceProber{},
		Confirm: noConfirm{}, Alerts: noAlerts{}, Events: p.Events, Notifier: p.Notifier, Clock: p.Clock.Now, Interval: time.Hour})
	svc := runner.NewService(runner.ServiceDeps{Registry: p.Registry, Ledger: p.Ledger, Events: p.Log, Recorder: p.Events, Audit: &drainAudit{},
		Visibility: seeAll{}, Monitor: mon, Notifier: p.Notifier, Clock: p.Clock.Now})
	_, err := svc.Drain(asRole(identity.RoleAdmin), "mac-1")
	require.ErrorIs(t, err, runner.ErrDrainUnavailable)
}
