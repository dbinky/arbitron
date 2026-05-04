// Package http provides REST API handlers for the Arbitron workflow engine.
package http

import (
	"encoding/json"
	"net/http"
)

// ErrorResponse represents a structured error returned by the API.
type ErrorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// WriteError writes a JSON error response with the given HTTP status code.
func WriteError(w http.ResponseWriter, status int, code string, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorResponse{
		Error: message,
		Code:  code,
	})
}

// WriteJSON writes a JSON response with the given HTTP status code.
func WriteJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
