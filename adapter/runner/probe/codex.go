package probe

import (
	"context"
	"time"

	"github.com/arbitron/arbitron/adapter/codex"
	"github.com/arbitron/arbitron/domain/runner"
)

// CodexProber: `codex login status` (via codex.Readiness) for auth, then the
// token-free account/rateLimits/read for capacity.
type CodexProber struct {
	cfg        codex.Config
	readLimits func(context.Context, codex.Config) (codex.RateLimitsSnapshot, error) // seam; codex.RateLimitsRead in production
}

func NewCodexProber(cfg codex.Config) *CodexProber {
	return &CodexProber{cfg: cfg, readLimits: codex.RateLimitsRead}
}

func (p *CodexProber) Probe(ctx context.Context, s runner.Source) (runner.ProbeResult, error) {
	if s.Kind != runner.CredOpenAISubscription {
		return runner.ProbeResult{Auth: runner.AuthUnknown}, nil
	}
	st, err := codex.Readiness(ctx, p.cfg)
	if err != nil {
		return runner.ProbeResult{Auth: runner.AuthUnknown}, nil
	}
	switch st {
	case codex.NotLoggedIn:
		return runner.ProbeResult{Auth: runner.AuthLoggedOut}, nil
	case codex.NotInstalled:
		return runner.ProbeResult{Auth: runner.AuthUnknown}, nil
	}
	res := runner.ProbeResult{Auth: runner.AuthOK}
	snap, err := p.readLimits(ctx, p.cfg)
	if err != nil {
		// ErrRateLimitsUnavailable (id-1 error incl. -32600, EOF, timeout,
		// garbage): capacity unknown, never "available", never logged-out.
		return res, nil
	}
	obs := &runner.CapacityObservation{OrdinaryUsageAllowed: snap.OrdinaryUsageAllowed, Windows: snap.Windows, Headroom: headroomOf(snap.Windows)}
	if oua := snap.OrdinaryUsageAllowed; oua != nil {
		if *oua {
			obs.State = runner.CapacityAvailable
		} else {
			obs.State = runner.CapacityExhausted
			obs.ResetAt = latestExhaustedReset(snap.Windows)
		}
	}
	res.Capacity = obs
	return res, nil
}

// headroomOf = 1 - (highest used% / 100); nil when no window reports usage.
func headroomOf(ws []runner.LimitWindow) *float64 {
	var maxUsed *float64
	for _, w := range ws {
		if w.UsedPercent != nil && (maxUsed == nil || *w.UsedPercent > *maxUsed) {
			v := *w.UsedPercent
			maxUsed = &v
		}
	}
	if maxUsed == nil {
		return nil
	}
	h := 1 - *maxUsed/100
	if h < 0 {
		h = 0
	}
	if h > 1 {
		h = 1
	}
	return &h
}

func latestExhaustedReset(ws []runner.LimitWindow) *time.Time {
	var out *time.Time
	for _, w := range ws {
		if w.UsedPercent != nil && *w.UsedPercent >= 100 && w.ResetAt != nil && (out == nil || w.ResetAt.After(*out)) {
			t := *w.ResetAt
			out = &t
		}
	}
	return out
}
