package runner

import (
	"context"
	"sync/atomic"
	"time"
)

// MachineTelemetry samples the local machine. Every Telemetry metric is a
// pointer: nil means "unavailable" and is never replaced by a healthy guess.
type MachineTelemetry interface {
	Sample(ctx context.Context) (Telemetry, error)
}

// telemetrySink is the part of *Hub the built-in loop feeds.
type telemetrySink interface {
	RecordTelemetry(id RunnerID, t Telemetry)
}

// The hub must accept built-in samples into the same window heartbeats feed.
var _ telemetrySink = (*Hub)(nil)

const defaultBuiltInTelemetryEvery = 5 * time.Second

// BuiltInTelemetryLoop samples the server machine and records it for the
// built-in runner, so the hub window holds built-in telemetry exactly like a
// remote runner's heartbeats.
type BuiltInTelemetryLoop struct {
	src     MachineTelemetry
	sink    telemetrySink
	every   time.Duration
	now     func() time.Time
	after   func(time.Duration) <-chan time.Time
	started atomic.Bool
}

// NewBuiltInTelemetryLoop builds the loop feeding h. It panics on a nil
// sampler or hub: both are composition-root programming errors.
func NewBuiltInTelemetryLoop(t MachineTelemetry, h *Hub, every time.Duration) *BuiltInTelemetryLoop {
	if h == nil {
		panic("runner: NewBuiltInTelemetryLoop requires a non-nil *Hub")
	}
	return newBuiltInTelemetryLoop(t, h, every, time.Now, time.After)
}

func newBuiltInTelemetryLoop(t MachineTelemetry, sink telemetrySink, every time.Duration,
	now func() time.Time, after func(time.Duration) <-chan time.Time) *BuiltInTelemetryLoop {
	if t == nil {
		panic("runner: built-in telemetry loop requires a MachineTelemetry")
	}
	if sink == nil {
		panic("runner: built-in telemetry loop requires a sink")
	}
	if every <= 0 {
		every = defaultBuiltInTelemetryEvery
	}
	return &BuiltInTelemetryLoop{src: t, sink: sink, every: every, now: now, after: after}
}

// Every is the sampling interval.
func (l *BuiltInTelemetryLoop) Every() time.Duration { return l.every }

// Tick samples once and records the result for BuiltInRunnerID. A sampling
// error records an all-unavailable sample stamped now and is returned. Samples
// without a producer timestamp also carry no metric evidence.
func (l *BuiltInTelemetryLoop) Tick(ctx context.Context) error {
	s, err := l.src.Sample(ctx)
	if err != nil {
		s = Telemetry{}
	}
	if s.SampledAt.IsZero() {
		s = Telemetry{SampledAt: l.now()}
	}
	l.sink.RecordTelemetry(BuiltInRunnerID, s)
	return err
}

// Start samples immediately and then every Every() until ctx is done. A
// second call is a no-op.
func (l *BuiltInTelemetryLoop) Start(ctx context.Context) {
	if !l.started.CompareAndSwap(false, true) {
		return
	}
	go func() {
		for ctx.Err() == nil {
			_ = l.Tick(ctx)
			select {
			case <-ctx.Done():
				return
			case <-l.after(l.every):
			}
		}
	}()
}
