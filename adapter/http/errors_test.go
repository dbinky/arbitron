package http_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteError(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		code       string
		message    string
		wantStatus int
	}{
		{
			name:       "bad request",
			status:     http.StatusBadRequest,
			code:       "BAD_REQUEST",
			message:    "invalid input",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "not found",
			status:     http.StatusNotFound,
			code:       "NOT_FOUND",
			message:    "resource not found",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "conflict",
			status:     http.StatusConflict,
			code:       "CONFLICT",
			message:    "cannot pause instance in state completed",
			wantStatus: http.StatusConflict,
		},
		{
			name:       "internal server error",
			status:     http.StatusInternalServerError,
			code:       "INTERNAL_ERROR",
			message:    "something went wrong",
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			adapthttp.WriteError(w, tt.status, tt.code, tt.message)

			assert.Equal(t, tt.wantStatus, w.Code)
			assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

			var resp adapthttp.ErrorResponse
			err := json.NewDecoder(w.Body).Decode(&resp)
			require.NoError(t, err)
			assert.Equal(t, tt.code, resp.Code)

			// 5xx errors are redacted to prevent leaking internal details.
			if tt.status >= 500 {
				assert.Equal(t, "internal server error", resp.Error)
			} else {
				assert.Equal(t, tt.message, resp.Error)
			}
		})
	}
}

func TestWriteJSON(t *testing.T) {
	t.Run("writes JSON with status code", func(t *testing.T) {
		w := httptest.NewRecorder()
		data := map[string]string{"status": "ok"}
		adapthttp.WriteJSON(w, http.StatusOK, data)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

		var resp map[string]string
		err := json.NewDecoder(w.Body).Decode(&resp)
		require.NoError(t, err)
		assert.Equal(t, "ok", resp["status"])
	})

	t.Run("writes 201 status", func(t *testing.T) {
		w := httptest.NewRecorder()
		data := map[string]string{"id": "abc"}
		adapthttp.WriteJSON(w, http.StatusCreated, data)

		assert.Equal(t, http.StatusCreated, w.Code)
	})

	t.Run("writes struct as JSON", func(t *testing.T) {
		type testStruct struct {
			Name  string `json:"name"`
			Count int    `json:"count"`
		}
		w := httptest.NewRecorder()
		adapthttp.WriteJSON(w, http.StatusOK, testStruct{Name: "test", Count: 42})

		assert.Equal(t, http.StatusOK, w.Code)

		var resp testStruct
		err := json.NewDecoder(w.Body).Decode(&resp)
		require.NoError(t, err)
		assert.Equal(t, "test", resp.Name)
		assert.Equal(t, 42, resp.Count)
	})

	t.Run("writes slice as JSON", func(t *testing.T) {
		w := httptest.NewRecorder()
		data := []string{"a", "b", "c"}
		adapthttp.WriteJSON(w, http.StatusOK, data)

		var resp []string
		err := json.NewDecoder(w.Body).Decode(&resp)
		require.NoError(t, err)
		assert.Equal(t, []string{"a", "b", "c"}, resp)
	})
}
