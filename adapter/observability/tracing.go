package observability

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"

	"github.com/arbitron/arbitron/domain/config"
)

// TracingShutdown bundles the shutdown function and the trace file closer.
type TracingShutdown struct {
	provider *sdktrace.TracerProvider
	file     *os.File
}

// Shutdown flushes all pending spans and closes the trace file.
func (ts *TracingShutdown) Shutdown(ctx context.Context) error {
	if ts == nil || ts.provider == nil {
		return nil
	}
	err := ts.provider.Shutdown(ctx)
	if ts.file != nil {
		if closeErr := ts.file.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}
	return err
}

// SetupTracing configures OpenTelemetry tracing with a stdout JSON exporter that
// writes to a file in the configured export directory. If tracing is not enabled,
// it returns a no-op shutdown function.
func SetupTracing(cfg config.TracingConfig) (func(context.Context) error, error) {
	if !cfg.Enabled {
		return func(context.Context) error { return nil }, nil
	}

	// Create export directory.
	if err := os.MkdirAll(cfg.ExportDir, 0755); err != nil {
		return nil, fmt.Errorf("creating trace export directory %q: %w", cfg.ExportDir, err)
	}

	// Open trace file.
	tracePath := filepath.Join(cfg.ExportDir, "traces.jsonl")
	f, err := os.OpenFile(tracePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("opening trace file %q: %w", tracePath, err)
	}

	// Create stdout exporter writing to the file.
	exporter, err := stdouttrace.New(stdouttrace.WithWriter(f))
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("creating stdout trace exporter: %w", err)
	}

	// Create resource with service name.
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName("arbitron"),
		),
	)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("creating trace resource: %w", err)
	}

	// Create tracer provider with batch span processor.
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)

	// Set as global tracer provider.
	otel.SetTracerProvider(tp)

	ts := &TracingShutdown{
		provider: tp,
		file:     f,
	}

	return ts.Shutdown, nil
}
