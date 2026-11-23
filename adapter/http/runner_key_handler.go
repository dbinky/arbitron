// adapter/http/runner_key_handler.go
package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/runner"
)

const maxRunnerKeyBodyBytes = 64 << 10

// RunnerKeyService is the admin key-management port (*runner.KeyService).
type RunnerKeyService interface {
	Create(ctx context.Context, name string) (string, runner.RunnerKey, error)
	List(ctx context.Context) ([]runner.RunnerKey, error)
	Revoke(ctx context.Context, id uuid.UUID, ack runner.RevokeAck) error
}

// RunnerKeyHandler serves /api/v1/runners/keys. Authz and audit live in the service.
type RunnerKeyHandler struct{ svc RunnerKeyService }

// NewRunnerKeyHandler builds the handler.
func NewRunnerKeyHandler(svc RunnerKeyService) *RunnerKeyHandler { return &RunnerKeyHandler{svc: svc} }

// runnerKeyView is the read DTO. SECURITY: no hash, no machine key, no plaintext.
type runnerKeyView struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	RunnerID   string  `json:"runnerId"`
	Prefix     string  `json:"prefix"`
	Claimed    bool    `json:"claimed"`
	Revoked    bool    `json:"revoked"`
	CreatedAt  string  `json:"createdAt"`
	ClaimedAt  *string `json:"claimedAt,omitempty"`
	LastUsedAt *string `json:"lastUsedAt,omitempty"`
	RevokedAt  *string `json:"revokedAt,omitempty"`
}

func fmtTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(APITimeFormat)
	return &s
}

func toRunnerKeyView(k runner.RunnerKey) runnerKeyView {
	return runnerKeyView{
		ID: k.ID.String(), Name: k.Name, RunnerID: string(k.RunnerID), Prefix: k.Prefix,
		Claimed: k.MachinePublicKey != nil, Revoked: k.RevokedAt != nil,
		CreatedAt: k.CreatedAt.UTC().Format(APITimeFormat),
		ClaimedAt: fmtTimePtr(k.ClaimedAt), LastUsedAt: fmtTimePtr(k.LastUsedAt), RevokedAt: fmtTimePtr(k.RevokedAt),
	}
}

// runnerKeyCreateResponse is the ONLY response carrying the plaintext.
type runnerKeyCreateResponse struct {
	Key       runnerKeyView `json:"key"`
	Plaintext string        `json:"plaintext"`
	Setup     string        `json:"setup"`
}

func writeRunnerKeyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, identity.ErrUnauthenticated):
		WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
	case errors.Is(err, identity.ErrForbidden):
		WriteError(w, http.StatusForbidden, "FORBIDDEN", "admin role required")
	case errors.Is(err, runner.ErrInvalidKeyName):
		WriteError(w, http.StatusBadRequest, "INVALID_NAME", err.Error())
	case errors.Is(err, runner.ErrKeyNameTaken):
		WriteError(w, http.StatusConflict, "NAME_TAKEN", "a runner key with this name already exists")
	case errors.Is(err, runner.ErrKeyNotFound):
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "runner key not found")
	case errors.Is(err, runner.ErrRevokeNotAcknowledged):
		WriteError(w, http.StatusBadRequest, "ACKNOWLEDGEMENT_REQUIRED", err.Error())
	default:
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "runner key operation failed: "+err.Error())
	}
}

// Create handles POST /api/v1/runners/keys {"name": "..."}.
func (h *RunnerKeyHandler) Create(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRunnerKeyBodyBytes)
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	plaintext, k, err := h.svc.Create(r.Context(), req.Name)
	if err != nil {
		writeRunnerKeyError(w, err)
		return
	}
	WriteJSON(w, http.StatusCreated, runnerKeyCreateResponse{Key: toRunnerKeyView(k), Plaintext: plaintext, Setup: runner.KeySetupGuidance})
}

// List handles GET /api/v1/runners/keys.
func (h *RunnerKeyHandler) List(w http.ResponseWriter, r *http.Request) {
	keys, err := h.svc.List(r.Context())
	if err != nil {
		writeRunnerKeyError(w, err)
		return
	}
	out := make([]runnerKeyView, 0, len(keys))
	for _, k := range keys {
		out = append(out, toRunnerKeyView(k))
	}
	WriteJSON(w, http.StatusOK, out)
}

// Revoke handles DELETE /api/v1/runners/keys/{id} {"acknowledge": true}.
func (h *RunnerKeyHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid runner key id")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRunnerKeyBodyBytes)
	var req struct {
		Acknowledge bool `json:"acknowledge"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
			return
		}
	}
	if err := h.svc.Revoke(r.Context(), id, runner.RevokeAck{Acknowledged: req.Acknowledge}); err != nil {
		writeRunnerKeyError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{"status": "revoked", "id": id.String()})
}
