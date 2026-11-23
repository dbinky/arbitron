package telemetry

import (
	"context"
	"time"

	"github.com/arbitron/arbitron/domain/runner"
)

// Null reports every metric as unavailable. It is only the fallback for
// operating systems without a sampler; it never fabricates healthy values.
type Null struct{}

var _ runner.MachineTelemetry = Null{}

// Sample returns a timestamp and no metrics.
func (Null) Sample(ctx context.Context) (runner.Telemetry, error) {
	return runner.Telemetry{SampledAt: time.Now().UTC()}, nil
}
