# Phase 3 Task 3: Structured Logging & OpenTelemetry Tracing

> **Historical note (added 2026-06-02):** This document predates Arbitron's migration from Postgres to SQLite. All Postgres references below are obsolete — the live persistence layer is the SQLite adapter (`adapter/sqlite/`, configured via `database.path`, default `./arbitron.db`). Retained as a historical record.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement structured logging (slog with dual JSON/text handlers) and OpenTelemetry tracing (with file exporter), plus request logging middleware and engine span instrumentation.

**Architecture:** Logging and tracing setup live in `adapter/observability/`. The MultiHandler fans out log records to both a JSON file handler and a text stdout handler. OTel tracing uses the global tracer provider with a file exporter.

**Tech Stack:** Go 1.23+, log/slog, OpenTelemetry Go SDK

---

### Task 1: Structured Logging

**Files:**
- Create: `adapter/observability/logging.go`
- Create: `adapter/observability/logging_test.go`

- [ ] **Step 1: Write failing tests for logging setup**

Create `adapter/observability/logging_test.go`:

```go
package observability_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/observability"
	"github.com/arbitron/arbitron/domain/config"
)

func TestSetupLogging_CreatesLogFile(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "test.log")

	cfg := config.LoggingConfig{Level: "info", File: logFile}
	logger, err := observability.SetupLogging(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	logger.Info("test message")

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}

	var logEntry map[string]any
	if err := json.Unmarshal(data, &logEntry); err != nil {
		t.Fatalf("log file should contain JSON: %v, content: %s", err, string(data))
	}

	if msg, ok := logEntry["msg"].(string); !ok || msg != "test message" {
		t.Errorf("expected 'test message' in log, got %v", logEntry["msg"])
	}
}

func TestSetupLogging_LevelFiltering(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "test.log")

	cfg := config.LoggingConfig{Level: "warn", File: logFile}
	logger, err := observability.SetupLogging(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	logger.Info("should not appear")
	logger.Warn("should appear")

	data, _ := os.ReadFile(logFile)
	content := string(data)

	if strings.Contains(content, "should not appear") {
		t.Error("info message should be filtered at warn level")
	}
	if !strings.Contains(content, "should appear") {
		t.Error("warn message should appear at warn level")
	}
}

func TestSetupLogging_CreatesParentDirectories(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "deep", "nested", "test.log")

	cfg := config.LoggingConfig{Level: "info", File: logFile}
	_, err := observability.SetupLogging(cfg)
	if err != nil {
		t.Fatalf("should create parent directories: %v", err)
	}

	if _, statErr := os.Stat(logFile); os.IsNotExist(statErr) {
		t.Error("log file should exist")
	}
}

func TestMultiHandler_FansOut(t *testing.T) {
	var buf1, buf2 bytes.Buffer

	h1 := slog.NewJSONHandler(&buf1, nil)
	h2 := slog.NewTextHandler(&buf2, nil)
	multi := observability.NewMultiHandler(h1, h2)

	logger := slog.New(multi)
	logger.Info("dual output")

	if !strings.Contains(buf1.String(), "dual output") {
		t.Error("expected JSON handler to receive message")
	}
	if !strings.Contains(buf2.String(), "dual output") {
		t.Error("expected text handler to receive message")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./adapter/observability/ -v
```

Expected: FAIL

- [ ] **Step 3: Implement logging setup**

Create `adapter/observability/logging.go`:

```go
package observability

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/arbitron/arbitron/domain/config"
)

func SetupLogging(cfg config.LoggingConfig) (*slog.Logger, error) {
	level := parseLevel(cfg.Level)

	if err := os.MkdirAll(filepath.Dir(cfg.File), 0755); err != nil {
		return nil, err
	}

	logFile, err := os.OpenFile(cfg.File, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}

	jsonHandler := slog.NewJSONHandler(logFile, &slog.HandlerOptions{Level: level})
	textHandler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	multi := NewMultiHandler(jsonHandler, textHandler)

	logger := slog.New(multi)
	slog.SetDefault(logger)

	return logger, nil
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

type MultiHandler struct {
	handlers []slog.Handler
}

func NewMultiHandler(handlers ...slog.Handler) *MultiHandler {
	return &MultiHandler{handlers: handlers}
}

func (h *MultiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h *MultiHandler) Handle(ctx context.Context, record slog.Record) error {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, record.Level) {
			if err := handler.Handle(ctx, record); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *MultiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		handlers[i] = handler.WithAttrs(attrs)
	}
	return &MultiHandler{handlers: handlers}
}

func (h *MultiHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		handlers[i] = handler.WithGroup(name)
	}
	return &MultiHandler{handlers: handlers}
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./adapter/observability/ -v
```

Expected: all PASS

- [ ] **Step 5: Commit**

```bash
git add adapter/observability/logging*
git commit -m "feat: structured logging with slog MultiHandler"
```

---

### Task 2: Request Logging Middleware

**Files:**
- Create: `adapter/http/middleware.go`
- Create: `adapter/http/middleware_test.go`

- [ ] **Step 6: Write failing tests for request logging middleware**

Create `adapter/http/middleware_test.go`:

```go
package http_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
)

func TestRequestLoggingMiddleware(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	handler := adapthttp.RequestLoggingMiddleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))

	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	output := buf.String()
	if !strings.Contains(output, "GET") {
		t.Error("expected method in log")
	}
	if !strings.Contains(output, "/api/v1/health") {
		t.Error("expected path in log")
	}
	if !strings.Contains(output, "200") {
		t.Error("expected status in log")
	}
}
```

- [ ] **Step 7: Implement middleware**

Create `adapter/http/middleware.go`:

```go
package http

import (
	"log/slog"
	"net/http"
	"time"
)

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func RequestLoggingMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: 200}

			next.ServeHTTP(rec, r)

			duration := time.Since(start)
			level := slog.LevelInfo
			if rec.status >= 500 {
				level = slog.LevelError
			} else if rec.status >= 400 {
				level = slog.LevelWarn
			}

			logger.Log(r.Context(), level, "http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"duration_ms", duration.Milliseconds(),
			)
		})
	}
}
```

- [ ] **Step 8: Run tests**

```bash
go test ./adapter/http/ -v -run TestRequestLogging
```

Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add adapter/http/middleware*
git commit -m "feat: request logging middleware"
```

---

### Task 3: OpenTelemetry Tracing

**Files:**
- Create: `adapter/observability/tracing.go`
- Create: `adapter/observability/tracing_test.go`

- [ ] **Step 10: Install OpenTelemetry dependencies**

```bash
go get go.opentelemetry.io/otel
go get go.opentelemetry.io/otel/sdk
go get go.opentelemetry.io/otel/sdk/trace
go get go.opentelemetry.io/otel/exporters/stdout/stdouttrace
```

- [ ] **Step 11: Write failing tests for tracing setup**

Create `adapter/observability/tracing_test.go`:

```go
package observability_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"go.opentelemetry.io/otel"

	"github.com/arbitron/arbitron/adapter/observability"
	"github.com/arbitron/arbitron/domain/config"
)

func TestSetupTracing_Enabled(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := config.TracingConfig{Enabled: true, ExportDir: tmpDir}

	shutdown, err := observability.SetupTracing(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer shutdown(context.Background())

	tracer := otel.Tracer("test")
	_, span := tracer.Start(context.Background(), "test-span")
	span.End()

	// Flush
	shutdown(context.Background())

	// Verify trace file was written
	entries, _ := os.ReadDir(tmpDir)
	if len(entries) == 0 {
		t.Error("expected trace file in export directory")
	}
}

func TestSetupTracing_Disabled(t *testing.T) {
	cfg := config.TracingConfig{Enabled: false, ExportDir: t.TempDir()}

	shutdown, err := observability.SetupTracing(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should be a no-op shutdown
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("no-op shutdown should not error: %v", err)
	}
}
```

- [ ] **Step 12: Implement tracing setup**

Create `adapter/observability/tracing.go`:

```go
package observability

import (
	"context"
	"os"
	"path/filepath"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"

	"github.com/arbitron/arbitron/domain/config"
)

func SetupTracing(cfg config.TracingConfig) (func(context.Context) error, error) {
	if !cfg.Enabled {
		return func(ctx context.Context) error { return nil }, nil
	}

	if err := os.MkdirAll(cfg.ExportDir, 0755); err != nil {
		return nil, err
	}

	traceFile, err := os.Create(filepath.Join(cfg.ExportDir, "traces.jsonl"))
	if err != nil {
		return nil, err
	}

	exporter, err := stdouttrace.New(stdouttrace.WithWriter(traceFile))
	if err != nil {
		traceFile.Close()
		return nil, err
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName("arbitron"),
		),
	)
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)

	otel.SetTracerProvider(tp)

	shutdown := func(ctx context.Context) error {
		err := tp.Shutdown(ctx)
		traceFile.Close()
		return err
	}

	return shutdown, nil
}
```

- [ ] **Step 13: Run tests**

```bash
go test ./adapter/observability/ -v
```

Expected: all PASS

- [ ] **Step 14: Commit**

```bash
git add adapter/observability/tracing*
git commit -m "feat: OpenTelemetry tracing with file exporter"
```

---

### Task 4: Wire Everything Into Bootstrap

**Files:**
- Modify: `cmd/arbitron/main.go`

- [ ] **Step 15: Update main.go to wire all components**

Update `cmd/arbitron/main.go` to replace all TODO markers with actual adapter creation and wiring. The serve command should now:

1. Load config
2. Set up logging (adapter/observability)
3. Connect to Postgres, run migrations _(Postgres has since been replaced by SQLite.)_
4. Initialize sqlc + create Postgres adapters _(Postgres has since been replaced by SQLite.)_
5. Create filesystem workflow repo + file reader
6. Create Anthropic provider
7. Create event bus
8. Create all node executors (inject file reader + LLM provider)
9. Create engine (inject all ports)
10. Recover crashed instances
11. Set up OTel tracing
12. Create HTTP handlers + router
13. Wire request logging middleware into router
14. Start HTTP server
15. Graceful shutdown (flush OTel, close DB pool)

- [ ] **Step 16: Verify it compiles**

```bash
go build ./cmd/arbitron/
```

Expected: builds successfully

- [ ] **Step 17: Commit**

```bash
git add cmd/arbitron/main.go
git commit -m "feat: wire all components into bootstrap"
```

---

### Task 5: End-to-End Integration Test

**Files:**
- Create: `integration_test.go` (at project root)

- [ ] **Step 18: Write an end-to-end integration test**

Create an integration test that:
1. Starts a Postgres testcontainer _(Postgres has since been replaced by SQLite.)_
2. Runs migrations
3. Creates a test workflow folder on disk (a simple transform-only workflow)
4. Boots the full application (creates all adapters, engine, router)
5. Triggers the workflow via HTTP POST
6. Polls GET /instances/{id} until completed
7. Verifies the execution trace via GET /instances/{id}/trace
8. Verifies variable history via GET /instances/{id}/variables/history
9. Tests the full lifecycle: trigger → complete, then re-run

- [ ] **Step 19: Run integration test**

```bash
go test -v -tags integration -timeout 120s -run TestEndToEnd
```

Expected: PASS

- [ ] **Step 20: Final commit**

```bash
git add integration_test.go
git commit -m "test: end-to-end integration test"
```
