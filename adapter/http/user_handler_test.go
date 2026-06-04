package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newUserHandler builds a real UserService over the shared fakeUserRepo with a
// real Provisioner as the bootstrap checker.
func newUserHandler(bootstrap ...string) (*UserHandler, *fakeUserRepo) {
	repo := newFakeUserRepo()
	svc := instance.NewUserService(repo, instance.NewProvisioner(repo, bootstrap, identity.GroupConfig{DefaultGroup: "default"}))
	return NewUserHandler(svc), repo
}

func userHTTPReq(method, target, body string, role identity.Role, idParam string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	r = r.WithContext(identity.WithIdentity(r.Context(), identity.Identity{UserID: uuid.New(), Role: role}))
	if idParam != "" {
		r = withChiURLParamTok(r, "id", idParam)
	}
	return r
}

// Happy path: admin promotes; admin demotes when another admin remains; GET /users
// returns userView without subject or email.
func TestUserHandler_HappyPath(t *testing.T) {
	h, repo := newUserHandler()
	target := uuid.New()
	repo.byID[target] = &instance.User{ID: target, Subject: strptrH("sub-secret"), Email: strptrH("u@example.com"), Username: "u", Role: identity.RoleUser}

	// Promote user -> admin.
	w := httptest.NewRecorder()
	h.SetRole(w, userHTTPReq(http.MethodPut, "/users/"+target.String()+"/role", `{"role":"admin"}`, identity.RoleAdmin, target.String()))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, identity.RoleAdmin, repo.byID[target].Role)

	// Successful demotion: seed a second admin so the last-admin floor does not fire.
	other := uuid.New()
	repo.byID[other] = &instance.User{ID: other, Subject: strptrH("other-sub"), Username: "other", Role: identity.RoleAdmin}
	wd := httptest.NewRecorder()
	h.SetRole(wd, userHTTPReq(http.MethodPut, "/users/"+target.String()+"/role", `{"role":"user"}`, identity.RoleAdmin, target.String()))
	require.Equal(t, http.StatusOK, wd.Code, "demotion must succeed when another admin remains")
	assert.Equal(t, identity.RoleUser, repo.byID[target].Role)

	wl := httptest.NewRecorder()
	h.List(wl, userHTTPReq(http.MethodGet, "/users", "", identity.RoleAdmin, ""))
	require.Equal(t, http.StatusOK, wl.Code)
	body := wl.Body.String()
	assert.NotContains(t, body, "sub-secret", "admin list must not leak IdP subject value")
	assert.NotContains(t, body, "u@example.com", "admin list must not leak IdP email value")
	assert.NotContains(t, body, "subject", "admin list must not leak 'subject' field name")
	assert.NotContains(t, body, "email", "admin list must not leak 'email' field name")
	var views []userView
	require.NoError(t, json.Unmarshal([]byte(body), &views))
	assert.Len(t, views, 2)
}

// Input validation: bad UUID, invalid role, malformed JSON.
func TestUserHandler_Validation(t *testing.T) {
	h, repo := newUserHandler()
	target := uuid.New()
	repo.byID[target] = &instance.User{ID: target, Role: identity.RoleUser}

	w := httptest.NewRecorder()
	h.SetRole(w, userHTTPReq(http.MethodPut, "/users/x/role", `{"role":"admin"}`, identity.RoleAdmin, "not-a-uuid"))
	assert.Equal(t, http.StatusBadRequest, w.Code)

	w = httptest.NewRecorder()
	h.SetRole(w, userHTTPReq(http.MethodPut, "/users/"+target.String()+"/role", `{"role":"superuser"}`, identity.RoleAdmin, target.String()))
	assert.Equal(t, http.StatusBadRequest, w.Code)

	w = httptest.NewRecorder()
	h.SetRole(w, userHTTPReq(http.MethodPut, "/users/"+target.String()+"/role", `{bad`, identity.RoleAdmin, target.String()))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// Error/failure: unknown target -> 404.
func TestUserHandler_NotFound(t *testing.T) {
	h, _ := newUserHandler()
	missing := uuid.New().String()
	w := httptest.NewRecorder()
	h.SetRole(w, userHTTPReq(http.MethodPut, "/users/"+missing+"/role", `{"role":"admin"}`, identity.RoleAdmin, missing))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// Boundary: demote last admin -> 409; demote bootstrap admin -> 409.
func TestUserHandler_DemotionFloors(t *testing.T) {
	// Last admin.
	h, repo := newUserHandler()
	admin := uuid.New()
	repo.byID[admin] = &instance.User{ID: admin, Subject: strptrH("a"), Role: identity.RoleAdmin}
	w := httptest.NewRecorder()
	h.SetRole(w, userHTTPReq(http.MethodPut, "/users/"+admin.String()+"/role", `{"role":"user"}`, identity.RoleAdmin, admin.String()))
	assert.Equal(t, http.StatusConflict, w.Code)

	// Bootstrap admin (with another admin present so the last-admin floor is not
	// what triggers).
	h2, repo2 := newUserHandler("boss-sub")
	boss := uuid.New()
	other := uuid.New()
	repo2.byID[boss] = &instance.User{ID: boss, Subject: strptrH("boss-sub"), Role: identity.RoleAdmin}
	repo2.byID[other] = &instance.User{ID: other, Subject: strptrH("other"), Role: identity.RoleAdmin}
	w2 := httptest.NewRecorder()
	h2.SetRole(w2, userHTTPReq(http.MethodPut, "/users/"+boss.String()+"/role", `{"role":"user"}`, identity.RoleAdmin, boss.String()))
	assert.Equal(t, http.StatusConflict, w2.Code)
	assert.Contains(t, w2.Body.String(), "bootstrap")
}

// Boundary/security: bootstrap floor uses case-insensitive email normalization —
// the same rule as the provisioner — so a user provisioned with "Boss@Example.com"
// cannot be demoted even when the bootstrap list stores "boss@example.com".
func TestUserHandler_BootstrapFloor_CaseInsensitiveEmail(t *testing.T) {
	// Bootstrap list stores the lowercase canonical form.
	h, repo := newUserHandler("boss@example.com")
	boss := uuid.New()
	other := uuid.New()
	// Seed the user with the MIXED-CASE email as it arrived from the IdP.
	repo.byID[boss] = &instance.User{
		ID:      boss,
		Subject: strptrH("boss-sub"),
		Email:   strptrH("Boss@Example.com"), // mixed case; bootstrap list has "boss@example.com"
		Role:    identity.RoleAdmin,
	}
	repo.byID[other] = &instance.User{ID: other, Subject: strptrH("other"), Role: identity.RoleAdmin}

	w := httptest.NewRecorder()
	h.SetRole(w, userHTTPReq(http.MethodPut, "/users/"+boss.String()+"/role", `{"role":"user"}`, identity.RoleAdmin, boss.String()))
	assert.Equal(t, http.StatusConflict, w.Code,
		"bootstrap floor must fire even when persisted email case differs from bootstrap list entry")
	assert.Contains(t, w.Body.String(), "bootstrap",
		"response must name the bootstrap floor, not the last-admin floor")
	// The user's role must be unchanged.
	assert.Equal(t, identity.RoleAdmin, repo.byID[boss].Role)
}

// Isolation/security: non-admin -> 403 on both; unauthenticated -> 401 on both.
func TestUserHandler_Security(t *testing.T) {
	h, repo := newUserHandler()
	target := uuid.New()
	repo.byID[target] = &instance.User{ID: target, Role: identity.RoleUser}

	w := httptest.NewRecorder()
	h.SetRole(w, userHTTPReq(http.MethodPut, "/users/"+target.String()+"/role", `{"role":"admin"}`, identity.RoleUser, target.String()))
	assert.Equal(t, http.StatusForbidden, w.Code)

	wl := httptest.NewRecorder()
	h.List(wl, userHTTPReq(http.MethodGet, "/users", "", identity.RoleUser, ""))
	assert.Equal(t, http.StatusForbidden, wl.Code)

	// Unauthenticated (no identity in ctx) on both endpoints.
	wu := httptest.NewRecorder()
	h.List(wu, httptest.NewRequest(http.MethodGet, "/users", nil))
	assert.Equal(t, http.StatusUnauthorized, wu.Code)

	ws := httptest.NewRecorder()
	unauthReq := httptest.NewRequest(http.MethodPut, "/users/"+target.String()+"/role", strings.NewReader(`{"role":"admin"}`))
	unauthReq = withChiURLParamTok(unauthReq, "id", target.String())
	h.SetRole(ws, unauthReq)
	assert.Equal(t, http.StatusUnauthorized, ws.Code, "SetRole must return 401 when no identity in context")
}

func strptrH(s string) *string { return &s }

var _ = context.Background
