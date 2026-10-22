package observability

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetupLogging_ConsoleUsesStderrOnly(t *testing.T) {
	stdout, outWriter, err := os.Pipe()
	require.NoError(t, err)
	stderr, errWriter, err := os.Pipe()
	require.NoError(t, err)
	priorOut, priorErr, priorLogger := os.Stdout, os.Stderr, slog.Default()
	os.Stdout, os.Stderr = outWriter, errWriter
	t.Cleanup(func() {
		os.Stdout, os.Stderr = priorOut, priorErr
		slog.SetDefault(priorLogger)
		stdout.Close()
		stderr.Close()
		outWriter.Close()
		errWriter.Close()
	})
	path := filepath.Join(t.TempDir(), "console.log")
	logger, closer, err := SetupLogging(config.LoggingConfig{Level: "info", File: path})
	require.NoError(t, err)
	logger.Info("protocol-safe console", "label", "并行\nsecond line")
	require.NoError(t, closer.Close())
	require.NoError(t, outWriter.Close())
	require.NoError(t, errWriter.Close())
	out, err := io.ReadAll(stdout)
	require.NoError(t, err)
	console, err := io.ReadAll(stderr)
	require.NoError(t, err)
	require.Empty(t, string(out), "stdout belongs exclusively to the protocol")
	require.Contains(t, string(console), "protocol-safe console")
	require.Contains(t, string(console), "level=INFO")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var entry map[string]any
	require.NoError(t, json.Unmarshal(data, &entry))
	require.Equal(t, "protocol-safe console", entry["msg"])
	require.Equal(t, "并行\nsecond line", entry["label"])
}

func TestSetupLogging_CreatesLogFileWithJSONEntries(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "test.log")

	cfg := config.LoggingConfig{
		Level: "info",
		File:  logFile,
	}

	logger, closer, err := SetupLogging(cfg)
	require.NoError(t, err)
	defer closer.Close()

	logger.Info("test message", "key", "value")

	// Read back the log file and verify JSON.
	data, err := os.ReadFile(logFile)
	require.NoError(t, err)
	require.NotEmpty(t, data)

	var entry map[string]any
	err = json.Unmarshal(data, &entry)
	require.NoError(t, err)

	assert.Equal(t, "test message", entry["msg"])
	assert.Equal(t, "value", entry["key"])
	assert.Equal(t, "INFO", entry["level"])
}

func TestSetupLogging_LevelFiltering(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "test.log")

	cfg := config.LoggingConfig{
		Level: "warn",
		File:  logFile,
	}

	logger, closer, err := SetupLogging(cfg)
	require.NoError(t, err)
	defer closer.Close()

	// Info should be filtered out at warn level.
	logger.Info("should not appear")
	logger.Warn("should appear")

	data, err := os.ReadFile(logFile)
	require.NoError(t, err)

	content := string(data)
	assert.NotContains(t, content, "should not appear")
	assert.Contains(t, content, "should appear")
}

func TestSetupLogging_CreatesParentDirectories(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "deep", "nested", "dir", "test.log")

	cfg := config.LoggingConfig{
		Level: "info",
		File:  logFile,
	}

	logger, closer, err := SetupLogging(cfg)
	require.NoError(t, err)
	defer closer.Close()

	logger.Info("nested dir test")

	_, err = os.Stat(logFile)
	assert.NoError(t, err)
}

func TestMultiHandler_FansOutToBothHandlers(t *testing.T) {
	var buf1, buf2 strings.Builder

	h1 := slog.NewJSONHandler(&buf1, &slog.HandlerOptions{Level: slog.LevelDebug})
	h2 := slog.NewTextHandler(&buf2, &slog.HandlerOptions{Level: slog.LevelDebug})

	multi := NewMultiHandler(h1, h2)
	logger := slog.New(multi)

	logger.Info("multi test", "foo", "bar")

	// Both writers should have received the log entry.
	assert.Contains(t, buf1.String(), "multi test")
	assert.Contains(t, buf2.String(), "multi test")
	assert.Contains(t, buf1.String(), "foo")
	assert.Contains(t, buf2.String(), "foo")
}

func TestMultiHandler_WithAttrs(t *testing.T) {
	var buf strings.Builder

	h := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	multi := NewMultiHandler(h)

	withAttrs := multi.WithAttrs([]slog.Attr{slog.String("component", "test")})
	logger := slog.New(withAttrs)

	logger.Info("attrs test")

	assert.Contains(t, buf.String(), "component")
	assert.Contains(t, buf.String(), "test")
}

func TestMultiHandler_WithGroup(t *testing.T) {
	var buf strings.Builder

	h := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	multi := NewMultiHandler(h)

	withGroup := multi.WithGroup("mygroup")
	logger := slog.New(withGroup)

	logger.Info("group test", "key", "val")

	assert.Contains(t, buf.String(), "mygroup")
}

func TestParseLevel_ValidLevels(t *testing.T) {
	tests := []struct {
		input    string
		expected slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"INFO", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			level, err := parseLevel(tt.input)
			assert.NoError(t, err)
			assert.Equal(t, tt.expected, level)
		})
	}
}

func TestParseLevel_InvalidLevel(t *testing.T) {
	level, err := parseLevel("bogus")
	assert.Error(t, err)
	assert.Equal(t, slog.LevelInfo, level)
}

func TestSetupLogging_InvalidLevel_DefaultsToInfo(t *testing.T) {
	dir := t.TempDir()
	cfg := config.LoggingConfig{Level: "bogus-level", File: filepath.Join(dir, "test.log")}

	logger, closer, err := SetupLogging(cfg)
	require.NoError(t, err)
	defer closer.Close()

	// Logger should still work (at info level by default).
	require.NotNil(t, logger)
	logger.Info("info-at-default")
	data, err := os.ReadFile(cfg.File)
	require.NoError(t, err)
	assert.Contains(t, string(data), "info-at-default")
}

func TestSetupLogging_InvalidFilePath(t *testing.T) {
	// /dev/null is not a directory; trying to create a file inside it should fail.
	cfg := config.LoggingConfig{Level: "info", File: "/dev/null/arbitron-test-logs/app.log"}

	_, _, err := SetupLogging(cfg)
	assert.Error(t, err)
}

func TestMultiHandler_Enabled_AllDisabled(t *testing.T) {
	// Both handlers at Error level; querying Info should return false.
	opts := &slog.HandlerOptions{Level: slog.LevelError}
	h1 := slog.NewJSONHandler(os.Stdout, opts)
	h2 := slog.NewJSONHandler(os.Stdout, opts)
	multi := NewMultiHandler(h1, h2)

	assert.False(t, multi.Enabled(context.Background(), slog.LevelInfo))
}

func TestMultiHandler_Enabled_AnyEnabled(t *testing.T) {
	// One handler at Error, another at Debug; querying Info should return true.
	h1 := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError})
	h2 := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})
	multi := NewMultiHandler(h1, h2)

	assert.True(t, multi.Enabled(context.Background(), slog.LevelInfo))
}

func TestMultiHandler_EmptyHandlers(t *testing.T) {
	multi := NewMultiHandler()

	// With no handlers, Enabled should return false.
	assert.False(t, multi.Enabled(context.Background(), slog.LevelInfo))

	// Handle should not error.
	assert.NoError(t, multi.Handle(context.Background(), slog.Record{}))
}
