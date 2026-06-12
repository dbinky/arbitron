package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
)

// stubReloader is a provider.Reloader whose Reload returns a fixed error.
type stubReloader struct {
	err   error
	calls int
}

func (s *stubReloader) Reload(context.Context) error { s.calls++; return s.err }

// loggingReloader must delegate to the wrapped reloader and, on a Reload failure,
// WARN-log so a swallowed recache failure (the split-brain on the live admin
// surface) is observable rather than silent. It must NOT turn a reload failure
// into an error the caller sees — the write is already durable.
func TestLoggingReloader_WarnsOnReloadFailure(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	inner := &stubReloader{err: errors.New("db gone")}

	rl := newLoggingReloader(inner, logger)
	err := rl.Reload(context.Background())

	if err != nil {
		t.Fatalf("loggingReloader.Reload must not surface the error to the caller, got %v", err)
	}
	if inner.calls != 1 {
		t.Fatalf("expected the wrapped reloader to be called once, got %d", inner.calls)
	}
	out := buf.String()
	if !strings.Contains(strings.ToUpper(out), "WARN") {
		t.Fatalf("expected a WARN log on reload failure, got: %q", out)
	}
	if !strings.Contains(out, "db gone") {
		t.Fatalf("expected the underlying error in the log, got: %q", out)
	}
}

// On success it delegates and logs nothing at WARN.
func TestLoggingReloader_QuietOnSuccess(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	inner := &stubReloader{err: nil}

	rl := newLoggingReloader(inner, logger)
	if err := rl.Reload(context.Background()); err != nil {
		t.Fatalf("unexpected error on a clean reload: %v", err)
	}
	if inner.calls != 1 {
		t.Fatalf("expected the wrapped reloader to be called once, got %d", inner.calls)
	}
	if strings.Contains(strings.ToUpper(buf.String()), "WARN") {
		t.Fatalf("expected no WARN log on a clean reload, got: %q", buf.String())
	}
}

var _ provider.Reloader = (*loggingReloader)(nil)
