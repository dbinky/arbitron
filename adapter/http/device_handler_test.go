package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memDeviceRepo is an in-memory identity.DeviceCodeRepository for handler tests.
type memDeviceRepo struct {
	byHash map[string]*identity.DeviceCode
	byUser map[string]*identity.DeviceCode
}

func newMemDeviceRepo() *memDeviceRepo {
	return &memDeviceRepo{byHash: map[string]*identity.DeviceCode{}, byUser: map[string]*identity.DeviceCode{}}
}

func (r *memDeviceRepo) Create(_ context.Context, dc *identity.DeviceCode) error {
	cp := *dc
	r.byHash[dc.HashedCode] = &cp
	r.byUser[dc.UserCode] = &cp
	return nil
}
func (r *memDeviceRepo) GetByHash(_ context.Context, h string) (*identity.DeviceCode, error) {
	if dc, ok := r.byHash[h]; ok {
		cp := *dc
		return &cp, nil
	}
	return nil, identity.ErrNotFound
}
func (r *memDeviceRepo) GetByUserCode(_ context.Context, uc string) (*identity.DeviceCode, error) {
	if dc, ok := r.byUser[uc]; ok {
		cp := *dc
		return &cp, nil
	}
	return nil, identity.ErrNotFound
}
func (r *memDeviceRepo) Approve(_ context.Context, uc string, uid uuid.UUID) error {
	dc, ok := r.byUser[uc]
	if !ok || dc.Status != identity.DeviceCodePending {
		return identity.ErrNotFound
	}
	dc.Status = identity.DeviceCodeApproved
	dc.UserID = &uid
	return nil
}
func (r *memDeviceRepo) Deny(_ context.Context, uc string) error {
	dc, ok := r.byUser[uc]
	if !ok || dc.Status != identity.DeviceCodePending {
		return identity.ErrNotFound
	}
	dc.Status = identity.DeviceCodeDenied
	return nil
}
func (r *memDeviceRepo) Consume(_ context.Context, h string) error {
	dc, ok := r.byHash[h]
	if !ok || dc.Status != identity.DeviceCodeApproved {
		return identity.ErrNotFound
	}
	dc.Status = identity.DeviceCodeConsumed
	return nil
}
func (r *memDeviceRepo) DeleteExpired(_ context.Context, _ time.Time) (int, error) { return 0, nil }

func newDeviceHandler() (*DeviceHandler, *memDeviceRepo, *memTokenRepo) {
	devRepo := newMemDeviceRepo()
	tokRepo := newMemTokenRepo()
	svc := identity.NewDeviceCodeService(devRepo, identity.NewTokenService(tokRepo), 10*time.Minute, 5)
	return NewDeviceHandler(svc, "https://app.example.com/auth/device"), devRepo, tokRepo
}

func devReq(method, body string, uid *uuid.UUID) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, "/auth/device", nil)
	} else {
		r = httptest.NewRequest(method, "/auth/device", strings.NewReader(body))
	}
	if uid != nil {
		r = r.WithContext(identity.WithIdentity(r.Context(), identity.Identity{UserID: *uid, Role: identity.RoleUser}))
	}
	return r
}

// Happy path: start -> approve (session) -> token returns arb_ access_token.
func TestDeviceHandler_HappyPath(t *testing.T) {
	h, _, tokRepo := newDeviceHandler()

	w := httptest.NewRecorder()
	h.Start(w, httptest.NewRequest(http.MethodPost, "/auth/device/code", nil))
	require.Equal(t, http.StatusOK, w.Code)
	var start startResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &start))
	assert.NotEmpty(t, start.DeviceCode)
	assert.NotEmpty(t, start.UserCode)
	assert.Equal(t, "https://app.example.com/auth/device", start.VerificationURI)
	assert.Equal(t, 5, start.Interval)

	// Approve as a logged-in user.
	approver := uuid.New()
	wa := httptest.NewRecorder()
	h.Approve(wa, devReq(http.MethodPost, `{"user_code":"`+start.UserCode+`"}`, &approver))
	require.Equal(t, http.StatusOK, wa.Code)

	// Poll for the token.
	wt := httptest.NewRecorder()
	h.Token(wt, httptest.NewRequest(http.MethodPost, "/auth/device/token", strings.NewReader(`{"device_code":"`+start.DeviceCode+`"}`)))
	require.Equal(t, http.StatusOK, wt.Code)
	var tr tokenResponse
	require.NoError(t, json.Unmarshal(wt.Body.Bytes(), &tr))
	assert.True(t, strings.HasPrefix(tr.AccessToken, "arb_"))
	assert.Equal(t, "bearer", tr.TokenType)

	// The minted token is owned by the approver.
	require.Len(t, tokRepo.toks, 1)
	for _, tk := range tokRepo.toks {
		assert.Equal(t, approver, tk.UserID)
	}
	// The raw device code never appears in the token response.
	assert.NotContains(t, wt.Body.String(), start.DeviceCode)
}

// Input validation: bad/no body on token; missing user_code on approve.
func TestDeviceHandler_Validation(t *testing.T) {
	h, _, _ := newDeviceHandler()

	w := httptest.NewRecorder()
	h.Token(w, httptest.NewRequest(http.MethodPost, "/auth/device/token", strings.NewReader(`not json`)))
	assert.Equal(t, http.StatusBadRequest, w.Code)

	w = httptest.NewRecorder()
	h.Token(w, httptest.NewRequest(http.MethodPost, "/auth/device/token", nil))
	assert.Equal(t, http.StatusBadRequest, w.Code)

	approver := uuid.New()
	w = httptest.NewRecorder()
	h.Approve(w, devReq(http.MethodPost, `{"user_code":""}`, &approver))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// Error/failure: pending/denied poll map to the RFC error codes.
func TestDeviceHandler_PollStates(t *testing.T) {
	h, devRepo, _ := newDeviceHandler()

	// Start a flow.
	w := httptest.NewRecorder()
	h.Start(w, httptest.NewRequest(http.MethodPost, "/auth/device/code", nil))
	var start startResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &start))

	// Pending poll.
	wp := httptest.NewRecorder()
	h.Token(wp, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"device_code":"`+start.DeviceCode+`"}`)))
	assert.Equal(t, http.StatusBadRequest, wp.Code)
	assert.Contains(t, wp.Body.String(), "authorization_pending")

	// Denied poll.
	require.NoError(t, devRepo.Deny(context.Background(), start.UserCode))
	wd := httptest.NewRecorder()
	h.Token(wd, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"device_code":"`+start.DeviceCode+`"}`)))
	assert.Equal(t, http.StatusBadRequest, wd.Code)
	assert.Contains(t, wd.Body.String(), "access_denied")
}

// Boundary: a consumed (second) poll yields invalid_grant.
func TestDeviceHandler_DoubleConsume(t *testing.T) {
	h, _, _ := newDeviceHandler()

	w := httptest.NewRecorder()
	h.Start(w, httptest.NewRequest(http.MethodPost, "/auth/device/code", nil))
	var start startResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &start))

	approver := uuid.New()
	wa := httptest.NewRecorder()
	h.Approve(wa, devReq(http.MethodPost, `{"user_code":"`+start.UserCode+`"}`, &approver))
	require.Equal(t, http.StatusOK, wa.Code)

	body := `{"device_code":"` + start.DeviceCode + `"}`
	w1 := httptest.NewRecorder()
	h.Token(w1, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body)))
	require.Equal(t, http.StatusOK, w1.Code)

	w2 := httptest.NewRecorder()
	h.Token(w2, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body)))
	assert.Equal(t, http.StatusBadRequest, w2.Code)
	assert.Contains(t, w2.Body.String(), "invalid_grant")
}

// Isolation/security: approve with no identity in ctx is unauthenticated (the
// service rejects it; the router additionally gates with the middleware).
func TestDeviceHandler_Approve_RequiresIdentity(t *testing.T) {
	h, _, _ := newDeviceHandler()

	w := httptest.NewRecorder()
	h.Start(w, httptest.NewRequest(http.MethodPost, "/auth/device/code", nil))
	var start startResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &start))

	wa := httptest.NewRecorder()
	h.Approve(wa, devReq(http.MethodPost, `{"user_code":"`+start.UserCode+`"}`, nil))
	assert.Equal(t, http.StatusUnauthorized, wa.Code)
}
