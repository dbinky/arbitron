package http

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// maxUserBodyBytes caps the set-role request body.
const maxUserBodyBytes = 1 << 16

// UserHandler exposes admin-only user/role management over REST. The admin gate
// and demotion floors live in the domain UserService, so these handlers do not
// re-check admin — they map the service's sentinels to status codes.
type UserHandler struct {
	svc *instance.UserService
}

// NewUserHandler creates a UserHandler over the given service.
func NewUserHandler(svc *instance.UserService) *UserHandler {
	return &UserHandler{svc: svc}
}

// userView is the read DTO. It exposes only id/username/role — never subject or
// email (no IdP identifiers leak through the admin list).
type userView struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

func toUserView(u instance.User) userView {
	return userView{ID: u.ID.String(), Username: u.Username, Role: string(u.Role)}
}

// List handles GET /users (admin-only via the service).
func (h *UserHandler) List(w http.ResponseWriter, r *http.Request) {
	users, err := h.svc.List(r.Context())
	if err != nil {
		writeUserError(w, err)
		return
	}
	views := make([]userView, 0, len(users))
	for _, u := range users {
		views = append(views, toUserView(u))
	}
	WriteJSON(w, http.StatusOK, views)
}

type setRoleRequest struct {
	Role string `json:"role"`
}

// SetRole handles PUT /users/{id}/role (admin-only via the service).
func (h *UserHandler) SetRole(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid user id")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUserBodyBytes)
	var req setRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body")
		return
	}
	if err := h.svc.SetRole(r.Context(), id, req.Role); err != nil {
		writeUserError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// writeUserError maps role-management sentinels to status codes. The demotion
// floors are 409 Conflict (the request is valid but conflicts with the
// invariant "the system must keep at least one, non-bootstrap, admin").
func writeUserError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, instance.ErrUnauthenticated):
		WriteError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication required")
	case errors.Is(err, instance.ErrForbidden):
		WriteError(w, http.StatusForbidden, "FORBIDDEN", "admin only")
	case errors.Is(err, instance.ErrInvalidRole):
		WriteError(w, http.StatusBadRequest, "INVALID_ROLE", "role must be 'user' or 'admin'")
	case errors.Is(err, instance.ErrNotFound):
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "user not found")
	case errors.Is(err, instance.ErrBootstrapAdminProtected):
		WriteError(w, http.StatusConflict, "BOOTSTRAP_ADMIN", "cannot demote a bootstrap admin")
	case errors.Is(err, instance.ErrLastAdmin):
		WriteError(w, http.StatusConflict, "LAST_ADMIN", "cannot demote the last admin")
	default:
		WriteError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
	}
}
