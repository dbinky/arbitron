package runner

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rdtWindow is a per-runner telemetry window. It honours the id argument
// (each runner has its own samples) and records every id it was asked for.
type rdtWindow struct {
	mu    sync.Mutex
	byID  map[RunnerID][]Telemetry
	asked []RunnerID
}

func (w *rdtWindow) Window(id RunnerID) []Telemetry {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.asked = append(w.asked, id)
	return append([]Telemetry(nil), w.byID[id]...)
}

func (w *rdtWindow) askedIDs() []RunnerID {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]RunnerID(nil), w.asked...)
}

// rdtAliasWindow hands out its own backing array, like a careless producer.
type rdtAliasWindow struct{ samples []Telemetry }

func (w *rdtAliasWindow) Window(RunnerID) []Telemetry { return w.samples }

var rdtT0 = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func rdtSample(offset time.Duration, cpu float64) Telemetry {
	c := cpu
	return Telemetry{SampledAt: rdtT0.Add(offset), CPUPercent: &c}
}

// rdtFixture is the P1 T15 service fixture with a second runner "mac-a" and
// the given window. A nil w leaves ServiceDeps.Window nil (a nil interface).
func rdtFixture(t *testing.T, w TelemetryWindow) *svFixture {
	t.Helper()
	f := newSVFixture(t)
	f.reg.mu.Lock()
	f.reg.runners["mac-a"] = Runner{ID: "mac-a", Name: "mac-a", StepLimit: 2, Liveness: "online"}
	f.reg.mu.Unlock()
	d := f.svc.d
	d.Window = w
	f.svc = NewService(d)
	return f
}

func rdtCPUs(ts []Telemetry) []float64 {
	out := make([]float64, 0, len(ts))
	for _, t := range ts {
		out = append(out, *t.CPUPercent)
	}
	return out
}

// Happy path + success: each runner's detail shows exactly its own window,
// oldest first, to any authenticated user.
func TestServiceGet_RecentTelemetryIsTheRequestedRunnersWindow(t *testing.T) {
	w := &rdtWindow{byID: map[RunnerID][]Telemetry{
		BuiltInRunnerID: {rdtSample(0, 10), rdtSample(time.Second, 20), rdtSample(2*time.Second, 30)},
		"mac-a":         {rdtSample(0, 90)},
	}}
	f := rdtFixture(t, w)

	d, err := f.svc.Get(svCtx(identity.RoleUser), BuiltInRunnerID)
	require.NoError(t, err)
	assert.Equal(t, []float64{10, 20, 30}, rdtCPUs(d.RecentTelemetry), "the built-in runner's samples, oldest first")
	assert.True(t, d.RecentTelemetry[0].SampledAt.Equal(rdtT0))
	assert.Len(t, d.Assignments, 2, "the window does not disturb the assignments")

	d, err = f.svc.Get(svCtx(identity.RoleUser), "mac-a")
	require.NoError(t, err)
	assert.Equal(t, []float64{90}, rdtCPUs(d.RecentTelemetry), "mac-a never shows the built-in runner's samples")

	assert.Equal(t, []RunnerID{BuiltInRunnerID, "mac-a"}, w.askedIDs(), "the window is read for the requested runner only")
}

// Edge: a runner that has not reported yet has an empty, non-nil window.
func TestServiceGet_RunnerWithoutSamplesHasEmptyRecentTelemetry(t *testing.T) {
	f := rdtFixture(t, &rdtWindow{byID: map[RunnerID][]Telemetry{BuiltInRunnerID: {rdtSample(0, 10)}}})
	d, err := f.svc.Get(svCtx(identity.RoleAdmin), "mac-a")
	require.NoError(t, err)
	require.NotNil(t, d.RecentTelemetry)
	assert.Empty(t, d.RecentTelemetry)
}

// Edge: without a window (Phase 1 wiring) the list is empty, never zeros.
func TestServiceGet_NilWindowLeavesRecentTelemetryEmpty(t *testing.T) {
	f := rdtFixture(t, nil)
	d, err := f.svc.Get(svCtx(identity.RoleUser), BuiltInRunnerID)
	require.NoError(t, err)
	require.NotNil(t, d.RecentTelemetry)
	assert.Empty(t, d.RecentTelemetry)
	assert.Len(t, d.Assignments, 2)
}

// Failure + error: an unauthenticated caller and an unknown runner fail
// before the window is read.
func TestServiceGet_FailuresNeverReadTheWindow(t *testing.T) {
	w := &rdtWindow{byID: map[RunnerID][]Telemetry{BuiltInRunnerID: {rdtSample(0, 10)}}}
	f := rdtFixture(t, w)

	_, err := f.svc.Get(context.Background(), BuiltInRunnerID)
	assert.ErrorIs(t, err, identity.ErrUnauthenticated)
	_, err = f.svc.Get(svCtx(identity.RoleUser), "laptop")
	assert.ErrorIs(t, err, ErrRunnerNotFound)
	assert.Empty(t, w.askedIDs())
}

// Edge: the detail owns its samples; a caller editing them cannot corrupt
// the producer's window.
func TestServiceGet_RecentTelemetryIsACopy(t *testing.T) {
	w := &rdtAliasWindow{samples: []Telemetry{rdtSample(0, 10), rdtSample(time.Second, 20)}}
	f := rdtFixture(t, w)
	d, err := f.svc.Get(svCtx(identity.RoleUser), BuiltInRunnerID)
	require.NoError(t, err)
	d.RecentTelemetry[0].SampledAt = time.Time{}
	assert.True(t, w.samples[0].SampledAt.Equal(rdtT0), "the producer's window is untouched")
}
