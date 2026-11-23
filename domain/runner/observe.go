package runner

import "context"

type outcomeObserverKey struct{}

// ContextWithOutcomeObserver installs fn to receive success-path observations.
func ContextWithOutcomeObserver(ctx context.Context, fn func(SourceOutcome)) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, outcomeObserverKey{}, fn)
}

// ObserveOutcome reports a success-path observation to the installed observer.
func ObserveOutcome(ctx context.Context, o SourceOutcome) {
	if fn, ok := ctx.Value(outcomeObserverKey{}).(func(SourceOutcome)); ok && fn != nil {
		fn(o)
	}
}
