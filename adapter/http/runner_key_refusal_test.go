// adapter/http/runner_key_refusal_test.go
package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
)

const fakeRunnerKey = "ark_0123456789abcdefghijklmnopqrstuvwxyzABCDEFG"

func TestRunnerKeyFromHeader(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Runner "+fakeRunnerKey)
	k, ok := runnerKeyFromHeader(h)
	assert.True(t, ok)
	assert.Equal(t, fakeRunnerKey, k)

	h.Set("Authorization", "runner   "+fakeRunnerKey)
	_, ok = runnerKeyFromHeader(h)
	assert.True(t, ok, "scheme is case-insensitive")

	for _, v := range []string{"", "Runner", "Runner ", "Bearer " + fakeRunnerKey, fakeRunnerKey} {
		h.Set("Authorization", v)
		_, ok = runnerKeyFromHeader(h)
		assert.False(t, ok, v)
	}
}

func TestRunnerKey_RefusedOnUserSurfaces_OrgMode(t *testing.T) {
	router, sessions, users, mcp := orgSurface(t)
	uid := uuid.New()
	users.byID[uid] = &instance.User{ID: uid, Username: "root", Role: identity.RoleAdmin}
	sessions.byToken["tok"] = &identity.Session{ID: "tok", UserID: uid}
	cookie := &http.Cookie{Name: "arbitron_session", Value: "tok"}

	cases := []struct {
		name, method, path, auth string
		withCookie               bool
	}{
		{"rest bearer runner key", http.MethodGet, "/api/v1/health", "Bearer " + fakeRunnerKey, false},
		{"rest runner scheme", http.MethodGet, "/api/v1/health", "Runner " + fakeRunnerKey, false},
		{"rest runner key alongside a valid session", http.MethodGet, "/api/v1/health", "Runner " + fakeRunnerKey, true},
		{"ws upgrade with runner key", http.MethodGet, "/api/v1/ws", "Runner " + fakeRunnerKey, true},
		{"mcp bearer runner key", http.MethodPost, "/mcp", "Bearer " + fakeRunnerKey, false},
		{"mcp runner key alongside a valid session", http.MethodPost, "/mcp", "Runner " + fakeRunnerKey, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mcp.reached = false
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`))
			req.Header.Set("Authorization", tc.auth)
			if tc.withCookie {
				req.AddCookie(cookie)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusUnauthorized, rec.Code)
			assert.False(t, mcp.reached, "no tool runs for a runner key")
			assert.NotContains(t, rec.Body.String(), fakeRunnerKey, "the key is never echoed")
		})
	}

	t.Run("session alone still works", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})
}

func TestRunnerKey_RefusedOnUserSurfaces_LocalMode(t *testing.T) {
	router, mcp := localSurface(t)
	for _, auth := range []string{"Runner " + fakeRunnerKey, "Bearer " + fakeRunnerKey} {
		for _, path := range []string{"/api/v1/health", "/mcp"} {
			mcp.reached = false
			method := http.MethodGet
			if path == "/mcp" {
				method = http.MethodPost
			}
			req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
			req.Header.Set("Authorization", auth)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusUnauthorized, rec.Code, "%s %s", auth[:6], path)
			assert.False(t, mcp.reached)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code, "local mode without a runner key is unchanged")
}
