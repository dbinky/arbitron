package http

import (
	"net/http"

	"github.com/arbitron/arbitron/domain/identity"
)

// MeHandler serves GET /api/v1/me, the whoami endpoint. It reads the caller's
// already-resolved identity from context (the auth middleware populated it) and
// returns the projection that drives client gating and the later dashboard
// department selector. It performs no DB read.
type MeHandler struct {
	// mode is the configured operating mode ("local"|"org"|"edge"), normalized.
	mode string
}

// NewMeHandler builds the stateless me handler. mode is the configured auth mode
// (config.AuthModeLocal/Org/Edge); an unknown/empty value normalizes to "local".
func NewMeHandler(mode string) *MeHandler {
	return &MeHandler{mode: string(identity.ModeFromString(mode))}
}

// meView is the read DTO. SECURITY: it exposes ONLY non-secret projection fields
// — never the IdP subject or email (those live on the persisted User and never
// reach the Identity, but this DTO is the serialization boundary, so it is
// documented here as the place that must stay free of identifiers).
type meView struct {
	UserID         string   `json:"userId"`
	Username       string   `json:"username"`
	Role           string   `json:"role"`
	Departments    []string `json:"departments"`
	LedDepartments []string `json:"ledDepartments"`
	IsAdmin        bool     `json:"isAdmin"`
	// AuthMode is the server operating mode ("local"|"org"|"edge"). Non-secret;
	// drives the dashboard's mode-aware default view.
	AuthMode string `json:"authMode"`
}

// Me handles GET /me. A missing identity (auth disabled, or a route reached
// outside the middleware) is a clean 401, never a panic — so this uses the
// (Identity, bool) FromContext form rather than MustFromContext.
func (h *MeHandler) Me(w http.ResponseWriter, r *http.Request) {
	id, ok := identity.FromContext(r.Context())
	if !ok {
		WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	// Normalize nil slices to empty so the JSON shape is always an array.
	depts := id.Departments
	if depts == nil {
		depts = []string{}
	}
	led := id.LedDepartments
	if led == nil {
		led = []string{}
	}
	WriteJSON(w, http.StatusOK, meView{
		UserID:         id.UserID.String(),
		Username:       id.Username,
		Role:           string(id.Role),
		Departments:    depts,
		LedDepartments: led,
		IsAdmin:        id.IsAdmin(),
		AuthMode:       h.mode,
	})
}
