// Package http provides REST API handlers for the Arbitron workflow engine.
package http

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// APITimeFormat is the canonical time format for all JSON API responses.
const APITimeFormat = time.RFC3339

// APITimeFormatMillis includes millisecond precision, used where the frontend
// needs sub-second display (e.g., notifications).
const APITimeFormatMillis = "2006-01-02T15:04:05.000Z07:00"

// ErrorResponse represents a structured error returned by the API.
type ErrorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// WriteError writes a JSON error response with the given HTTP status code.
// For 5xx errors, the actual message is logged server-side and a generic
// message is returned to the client to avoid leaking internal details.
func WriteError(w http.ResponseWriter, status int, code string, message string) {
	responseMsg := message
	if status >= 500 {
		slog.Error("internal server error", "status", status, "code", code, "detail", message)
		responseMsg = "internal server error"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorResponse{
		Error: responseMsg,
		Code:  code,
	})
}

// WriteJSON writes a JSON response with the given HTTP status code.
func WriteJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
