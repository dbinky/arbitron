package http

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestLoggingMiddleware_LogsExpectedFields(t *testing.T) {
	var buf strings.Builder
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	handler := RequestLoggingMiddleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	var entry map[string]any
	err := json.Unmarshal([]byte(buf.String()), &entry)
	require.NoError(t, err)

	assert.Equal(t, "GET", entry["method"])
	assert.Equal(t, "/api/v1/health", entry["path"])
	assert.Equal(t, float64(200), entry["status"])
	assert.Contains(t, entry, "duration_ms")
	assert.Equal(t, "INFO", entry["level"])
}

func TestRequestLoggingMiddleware_WarnFor4xx(t *testing.T) {
	var buf strings.Builder
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	handler := RequestLoggingMiddleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	var entry map[string]any
	err := json.Unmarshal([]byte(buf.String()), &entry)
	require.NoError(t, err)

	assert.Equal(t, "WARN", entry["level"])
	assert.Equal(t, float64(404), entry["status"])
}

func TestRequestLoggingMiddleware_ErrorFor5xx(t *testing.T) {
	var buf strings.Builder
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	handler := RequestLoggingMiddleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/trigger", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	var entry map[string]any
	err := json.Unmarshal([]byte(buf.String()), &entry)
	require.NoError(t, err)

	assert.Equal(t, "ERROR", entry["level"])
	assert.Equal(t, float64(500), entry["status"])
}

func TestStatusRecorder_DefaultsTo200(t *testing.T) {
	rec := httptest.NewRecorder()
	sr := newStatusRecorder(rec)

	// Without calling WriteHeader, should default to 200.
	assert.Equal(t, http.StatusOK, sr.statusCode)
}

func TestStatusRecorder_CapturesWriteHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	sr := newStatusRecorder(rec)

	sr.WriteHeader(http.StatusCreated)
	assert.Equal(t, http.StatusCreated, sr.statusCode)
	assert.Equal(t, http.StatusCreated, rec.Code)
}
