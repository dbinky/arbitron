// adapter/http/runner_key_handler_test.go
package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/audit"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/runner"
)

type keyAuditSpy struct {
	mu      sync.Mutex
	actions []string
}

func (a *keyAuditSpy) Record(_ context.Context, action, target, _ string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.actions = append(a.actions, action+" "+target)
}

type keyRevokerSpy struct {
	mu  sync.Mutex
	ids []runner.RunnerID
}

func (r *keyRevokerSpy) RevokeRunner(_ context.Context, id runner.RunnerID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids = append(r.ids, id)
	return nil
}

type keyRouterFixture struct {
	router  http.Handler
	admin   *http.Cookie
	user    *http.Cookie
	audit   *keyAuditSpy
	revoker *keyRevokerSpy
}

func newKeyRouter(t *testing.T) keyRouterFixture {
	t.Helper()
	db, err := adaptsqlite.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, adaptsqlite.RunMigrations(db))
	a, rv := &keyAuditSpy{}, &keyRevokerSpy{}
	svc := runner.NewKeyService(runner.KeyServiceDeps{
		Store: adaptsqlite.NewRunnerKeyRepo(db), Registry: adaptsqlite.NewRunnerRegistryRepo(db),
		Audit: a, Revoker: rv, Clock: time.Now, DefaultStepLimit: 8,
	})
	users, sessions := newFakeUserRepo(), newFakeSessionRepo()
	adminID, userID := uuid.New(), uuid.New()
	users.byID[adminID] = &instance.User{ID: adminID, Username: "root", Role: identity.RoleAdmin}
	users.byID[userID] = &instance.User{ID: userID, Username: "bob", Role: identity.RoleUser}
	sessions.byToken["admin"] = &identity.Session{ID: "admin", UserID: adminID}
	sessions.byToken["user"] = &identity.Session{ID: "user", UserID: userID}
	auth := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", sessions, nil, users, identity.GroupConfig{DefaultGroup: "default"})
	router := NewRouterFromDeps(RouterDeps{Config: &config.Config{}, Auth: auth, RunnerKeys: svc})
	return keyRouterFixture{router: router,
		admin: &http.Cookie{Name: "arbitron_session", Value: "admin"},
		user:  &http.Cookie{Name: "arbitron_session", Value: "user"},
		audit: a, revoker: rv}
}

func (f keyRouterFixture) do(t *testing.T, method, path, body string, cookie *http.Cookie, auth string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func TestRunnerKeysREST_AdminLifecycle(t *testing.T) {
	f := newKeyRouter(t)

	rec := f.do(t, http.MethodPost, "/api/v1/runners/keys", `{"name":"mac-a"}`, f.admin, "")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var created struct {
		Key       map[string]any `json:"key"`
		Plaintext string         `json:"plaintext"`
		Setup     string         `json:"setup"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	assert.True(t, strings.HasPrefix(created.Plaintext, "ark_"))
	assert.Equal(t, "mac-a", created.Key["name"])
	assert.Equal(t, "mac-a", created.Key["runnerId"])
	assert.Equal(t, false, created.Key["claimed"])
	assert.Equal(t, runner.KeySetupGuidance, created.Setup)
	assert.NotContains(t, strings.ToLower(rec.Body.String()), "hash")

	list := f.do(t, http.MethodGet, "/api/v1/runners/keys", "", f.admin, "")
	require.Equal(t, http.StatusOK, list.Code)
	var keys []map[string]any
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &keys))
	require.Len(t, keys, 1)
	assert.NotContains(t, list.Body.String(), created.Plaintext, "the secret is shown once")
	assert.NotContains(t, strings.ToLower(list.Body.String()), "hash")
	id := keys[0]["id"].(string)

	noAck := f.do(t, http.MethodDelete, "/api/v1/runners/keys/"+id, `{}`, f.admin, "")
	assert.Equal(t, http.StatusBadRequest, noAck.Code)
	assert.Contains(t, noAck.Body.String(), "ACKNOWLEDGEMENT_REQUIRED")
	assert.Empty(t, f.revoker.ids)

	rev := f.do(t, http.MethodDelete, "/api/v1/runners/keys/"+id, `{"acknowledge":true}`, f.admin, "")
	require.Equal(t, http.StatusOK, rev.Code, rev.Body.String())
	assert.JSONEq(t, `{"status":"revoked","id":"`+id+`"}`, rev.Body.String())
	assert.Equal(t, []runner.RunnerID{"mac-a"}, f.revoker.ids)
	assert.Equal(t, []string{audit.ActionRunnerKeyCreate + " mac-a", audit.ActionRunnerKeyRevoke + " mac-a"}, f.audit.actions)

	list = f.do(t, http.MethodGet, "/api/v1/runners/keys", "", f.admin, "")
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &keys))
	assert.Equal(t, true, keys[0]["revoked"])
	assert.NotEmpty(t, keys[0]["revokedAt"])
}

func TestRunnerKeysREST_NonAdminForbidden(t *testing.T) {
	f := newKeyRouter(t)
	assert.Equal(t, http.StatusForbidden, f.do(t, http.MethodPost, "/api/v1/runners/keys", `{"name":"mac-a"}`, f.user, "").Code)
	assert.Equal(t, http.StatusForbidden, f.do(t, http.MethodGet, "/api/v1/runners/keys", "", f.user, "").Code)
	assert.Equal(t, http.StatusForbidden, f.do(t, http.MethodDelete, "/api/v1/runners/keys/"+uuid.NewString(), `{"acknowledge":true}`, f.user, "").Code)
	assert.Equal(t, http.StatusUnauthorized, f.do(t, http.MethodGet, "/api/v1/runners/keys", "", nil, "").Code)
	assert.Empty(t, f.audit.actions)
}

func TestRunnerKeysREST_Validation(t *testing.T) {
	f := newKeyRouter(t)
	assert.Equal(t, http.StatusBadRequest, f.do(t, http.MethodPost, "/api/v1/runners/keys", `{"name":"Not Valid"}`, f.admin, "").Code)
	assert.Equal(t, http.StatusBadRequest, f.do(t, http.MethodPost, "/api/v1/runners/keys", `not json`, f.admin, "").Code)
	require.Equal(t, http.StatusCreated, f.do(t, http.MethodPost, "/api/v1/runners/keys", `{"name":"mac-a"}`, f.admin, "").Code)
	dup := f.do(t, http.MethodPost, "/api/v1/runners/keys", `{"name":"mac-a"}`, f.admin, "")
	assert.Equal(t, http.StatusConflict, dup.Code)
	assert.Equal(t, http.StatusNotFound, f.do(t, http.MethodDelete, "/api/v1/runners/keys/"+uuid.NewString(), `{"acknowledge":true}`, f.admin, "").Code)
	assert.Equal(t, http.StatusBadRequest, f.do(t, http.MethodDelete, "/api/v1/runners/keys/not-a-uuid", `{"acknowledge":true}`, f.admin, "").Code)
}

// Design §8 regression: runner-key requests cannot reach operator/key REST routes.
func TestRunnerKeysREST_RunnerKeyCannotManageKeys(t *testing.T) {
	f := newKeyRouter(t)
	rec := f.do(t, http.MethodPost, "/api/v1/runners/keys", `{"name":"mac-a"}`, f.admin, "")
	require.Equal(t, http.StatusCreated, rec.Code)
	var created struct{ Plaintext string }
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))

	for _, auth := range []string{"Bearer " + created.Plaintext, "Runner " + created.Plaintext} {
		assert.Equal(t, http.StatusUnauthorized, f.do(t, http.MethodGet, "/api/v1/runners/keys", "", nil, auth).Code)
		assert.Equal(t, http.StatusUnauthorized, f.do(t, http.MethodPost, "/api/v1/runners/keys", `{"name":"mac-b"}`, nil, auth).Code)
		assert.Equal(t, http.StatusUnauthorized, f.do(t, http.MethodPost, "/api/v1/runners/keys", `{"name":"mac-c"}`, f.admin, auth).Code,
			"a runner key never rides along an admin session")
	}
	assert.Equal(t, []string{audit.ActionRunnerKeyCreate + " mac-a"}, f.audit.actions)
}

func TestRunnerKeysREST_RoutesAbsentWhenNotWired(t *testing.T) {
	users, sessions := newFakeUserRepo(), newFakeSessionRepo()
	uid := uuid.New()
	users.byID[uid] = &instance.User{ID: uid, Username: "root", Role: identity.RoleAdmin}
	sessions.byToken["tok"] = &identity.Session{ID: "tok", UserID: uid}
	auth := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", sessions, nil, users, identity.GroupConfig{DefaultGroup: "default"})
	router := NewRouterFromDeps(RouterDeps{Config: &config.Config{}, Auth: auth})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runners/keys", nil)
	req.AddCookie(&http.Cookie{Name: "arbitron_session", Value: "tok"})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}
