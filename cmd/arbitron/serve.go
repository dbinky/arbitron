package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// shutdownTimeout bounds the graceful shutdown of both listeners.
const shutdownTimeout = 15 * time.Second

// serveHTTP runs the main HTTP(S) server and, in autocert mode, the ACME
// challenge server, then shuts both down gracefully. It blocks until either the
// stop channel is closed (signal-driven shutdown) or a listener fails to bind /
// serve, whichever comes first.
//
// Both listeners are started in their own top-level goroutine (no
// goroutine-in-goroutine) and both push a non-http.ErrServerClosed error onto a
// shared error channel, so a permission-denied on :80 (a non-root autocert
// deployment) aborts startup instead of leaving a tls=true process that can
// never provision. Both are shut down together so neither listener leaks.
//
// challengeSrv may be nil (cert/key or disabled mode); when present it is the
// ACME HTTP-01 server produced by buildTLSConfig.
func serveHTTP(ctx context.Context, logger *slog.Logger, srv, challengeSrv *http.Server, stop <-chan struct{}) error {
	// Buffered for both potential producers so a send never blocks after the
	// select has already returned.
	errCh := make(chan error, 2)

	if challengeSrv != nil {
		go func() {
			logger.Info("ACME challenge server starting", "addr", challengeSrv.Addr)
			if err := challengeSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("ACME challenge server error: %w", err)
			}
		}()
	}

	go func() {
		serveTLS := srv.TLSConfig != nil
		logger.Info("server starting", "addr", srv.Addr, "tls", serveTLS)
		var err error
		if serveTLS {
			// Cert/key (or autocert) come from srv.TLSConfig, so the file
			// arguments are empty.
			err = srv.ListenAndServeTLS("", "")
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("server error: %w", err)
		}
	}()

	var serveErr error
	select {
	case <-stop:
	case <-ctx.Done():
	case serveErr = <-errCh:
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	logger.Info("shutting down server")
	if challengeSrv != nil {
		if err := challengeSrv.Shutdown(shutdownCtx); err != nil {
			logger.Error("ACME challenge server shutdown", "error", err)
		}
	}
	if err := srv.Shutdown(shutdownCtx); err != nil {
		// Prefer the original serve error, if any, over a shutdown error.
		if serveErr == nil {
			serveErr = fmt.Errorf("server shutdown: %w", err)
		}
	}

	if serveErr != nil {
		return serveErr
	}
	logger.Info("server stopped gracefully")
	return nil
}
