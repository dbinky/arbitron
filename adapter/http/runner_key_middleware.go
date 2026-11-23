// adapter/http/runner_key_middleware.go
package http

import (
	"context"
	"crypto/ed25519"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/arbitron/arbitron/domain/runner"
)

// RunnerKeyAuthenticator is the runner-channel key port (*runner.KeyService).
type RunnerKeyAuthenticator interface {
	Authenticate(ctx context.Context, plaintext string) (runner.RunnerKey, error)
	ProveMachine(ctx context.Context, k runner.RunnerKey, nonce []byte, pub ed25519.PublicKey, sig []byte) (runner.RunnerKey, error)
}

type runnerKeyCtxKey struct{}

// WithRunnerKey stores the authenticated runner key on the request context.
func WithRunnerKey(ctx context.Context, k runner.RunnerKey) context.Context {
	return context.WithValue(ctx, runnerKeyCtxKey{}, k)
}

// RunnerKeyFromContext returns the key set by RunnerKeyMiddleware.
func RunnerKeyFromContext(ctx context.Context) (runner.RunnerKey, bool) {
	k, ok := ctx.Value(runnerKeyCtxKey{}).(runner.RunnerKey)
	return k, ok
}

// runnerKeyQueryParams are parameter names refused outright: a key in a URL
// leaks into proxy and access logs.
var runnerKeyQueryParams = []string{"key", "runner_key", "runnerkey", "token", "access_token", "api_key", "authorization"}

// RunnerKeyMiddleware authenticates runner-channel requests from the
// Authorization header ONLY. It never reads or alters other headers, so
// Cloudflare Access headers reach the edge-validated request unchanged. It
// logs refusal codes, never the key or its hash.
type RunnerKeyMiddleware struct {
	keys RunnerKeyAuthenticator
	log  *slog.Logger
}

// NewRunnerKeyMiddleware builds the middleware; a nil log uses slog.Default().
func NewRunnerKeyMiddleware(keys RunnerKeyAuthenticator, log *slog.Logger) *RunnerKeyMiddleware {
	if keys == nil {
		panic("http: NewRunnerKeyMiddleware requires a key authenticator")
	}
	if log == nil {
		log = slog.Default()
	}
	return &RunnerKeyMiddleware{keys: keys, log: log}
}

func keyInQuery(q url.Values) bool {
	for name, vals := range q {
		for _, p := range runnerKeyQueryParams {
			if strings.EqualFold(name, p) {
				return true
			}
		}
		for _, v := range vals {
			if strings.HasPrefix(v, runner.RunnerKeyPrefix) {
				return true
			}
		}
	}
	return false
}

func (m *RunnerKeyMiddleware) refuse(w http.ResponseWriter, r *http.Request, code, msg string) {
	m.log.Warn("runner request refused", "code", code, "path", r.URL.Path, "remote", r.RemoteAddr)
	w.Header().Set("WWW-Authenticate", RunnerAuthScheme)
	WriteError(w, http.StatusUnauthorized, code, msg)
}

// Handler authenticates and stores the key on the context.
func (m *RunnerKeyMiddleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if keyInQuery(r.URL.Query()) {
			m.refuse(w, r, "RUNNER_KEY_IN_QUERY", "runner keys are accepted only in the Authorization header")
			return
		}
		plaintext, ok := runnerKeyFromHeader(r.Header)
		if !ok {
			m.refuse(w, r, "RUNNER_KEY_REQUIRED", "Authorization: Runner <key> is required")
			return
		}
		k, err := m.keys.Authenticate(r.Context(), plaintext)
		switch {
		case err == nil:
		case errors.Is(err, runner.ErrKeyRevoked):
			m.refuse(w, r, "RUNNER_KEY_REVOKED", "runner key revoked")
			return
		case errors.Is(err, runner.ErrKeyNotFound):
			m.refuse(w, r, "RUNNER_KEY_INVALID", "runner key not recognised")
			return
		default:
			m.log.Error("runner key check failed", "error", err)
			WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "runner key check failed")
			return
		}
		next.ServeHTTP(w, r.WithContext(WithRunnerKey(r.Context(), k)))
	})
}
