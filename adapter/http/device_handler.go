package http

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/arbitron/arbitron/domain/identity"
)

// maxDeviceBodyBytes caps the device-grant request bodies.
const maxDeviceBodyBytes = 1 << 16

// DeviceHandler exposes the OAuth 2.0 device authorization grant. The /code and
// /token endpoints are unauthenticated (mounted outside the auth middleware);
// the /approve and /deny actions run behind session auth (the approver must be
// a logged-in browser).
type DeviceHandler struct {
	svc             *identity.DeviceCodeService
	verificationURI string
}

// NewDeviceHandler builds a DeviceHandler. verificationURI is the absolute URL
// shown to the human approver (typically PublicBaseURL + "/auth/device").
func NewDeviceHandler(svc *identity.DeviceCodeService, verificationURI string) *DeviceHandler {
	return &DeviceHandler{svc: svc, verificationURI: verificationURI}
}

// startResponse is the RFC 8628 device-authorization response.
type startResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	Interval        int    `json:"interval"`
	ExpiresIn       int    `json:"expires_in"`
}

// Start handles POST /auth/device/code (unauthenticated).
func (h *DeviceHandler) Start(w http.ResponseWriter, r *http.Request) {
	resp, err := h.svc.Start(r.Context(), h.verificationURI)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, startResponse{
		DeviceCode:      resp.DeviceCode,
		UserCode:        resp.UserCode,
		VerificationURI: resp.VerificationURI,
		Interval:        resp.Interval,
		ExpiresIn:       resp.ExpiresIn,
	})
}

type userCodeRequest struct {
	UserCode string `json:"user_code"`
}

// Approve handles POST /auth/device/approve (session-authenticated). The
// approver identity is read from context by the service.
func (h *DeviceHandler) Approve(w http.ResponseWriter, r *http.Request) {
	uc, ok := decodeUserCode(w, r)
	if !ok {
		return
	}
	if err := h.svc.Approve(r.Context(), uc); err != nil {
		writeDeviceApprovalError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{"status": "approved"})
}

// Deny handles POST /auth/device/deny (session-authenticated).
func (h *DeviceHandler) Deny(w http.ResponseWriter, r *http.Request) {
	uc, ok := decodeUserCode(w, r)
	if !ok {
		return
	}
	if err := h.svc.Deny(r.Context(), uc); err != nil {
		writeDeviceApprovalError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{"status": "denied"})
}

type tokenRequest struct {
	DeviceCode string `json:"device_code"`
}

// tokenResponse is the RFC 8628 success body. The access_token is the minted
// arb_ plaintext, returned once.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
}

// Token handles POST /auth/device/token (unauthenticated polling).
func (h *DeviceHandler) Token(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxDeviceBodyBytes)
	var req tokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	plaintext, _, err := h.svc.Poll(r.Context(), req.DeviceCode)
	if err != nil {
		writeDeviceGrantError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, tokenResponse{AccessToken: plaintext, TokenType: "bearer"})
}

// decodeUserCode reads {"user_code": "..."} from the request, writing a 400 and
// returning ok=false on failure.
func decodeUserCode(w http.ResponseWriter, r *http.Request) (string, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxDeviceBodyBytes)
	var req userCodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return "", false
	}
	if req.UserCode == "" {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "user_code is required")
		return "", false
	}
	return req.UserCode, true
}

// writeDeviceApprovalError maps approval-path errors. Unauthenticated is 401 (the
// middleware should already block, defense-in-depth).
func writeDeviceApprovalError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, identity.ErrUnauthenticated):
		WriteError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
	case errors.Is(err, identity.ErrExpired):
		WriteError(w, http.StatusBadRequest, "EXPIRED", "device code expired")
	case errors.Is(err, identity.ErrNotFound):
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "device code not found")
	default:
		WriteError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
	}
}

// writeDeviceGrantError maps Poll errors to RFC 8628 token-endpoint error codes.
// Every grant error is a 400 with an {"error": "<code>"} body.
func writeDeviceGrantError(w http.ResponseWriter, err error) {
	code := "invalid_grant"
	switch {
	case errors.Is(err, identity.ErrAuthorizationPending):
		code = "authorization_pending"
	case errors.Is(err, identity.ErrAccessDenied):
		code = "access_denied"
	case errors.Is(err, identity.ErrExpired):
		code = "expired_token"
	case errors.Is(err, identity.ErrInvalidGrant):
		code = "invalid_grant"
	}
	WriteJSON(w, http.StatusBadRequest, map[string]string{"error": code})
}
