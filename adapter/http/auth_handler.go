package http

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
)

// userProvisioner is the consumer-side seam the handler needs: resolve verified
// claims to a persisted user (JIT-creating on first sight). Defined HERE (the
// consumer) rather than depending on the concrete *instance.Provisioner, so the
// handler is unit-testable with a fake. *instance.Provisioner satisfies it.
type userProvisioner interface {
	Resolve(ctx context.Context, c instance.Claims) (*instance.User, error)
}

// AuthHandler serves the browser OIDC login flow. It lives OUTSIDE the auth
// middleware: these routes establish the session that the middleware later
// validates.
type AuthHandler struct {
	oidc        identity.OIDCProvider
	sessions    identity.SessionRepository
	users       instance.UserRepository
	provisioner userProvisioner
	cfg         config.SessionConfig
	hmacKey     []byte // signs the short-lived state/nonce cookie
}

// NewAuthHandler builds the handler. The state-cookie signing key is random per
// process, which is acceptable for the short-lived login round-trip. The
// provisioner resolves verified claims to a persisted User (JIT-creating one on
// first login, as admin for bootstrap identities).
func NewAuthHandler(oidc identity.OIDCProvider, sessions identity.SessionRepository, users instance.UserRepository, provisioner userProvisioner, cfg config.SessionConfig) *AuthHandler {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	return &AuthHandler{oidc: oidc, sessions: sessions, users: users, provisioner: provisioner, cfg: cfg, hmacKey: key}
}

const stateCookieName = "arbitron_oauth_state"

// Login: GET /auth/login -- mint state+nonce, stash them in a signed short-lived
// cookie, redirect to the IdP.
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	state := randToken()
	nonce := randToken()
	next := sanitizeNext(r.URL.Query().Get("next"))
	h.setStateCookie(w, state, nonce, next)
	http.Redirect(w, r, h.oidc.AuthCodeURL(state, nonce), http.StatusFound)
}

// Callback: GET /auth/callback -- verify state, exchange code, resolve user,
// create session, set session cookie, redirect to next.
func (h *AuthHandler) Callback(w http.ResponseWriter, r *http.Request) {
	gotState := r.URL.Query().Get("state")
	wantState, wantNonce, next, ok := h.readStateCookie(r)
	h.clearStateCookie(w)
	if !ok || gotState == "" || subtle.ConstantTimeCompare([]byte(gotState), []byte(wantState)) != 1 {
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
	// Nonce equality (replay guard): the nonce we minted at login is held in the
	// HMAC-signed state cookie; the adapter now surfaces it via Claims.Nonce. A
	// constant-time comparison guards against timing attacks. Mismatch means the
	// ID token was minted for a different login session (replay).
	if subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(wantNonce)) != 1 {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "nonce mismatch")
		return
	}

	user, err := h.resolveOrProvision(r.Context(), claims)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "could not resolve user")
		return
	}

	now := time.Now().UTC()
	sess := &identity.Session{
		ID:        randToken(),
		UserID:    user.ID,
		CreatedAt: now,
		ExpiresAt: now.Add(time.Duration(h.cfg.TTLSeconds) * time.Second),
	}
	if err := h.sessions.Create(r.Context(), sess); err != nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "could not start session")
		return
	}
	h.setSessionCookie(w, sess.ID, sess.ExpiresAt)
	http.Redirect(w, r, next, http.StatusFound)
}

// Logout: POST /auth/logout -- delete the session, clear the cookie.
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(h.cfg.CookieName); err == nil && c.Value != "" {
		_ = h.sessions.Delete(r.Context(), c.Value)
	}
	h.clearSessionCookie(w)
	WriteJSON(w, http.StatusOK, map[string]string{"status": "logged out"})
}

// resolveOrProvision maps the verified identity.Claims to the instance.Claims
// the Provisioner consumes and delegates the JIT-create + bootstrap-admin rule
// to the single domain provisioner (Phase 2), replacing the Phase 1 placeholder.
func (h *AuthHandler) resolveOrProvision(ctx context.Context, claims identity.Claims) (*instance.User, error) {
	return h.provisioner.Resolve(ctx, instance.Claims{
		Subject:  claims.Subject,
		Username: claims.Username,
		Email:    claims.Email,
		// SECURITY: forward email_verified so the provisioner only trusts the
		// email for bootstrap-admin matching when the IdP verified it.
		EmailVerified: claims.EmailVerified,
	})
}

func (h *AuthHandler) setSessionCookie(w http.ResponseWriter, raw string, exp time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     h.cfg.CookieName,
		Value:    raw, // RAW token; only its hash is stored server-side
		Path:     "/",
		Expires:  exp,
		HttpOnly: true,
		Secure:   h.cfg.CookieSecure,
		// SameSite=Lax is DELIBERATE: SameSite=Strict would drop the cookie on the
		// cross-site redirect back from the IdP (/auth/callback), breaking the OIDC
		// login round-trip. Do not naively flip to Strict in a hardening pass.
		SameSite: http.SameSiteLaxMode,
	})
}

func (h *AuthHandler) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: h.cfg.CookieName, Value: "", Path: "/",
		MaxAge: -1, HttpOnly: true, Secure: h.cfg.CookieSecure, SameSite: http.SameSiteLaxMode,
	})
}

// ---- signed state/nonce cookie ----

// setStateCookie stores "state|nonce|next" with an HMAC suffix in a short-lived
// HttpOnly cookie so /auth/callback can verify it was the handler that issued
// this login round-trip.
func (h *AuthHandler) setStateCookie(w http.ResponseWriter, state, nonce, next string) {
	payload := strings.Join([]string{state, nonce, next}, "|")
	signed := payload + "|" + h.sign(payload)
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    base64.RawURLEncoding.EncodeToString([]byte(signed)),
		Path:     "/",
		MaxAge:   600, // 10 minutes
		HttpOnly: true,
		Secure:   h.cfg.CookieSecure,
		// SameSite=Lax is DELIBERATE: Strict would drop this state/nonce cookie on
		// the cross-site redirect back from the IdP, breaking CSRF-state validation
		// in /auth/callback. Do not naively flip to Strict in a hardening pass.
		SameSite: http.SameSiteLaxMode,
	})
}

func (h *AuthHandler) readStateCookie(r *http.Request) (state, nonce, next string, ok bool) {
	c, err := r.Cookie(stateCookieName)
	if err != nil || c.Value == "" {
		return "", "", "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return "", "", "", false
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 4 {
		return "", "", "", false
	}
	payload := strings.Join(parts[:3], "|")
	if !hmac.Equal([]byte(parts[3]), []byte(h.sign(payload))) {
		return "", "", "", false
	}
	return parts[0], parts[1], sanitizeNext(parts[2]), true
}

func (h *AuthHandler) clearStateCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: stateCookieName, Value: "", Path: "/",
		MaxAge: -1, HttpOnly: true, Secure: h.cfg.CookieSecure, SameSite: http.SameSiteLaxMode,
	})
}

func (h *AuthHandler) sign(payload string) string {
	mac := hmac.New(sha256.New, h.hmacKey)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
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
