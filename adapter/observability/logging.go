// Package observability provides structured logging and tracing adapters for Arbitron.
package observability

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/arbitron/arbitron/domain/config"
)

// MultiHandler is an slog.Handler that fans out log records to multiple handlers.
type MultiHandler struct {
	handlers []slog.Handler
}

// NewMultiHandler creates a MultiHandler that dispatches to all provided handlers.
func NewMultiHandler(handlers ...slog.Handler) *MultiHandler {
	return &MultiHandler{handlers: handlers}
}

// Enabled returns true if any underlying handler is enabled for the given level.
func (m *MultiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

// Handle dispatches the record to all underlying handlers.
func (m *MultiHandler) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range m.handlers {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// WithAttrs returns a new MultiHandler with the given attributes added to each
// underlying handler.
func (m *MultiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		handlers[i] = h.WithAttrs(attrs)
	}
	return &MultiHandler{handlers: handlers}
}

// WithGroup returns a new MultiHandler with the given group name added to each
// underlying handler.
func (m *MultiHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		handlers[i] = h.WithGroup(name)
	}
	return &MultiHandler{handlers: handlers}
}

// parseLevel converts a level string to slog.Level.
func parseLevel(level string) (slog.Level, error) {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("unknown log level %q, defaulting to info", level)
	}
}

// SetupLogging configures structured logging with a MultiHandler that writes
// JSON to a log file and text to stdout. It sets the result as the default logger.
// The returned io.Closer should be closed on shutdown to flush the log file.
func SetupLogging(cfg config.LoggingConfig) (*slog.Logger, io.Closer, error) {
	level, err := parseLevel(cfg.Level)
	if err != nil {
		// Non-fatal: use the defaulted level and log a warning later.
		level = slog.LevelInfo
	}

	// Create parent directories for the log file.
	logDir := filepath.Dir(cfg.File)
	if logDir != "" && logDir != "." {
		if err := os.MkdirAll(logDir, 0755); err != nil {
			return nil, nil, fmt.Errorf("creating log directory %q: %w", logDir, err)
		}
	}

	// Open log file for append.
	f, err := os.OpenFile(cfg.File, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, nil, fmt.Errorf("opening log file %q: %w", cfg.File, err)
	}

	opts := &slog.HandlerOptions{Level: level}

	jsonHandler := slog.NewJSONHandler(f, opts)
	textHandler := slog.NewTextHandler(os.Stdout, opts)

	multi := NewMultiHandler(jsonHandler, textHandler)
	logger := slog.New(multi)
	slog.SetDefault(logger)

	return logger, f, nil
}
