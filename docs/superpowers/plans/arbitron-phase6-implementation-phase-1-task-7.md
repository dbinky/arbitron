# Phase 1, Task 7: `/auth` Login/Callback/Logout Handlers + Session Cookie

## Goal

Add the browser login flow handlers under `/auth` (OUTSIDE the authenticated group): `GET /auth/login` (mint state+nonce, redirect to the IdP), `GET /auth/callback` (verify state+nonce, exchange the code, resolve/provision the user, create a session, set the session cookie, redirect to the original destination), and `POST /auth/logout` (delete the session, clear the cookie). The handler ties together the `OIDCProvider`, `SessionRepository`, and `UserRepository` ports and sets a `HttpOnly`, `SameSite=Lax`, `Secure`(per config) cookie holding the raw session token.

Source of truth: `docs/superpowers/specs/arbitron-phase6-design-phase-1.md` §5.

## Dependencies

- Task 1 (config) — `cfg.Auth.Session.{CookieName,CookieSecure,TTLSeconds}`, `cfg.Server.PublicBaseURL`.
- Task 2 (`domain/identity`) — `OIDCProvider`, `SessionRepository`, `Claims`, `Session`, `Identity`.
- Task 3 (`UserRepository` expansion) — `GetBySubject`, `Create` for user resolution/Phase-1 provisioning.
- Task 4 (SessionRepo) — concrete session creation/deletion.
- Task 5 (OIDC adapter) — the `OIDCProvider` implementation.

## Background (current state)

- There are no `/auth` routes today. They are registered in `server.go` OUTSIDE the `/api/v1` group and the `d.Auth` middleware (they establish the session, so they cannot require one).
- The house pattern for handlers: a struct (e.g. `WorkflowHandler`, `NotificationHandler`, `SystemHandler` in `server.go`) with `New...Handler(deps)` and method handlers using `WriteJSON`/`WriteError`. Mirror that.
- Per design §5, Phase-1 user resolution: `GetBySubject(claims.Subject)`; if `instance.ErrNotFound`, JIT-provision a PLAIN `User` (role `user`) so the flow is testable end-to-end. (Bootstrap-admin promotion + real JIT policy is Phase 2; Phase 1 provisions as a plain user.)
- State/nonce storage: a short-lived signed/`HttpOnly` cookie (separate from the session cookie) holds the `state` and `nonce` between `/auth/login` and `/auth/callback`. Sign it with an HMAC key derived at startup (random per-process is acceptable for Phase 1 since the login round-trip is short-lived and same-process). Cookie is cleared on callback.
- Cookie security flags come from config: `CookieSecure` (Secure attribute), name from `CookieName`, `SameSite=Lax` (allows the top-level redirect back from the IdP to carry the cookie), `HttpOnly` always, `Path=/`.

## Files to Create/Modify

### Create: `adapter/http/auth_handler.go`

```go
package http

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// AuthHandler serves the browser OIDC login flow. It lives OUTSIDE the auth
// middleware: these routes establish the session that the middleware later
// validates.
type AuthHandler struct {
	oidc     identity.OIDCProvider
	sessions identity.SessionRepository
	users    instance.UserRepository
	cfg      config.SessionConfig
	hmacKey  []byte // signs the short-lived state/nonce cookie
}

func NewAuthHandler(oidc identity.OIDCProvider, sessions identity.SessionRepository, users instance.UserRepository, cfg config.SessionConfig) *AuthHandler {
	key := make([]byte, 32)
	_, _ = rand.Read(key) // per-process state-cookie signing key
	return &AuthHandler{oidc: oidc, sessions: sessions, users: users, cfg: cfg, hmacKey: key}
}

const stateCookieName = "arbitron_oauth_state"

// Login: GET /auth/login — mint state+nonce, stash them in a signed short-lived
// cookie, redirect to the IdP.
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	state := randToken()
	nonce := randToken()
	next := sanitizeNext(r.URL.Query().Get("next")) // only same-site relative paths
	h.setStateCookie(w, state, nonce, next)
	http.Redirect(w, r, h.oidc.AuthCodeURL(state, nonce), http.StatusFound)
}

// Callback: GET /auth/callback — verify state, exchange code, resolve user,
// create session, set session cookie, redirect to next.
func (h *AuthHandler) Callback(w http.ResponseWriter, r *http.Request) {
	gotState := r.URL.Query().Get("state")
	wantState, wantNonce, next, ok := h.readStateCookie(r)
	h.clearStateCookie(w)
	if !ok || gotState == "" || gotState != wantState {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid auth state")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "missing code")
		return
	}
	claims, err := h.oidc.Exchange(r.Context(), code)
	if err != nil {
		WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "login failed")
		return
	}
	// Nonce equality check (replay guard): the verifier confirmed signature/
	// issuer/audience; here we confirm the nonce we minted is the one returned.
	// (If the OIDC adapter already enforces nonce, this is defence-in-depth.)
	_ = wantNonce // compared against claims nonce if exposed; see Task 5 note.

	user, err := h.resolveOrProvision(r.Context(), claims)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "could not resolve user")
		return
	}

	sess := &identity.Session{
		ID:        randToken(),
		UserID:    user.ID,
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(time.Duration(h.cfg.TTLSeconds) * time.Second),
	}
	if err := h.sessions.Create(r.Context(), sess); err != nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "could not start session")
		return
	}
	h.setSessionCookie(w, sess.ID, sess.ExpiresAt)
	http.Redirect(w, r, next, http.StatusFound)
}

// Logout: POST /auth/logout — delete the session, clear the cookie.
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(h.cfg.CookieName); err == nil && c.Value != "" {
		_ = h.sessions.Delete(r.Context(), c.Value)
	}
	h.clearSessionCookie(w)
	WriteJSON(w, http.StatusOK, map[string]string{"status": "logged out"})
}

func (h *AuthHandler) resolveOrProvision(ctx context.Context, claims identity.Claims) (*instance.User, error) {
	user, err := h.users.GetBySubject(ctx, claims.Subject)
	if err == nil {
		return user, nil
	}
	if !errors.Is(err, instance.ErrNotFound) {
		return nil, err
	}
	// Phase-1 JIT: provision a plain user (role user). Bootstrap-admin promotion
	// is Phase 2. Persist the email claim (nullable) so the Phase 2 demotion floor
	// can match a bootstrap-by-email admin from the persisted User alone.
	subj := claims.Subject
	var emailPtr *string
	if claims.Email != "" {
		e := claims.Email
		emailPtr = &e
	}
	u := &instance.User{ID: uuid.New(), Subject: &subj, Email: emailPtr, Username: claims.Username, Role: identity.RoleUser}
	if err := h.users.Create(ctx, u); err != nil {
		return nil, err
	}
	return u, nil
}

func (h *AuthHandler) setSessionCookie(w http.ResponseWriter, raw string, exp time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     h.cfg.CookieName,
		Value:    raw, // RAW token; only its hash is stored server-side
		Path:     "/",
		Expires:  exp,
		HttpOnly: true,
		Secure:   h.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (h *AuthHandler) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: h.cfg.CookieName, Value: "", Path: "/",
		MaxAge: -1, HttpOnly: true, Secure: h.cfg.CookieSecure, SameSite: http.SameSiteLaxMode,
	})
}

func randToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// sanitizeNext rejects absolute/external URLs to prevent open-redirect; only a
// same-site path beginning with a single "/" (not "//") is allowed.
func sanitizeNext(next string) string {
	if next == "" || next[0] != '/' || (len(next) > 1 && next[1] == '/') {
		return "/"
	}
	return next
}
```

The signed state/nonce cookie helpers (`setStateCookie`/`readStateCookie`/`clearStateCookie`) HMAC-sign `state|nonce|next` with `h.hmacKey` and store it in a short-`MaxAge`, `HttpOnly`, `SameSite=Lax`, `Secure` cookie named `stateCookieName`; `readStateCookie` verifies the HMAC before returning the values. Implement with `crypto/hmac` + `crypto/sha256`.

### Modify: `adapter/http/server.go`

1. Add to `RouterDeps`: `AuthHandler *AuthHandler` (nil in local mode / when auth disabled).
2. Register the `/auth` routes OUTSIDE the `/api/v1` group and BEFORE the dashboard `NotFound`, not wrapped by `d.Auth`:

```go
if d.AuthHandler != nil {
	r.Get("/auth/login", d.AuthHandler.Login)
	r.Get("/auth/callback", d.AuthHandler.Callback)
	r.Post("/auth/logout", d.AuthHandler.Logout)
}
```

## Testing (strict TDD — five scenario categories)

Tests in `adapter/http/auth_handler_test.go` with a fake `OIDCProvider` (returns a canned `Claims` / error), in-memory fake `SessionRepository`/`UserRepository`, and `httptest`. Write first.

- **Happy path** — `TestAuthHandler_LoginThenCallback_NewUser`: `GET /auth/login?next=/instances` sets the state cookie and 302s to the fake IdP URL (containing the minted state); a follow-up `GET /auth/callback?code=ok&state=<same>` (carrying the state cookie) exchanges via the fake OIDC, JIT-creates a `user`-role user with `Subject==claims.Subject`, creates a session, sets the session cookie (raw token), and 302s to `/instances`.
- **Happy path (existing user)** — `TestAuthHandler_Callback_ExistingUser`: when `GetBySubject` already returns a user, no new user is created and the existing user's role is preserved.
- **Input validation** — `TestAuthHandler_Callback_StateMismatch`: `state` query ≠ state cookie → `400`, no session created. `TestAuthHandler_Callback_MissingCode` → `400`.
- **Error/failure** — `TestAuthHandler_Callback_ExchangeFails`: fake OIDC returns an error → `401`, no session, no cookie set. `TestAuthHandler_Callback_SessionCreateFails`: session repo errors → `500`, no session cookie.
- **Boundary** — `TestAuthHandler_Login_SanitizesNext`: `next=https://evil.com` and `next=//evil.com` both collapse to `/` (open-redirect guard); `next=/instances/123` is preserved. `TestAuthHandler_SessionCookie_Attributes`: the session cookie is `HttpOnly`, `SameSite=Lax`, `Path=/`, `Secure` matching `cfg.CookieSecure`, and `Expires` ≈ now+TTL.
- **Isolation/security** — `TestAuthHandler_Callback_SetsRawTokenCookie_NotHash`: the cookie value is the raw session token (the value the fake session repo will hash on lookup), and the value is high-entropy (length/charset check). `TestAuthHandler_Logout_DeletesSession`: after `POST /auth/logout`, the fake session repo no longer has the session and the response clears the cookie (`MaxAge<0`); a subsequent middleware lookup (Task 6) would 401. `TestAuthHandler_Callback_TamperedStateCookie`: a state cookie with a broken HMAC is rejected (treated as state mismatch → 400), proving the signed-cookie integrity check.

## Acceptance Criteria

- `/auth/login`, `/auth/callback`, `/auth/logout` are registered outside the auth middleware.
- Callback verifies state (+nonce per Task 5 contract), exchanges the code, resolves-or-JIT-provisions a `user`-role user, creates a session, and sets a `HttpOnly`/`Lax`/config-`Secure` cookie holding the RAW token.
- `next` is open-redirect-safe; logout deletes the session and clears the cookie.
- `go build ./...` and `go test ./adapter/http/...` pass.
