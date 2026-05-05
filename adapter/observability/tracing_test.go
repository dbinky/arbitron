package observability

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.opentelemetry.io/otel"

	"github.com/arbitron/arbitron/domain/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetupTracing_Enabled_CreatesTraceFile(t *testing.T) {
	dir := t.TempDir()

	cfg := config.TracingConfig{
		Enabled:   true,
		ExportDir: filepath.Join(dir, "traces"),
	}

	shutdown, err := SetupTracing(cfg)
	require.NoError(t, err)
	require.NotNil(t, shutdown)

	// Create a span to generate trace data.
	tracer := otel.Tracer("test")
	_, span := tracer.Start(context.Background(), "test-span")
	span.End()

	// Give the batch processor a moment to flush.
	time.Sleep(100 * time.Millisecond)

	// Shutdown flushes pending spans.
	err = shutdown(context.Background())
	require.NoError(t, err)

	// Verify trace file was created and contains data.
	tracePath := filepath.Join(dir, "traces", "traces.jsonl")
	data, err := os.ReadFile(tracePath)
	require.NoError(t, err)
	assert.NotEmpty(t, data)
	assert.Contains(t, string(data), "test-span")
}

func TestSetupTracing_Disabled_NoOp(t *testing.T) {
	cfg := config.TracingConfig{
		Enabled:   false,
		ExportDir: "",
	}

	shutdown, err := SetupTracing(cfg)
	require.NoError(t, err)
	require.NotNil(t, shutdown)

	// Should not error on shutdown.
	err = shutdown(context.Background())
	assert.NoError(t, err)
}

func TestTracingShutdown_NilProvider_ReturnsNil(t *testing.T) {
	ts := &TracingShutdown{}
	err := ts.Shutdown(context.Background())
	assert.NoError(t, err)
}

func TestSetupTracing_Enabled_ExportsSpans(t *testing.T) {
	dir := t.TempDir()

	cfg := config.TracingConfig{
		Enabled:   true,
		ExportDir: dir,
	}

	shutdown, err := SetupTracing(cfg)
	require.NoError(t, err)

	// Create multiple spans.
	tracer := otel.Tracer("arbitron-test")
	ctx, parent := tracer.Start(context.Background(), "parent-op")
	_, child := tracer.Start(ctx, "child-op")
	child.End()
	parent.End()

	time.Sleep(100 * time.Millisecond)

	err = shutdown(context.Background())
	require.NoError(t, err)

	tracePath := filepath.Join(dir, "traces.jsonl")
	data, err := os.ReadFile(tracePath)
	require.NoError(t, err)

	content := string(data)
	assert.Contains(t, content, "parent-op")
	assert.Contains(t, content, "child-op")
}
