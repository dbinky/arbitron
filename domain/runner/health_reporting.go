package runner

import (
	"context"
	"log/slog"
	"sync"

	"github.com/arbitron/arbitron/domain/engine"
)

// NewHealthReportingRunnerSet wraps every StepRunner so a SUCCESSFUL model step
// reports OutcomeNone (plus any adapter headroom hint) to h. This is what moves
// a probation source to available after one successful step. Failures stay the
// dispatcher's job (it classifies and applies them); process jobs hold a slot
// only and carry no source health.
func NewHealthReportingRunnerSet(inner RunnerSet, h HealthTransitioner) RunnerSet {
	if inner == nil || h == nil {
		panic("runner: NewHealthReportingRunnerSet requires an inner set and a transitioner")
	}
	return healthReportingSet{inner: inner, h: h}
}

type healthReportingSet struct {
	inner RunnerSet
	h     HealthTransitioner
}

func (s healthReportingSet) For(id RunnerID) (StepRunner, bool) {
	r, ok := s.inner.For(id)
	if !ok {
		return nil, false
	}
	return healthReportingRunner{inner: r, h: s.h}, true
}

type healthReportingRunner struct {
	inner StepRunner
	h     HealthTransitioner
}

func (r healthReportingRunner) Run(ctx context.Context, a Assignment, job StepJob) (StepOutput, error) {
	if job.Op == OpProcess || a.Job.SourceID == "" {
		return r.inner.Run(ctx, a, job)
	}
	var mu sync.Mutex
	var seen SourceOutcome
	obsCtx := ContextWithOutcomeObserver(ctx, func(o SourceOutcome) {
		mu.Lock()
		defer mu.Unlock()
		if o.HeadroomHint != nil {
			seen.HeadroomHint = o.HeadroomHint
		}
		if len(o.Windows) > 0 {
			seen.Windows = o.Windows
		}
	})
	runCtx, cancel := context.WithCancel(obsCtx)
	out, err := r.inner.Run(runCtx, a, job)
	if err != nil {
		cancel()
		return out, err
	}
	if job.Op == OpConfirm {
		if verr := VerifyConfirmation(job.Binding.Model, out.Confirmation); verr != nil {
			cancel()
			return StepOutput{}, WithOutcome(verr, SourceOutcome{Class: OutcomeOther})
		}
	}
	report := func() {
		mu.Lock()
		o := SourceOutcome{Class: OutcomeNone, Model: job.Binding.Model, HeadroomHint: seen.HeadroomHint, Windows: seen.Windows}
		mu.Unlock()
		if herr := r.h.ApplyOutcome(context.WithoutCancel(ctx), a.Job.SourceID, o); herr != nil {
			slog.Warn("runner success health report failed", "source", string(a.Job.SourceID), "error", herr)
		}
	}
	if out.Stream == nil {
		defer cancel()
		report()
		return out, nil
	}
	fwd := make(chan engine.ChatChunk)
	src := out.Stream
	go func() {
		defer close(fwd)
		defer cancel()
		failed := false
		for {
			var c engine.ChatChunk
			var ok bool
			select {
			case c, ok = <-src:
				if !ok {
					return
				}
			case <-ctx.Done():
				return
			}
			if c.Error != nil {
				failed = true
			}
			if c.Done && !failed {
				report()
			}
			select {
			case fwd <- c:
			case <-ctx.Done():
				return
			}
			if c.Done {
				return
			}
		}
	}()
	out.Stream = fwd
	return out, nil
}
