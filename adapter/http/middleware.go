package http

import (
	"log/slog"
	"net/http"
	"time"
)

// statusRecorder wraps http.ResponseWriter to capture the status code.
type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func newStatusRecorder(w http.ResponseWriter) *statusRecorder {
	return &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.statusCode = code
	sr.ResponseWriter.WriteHeader(code)
}

// RequestLoggingMiddleware returns middleware that logs each HTTP request
// with method, path, status code, and duration. Log level is determined by
// the response status: info for 2xx/3xx, warn for 4xx, error for 5xx.
func RequestLoggingMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			rec := newStatusRecorder(w)
			next.ServeHTTP(rec, r)

			duration := time.Since(start)
			attrs := []slog.Attr{
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.statusCode),
				slog.Float64("duration_ms", float64(duration.Microseconds())/1000.0),
			}

			msg := "HTTP request"
			switch {
			case rec.statusCode >= 500:
				logger.LogAttrs(r.Context(), slog.LevelError, msg, attrs...)
			case rec.statusCode >= 400:
				logger.LogAttrs(r.Context(), slog.LevelWarn, msg, attrs...)
			default:
				logger.LogAttrs(r.Context(), slog.LevelInfo, msg, attrs...)
			}
		})
	}
}
