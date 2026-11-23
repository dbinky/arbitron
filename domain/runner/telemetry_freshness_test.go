package runner

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBuiltInTelemetryLoopMissingTimestampDiscardsMetricEvidence(t *testing.T) {
	now := time.Now().UTC()
	sink := newTelemSink()
	m := &telemFakeMachine{samples: []Telemetry{{CPUPercent: telemF64(5)}}}
	l := newBuiltInTelemetryLoop(m, sink, time.Second, func() time.Time { return now }, time.After)
	require.NoError(t, l.Tick(context.Background()))
	require.Equal(t, Telemetry{SampledAt: now}, sink.next(t).t)
}
