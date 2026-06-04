package http

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// maxTokenBodyBytes caps the create-token request body.
const maxTokenBodyBytes = 1 << 20

// TokenHandler exposes personal API token management over REST. Every operation
// is scoped by the domain service to the request's identity (injected by the
// auth middleware), so a caller can only manage their own tokens.
type TokenHandler struct {
	svc *identity.TokenService
}

// NewTokenHandler creates a TokenHandler over the given service.
func NewTokenHandler(svc *identity.TokenService) *TokenHandler {
	return &TokenHandler{svc: svc}
}

// tokenView is the read DTO. SECURITY: it carries NO token_hash and NO
// plaintext — read surfaces never expose the secret material.
type tokenView struct {
	ID         string  `json:"id"`
	Label      string  `json:"label"`
	Prefix     string  `json:"prefix"`
	CreatedAt  string  `json:"created_at"`
	LastUsedAt *string `json:"last_used_at,omitempty"`
}

func toTokenView(t identity.Token) tokenView {
	v := tokenView{
		ID:        t.ID.String(),
		Label:     t.Label,
		Prefix:    t.Prefix,
		CreatedAt: t.CreatedAt.Format(APITimeFormat),
	}
	if t.LastUsedAt != nil {
		s := t.LastUsedAt.Format(APITimeFormat)
		v.LastUsedAt = &s
	}
	return v
}

// createTokenRequest is the create body.
type createTokenRequest struct {
	Label string `json:"label"`
}

// createTokenResponse is the ONLY response that carries the plaintext, returned
// once at creation. This is the sole opportunity for the caller to copy it; the
// plaintext is never recoverable afterwards.
type createTokenResponse struct {
	Token     tokenView `json:"token"`
	Plaintext string    `json:"plaintext"` // arb_..., shown ONCE
}

// Create handles POST /tokens.
func (h *TokenHandler) Create(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxTokenBodyBytes)
	var req createTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body: "+err.Error())
		return
	}

	plaintext, tok, err := h.svc.Mint(r.Context(), req.Label)
	if err != nil {
		writeTokenError(w, err)
		return
	}

	WriteJSON(w, http.StatusCreated, createTokenResponse{
		Token:     toTokenView(*tok),
		Plaintext: plaintext,
	})
}

// List handles GET /tokens.
func (h *TokenHandler) List(w http.ResponseWriter, r *http.Request) {
	toks, err := h.svc.List(r.Context())
	if err != nil {
		writeTokenError(w, err)
		return
	}
	views := make([]tokenView, 0, len(toks))
	for _, t := range toks {
		views = append(views, toTokenView(t))
	}
	WriteJSON(w, http.StatusOK, views)
}

// Revoke handles DELETE /tokens/{id}.
func (h *TokenHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid token id")
		return
	}
	if err := h.svc.Revoke(r.Context(), id); err != nil {
		writeTokenError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeTokenError maps domain sentinels to HTTP status codes. ErrNotFound and
// "not yours" are both 404 so a caller cannot probe for another user's token id.
func writeTokenError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, identity.ErrInvalidLabel):
		WriteError(w, http.StatusBadRequest, "INVALID_LABEL", "label is required")
	case errors.Is(err, identity.ErrUnauthenticated):
		WriteError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
	case errors.Is(err, identity.ErrNotFound):
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "token not found")
	default:
		WriteError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
	}
}
