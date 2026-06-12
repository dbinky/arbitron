package main

import (
	"context"
	"log/slog"

	"github.com/arbitron/arbitron/domain/provider"
)

// loggingReloader decorates a provider.Reloader so a recache failure after a
// durable write is WARN-logged instead of silently swallowed. The ProfileService
// runs recache best-effort (the write is already durable), but a swallowed
// failure leaves the in-memory cache the resolver and workflow-validation read
// STALE until the next boot — a silent split-brain on a live, credential-binding
// admin surface (a security-motivated repoint off a compromised/rotated
// connection would durably persist yet not take effect). The WARN log makes that
// staleness window observable.
//
// The decorator lives in the wiring layer so the domain ProfileService stays
// free of a concrete logger dependency: the Reloader port is already the seam.
type loggingReloader struct {
	inner  provider.Reloader
	logger *slog.Logger
}

// newLoggingReloader wraps inner so Reload failures are WARN-logged via logger.
func newLoggingReloader(inner provider.Reloader, logger *slog.Logger) *loggingReloader {
	return &loggingReloader{inner: inner, logger: logger}
}

// Reload delegates to the wrapped reloader and WARN-logs any failure. It returns
// the same value as the inner reloader so the ProfileService's best-effort
// semantics are unchanged (a durable write is never failed by a stale cache);
// the log is the observability floor the swallow lacked.
func (r *loggingReloader) Reload(ctx context.Context) error {
	if err := r.inner.Reload(ctx); err != nil {
		r.logger.WarnContext(ctx, "profile cache recache failed after a durable write; the engine resolves the STALE profile until the next reload/restart",
			slog.String("error", err.Error()))
		return nil
	}
	return nil
}
