package observability

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
