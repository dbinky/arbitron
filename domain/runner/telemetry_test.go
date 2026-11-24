package runner

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type telemFakeMachine struct {
	mu      sync.Mutex
	samples []Telemetry
	errs    []error
	calls   int
}

func (m *telemFakeMachine) Sample(ctx context.Context) (Telemetry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := m.calls
	m.calls++
	var s Telemetry
	var err error
	if i < len(m.samples) {
		s = m.samples[i]
	}
	if i < len(m.errs) {
		err = m.errs[i]
	}
	return s, err
}

type telemRecord struct {
	id RunnerID
	t  Telemetry
}

type telemFakeSink struct{ ch chan telemRecord }

func newTelemSink() *telemFakeSink { return &telemFakeSink{ch: make(chan telemRecord, 16)} }

func (s *telemFakeSink) RecordTelemetry(id RunnerID, t Telemetry) { s.ch <- telemRecord{id: id, t: t} }

func (s *telemFakeSink) next(t *testing.T) telemRecord {
	t.Helper()
	select {
	case r := <-s.ch:
		return r
	case <-time.After(2 * time.Second):
		t.Fatal("no telemetry recorded")
		return telemRecord{}
	}
}

func (s *telemFakeSink) none(t *testing.T) {
	t.Helper()
	select {
	case r := <-s.ch:
		t.Fatalf("unexpected telemetry recorded: %+v", r)
	case <-time.After(50 * time.Millisecond):
	}
}

// telemFakeTimer gates time: the loop blocks on fire until the test sends.
type telemFakeTimer struct {
	asked chan time.Duration
	fire  chan time.Time
}

func newTelemTimer() *telemFakeTimer {
	return &telemFakeTimer{asked: make(chan time.Duration, 16), fire: make(chan time.Time)}
}

func (f *telemFakeTimer) after(d time.Duration) <-chan time.Time {
	f.asked <- d
	return f.fire
}

func (f *telemFakeTimer) expectAsked(t *testing.T) time.Duration {
	t.Helper()
	select {
	case d := <-f.asked:
		return d
	case <-time.After(2 * time.Second):
		t.Fatal("loop never waited for its interval")
		return 0
	}
}

func telemF64(v float64) *float64 { return &v }

func TestBuiltInTelemetryLoopTickRecordsSampleForBuiltInRunner(t *testing.T) {
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	m := &telemFakeMachine{samples: []Telemetry{{SampledAt: at, CPUPercent: telemF64(37.5)}}}
	sink := newTelemSink()
	l := newBuiltInTelemetryLoop(m, sink, 5*time.Second, func() time.Time { return at.Add(time.Hour) }, newTelemTimer().after)

	require.NoError(t, l.Tick(context.Background()))

	r := sink.next(t)
	assert.Equal(t, BuiltInRunnerID, r.id)
	require.NotNil(t, r.t.CPUPercent)
	assert.Equal(t, 37.5, *r.t.CPUPercent)
	assert.Equal(t, at, r.t.SampledAt, "the sampler's own timestamp is kept")
}

func TestBuiltInTelemetryLoopTickErrorRecordsUnavailable(t *testing.T) {
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	boom := errors.New("sysctl failed")
	m := &telemFakeMachine{samples: []Telemetry{{CPUPercent: telemF64(1)}}, errs: []error{boom}}
	sink := newTelemSink()
	l := newBuiltInTelemetryLoop(m, sink, time.Second, func() time.Time { return now }, newTelemTimer().after)

	err := l.Tick(context.Background())
	assert.ErrorIs(t, err, boom)
	r := sink.next(t)
	assert.Equal(t, BuiltInRunnerID, r.id)
	assert.Equal(t, Telemetry{SampledAt: now}, r.t, "a failed sample is all-unavailable, never partial or healthy")
}

func TestBuiltInTelemetryLoopStampsZeroSampledAt(t *testing.T) {
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	m := &telemFakeMachine{samples: []Telemetry{{CPUPercent: telemF64(5)}}}
	sink := newTelemSink()
	l := newBuiltInTelemetryLoop(m, sink, time.Second, func() time.Time { return now }, newTelemTimer().after)
	require.NoError(t, l.Tick(context.Background()))
	assert.Equal(t, now, sink.next(t).t.SampledAt)
}

func TestBuiltInTelemetryLoopStartSamplesImmediatelyThenEveryInterval(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	m := &telemFakeMachine{samples: []Telemetry{{SampledAt: t0, CPUPercent: telemF64(1)}, {SampledAt: t0.Add(7 * time.Second), CPUPercent: telemF64(2)}, {SampledAt: t0.Add(14 * time.Second), CPUPercent: telemF64(3)}}}
	sink := newTelemSink()
	timer := newTelemTimer()
	l := newBuiltInTelemetryLoop(m, sink, 7*time.Second, time.Now, timer.after)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	l.Start(ctx)
	first := sink.next(t)
	assert.Equal(t, 1.0, *first.t.CPUPercent, "first sample is taken immediately")
	assert.Equal(t, 7*time.Second, timer.expectAsked(t))
	sink.none(t) // nothing until the interval elapses

	timer.fire <- time.Time{}
	second := sink.next(t)
	assert.Equal(t, 2.0, *second.t.CPUPercent)
	assert.Equal(t, 7*time.Second, timer.expectAsked(t))

	cancel()
	sink.none(t)
}

func TestBuiltInTelemetryLoopStartIsIdempotent(t *testing.T) {
	m := &telemFakeMachine{samples: []Telemetry{{CPUPercent: telemF64(1)}, {CPUPercent: telemF64(2)}}}
	sink := newTelemSink()
	timer := newTelemTimer()
	l := newBuiltInTelemetryLoop(m, sink, time.Second, time.Now, timer.after)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l.Start(ctx)
	l.Start(ctx)
	sink.next(t)
	sink.none(t)
}

func TestBuiltInTelemetryLoopDefaultsInterval(t *testing.T) {
	l := newBuiltInTelemetryLoop(&telemFakeMachine{}, newTelemSink(), 0, time.Now, time.After)
	assert.Equal(t, 5*time.Second, l.Every())
}

func TestNewBuiltInTelemetryLoopPanicsWithoutDependencies(t *testing.T) {
	assert.Panics(t, func() { NewBuiltInTelemetryLoop(&telemFakeMachine{}, nil, time.Second) })
	assert.Panics(t, func() { newBuiltInTelemetryLoop(nil, newTelemSink(), time.Second, time.Now, time.After) })
}
