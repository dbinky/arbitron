package procenv

import (
	"context"
	"time"
)

// Probe limits are independent of the operator's ordinary execution budgets.
const (
	ProbeTimeout             = 30 * time.Second
	ProbeResponseBytes int64 = 1 << 20
)

type probeLimitsKey struct{}

// WithProbeResponseLimit carries the probe's read ceiling through the real
// provider factory to the HTTP/process readers, without changing ordinary run
// settings. The caller supplies the check's bounded context separately.
func WithProbeResponseLimit(ctx context.Context) context.Context {
	return context.WithValue(ctx, probeLimitsKey{}, true)
}

// ResponseLimit is the effective limit at an I/O reader. Ordinary calls retain
// their configured limit (including zero/unlimited).
func ResponseLimit(ctx context.Context, configured int64) int64 {
	if probe, _ := ctx.Value(probeLimitsKey{}).(bool); probe && (configured <= 0 || configured > ProbeResponseBytes) {
		return ProbeResponseBytes
	}
	return configured
}
