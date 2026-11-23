// adapter/http/runner_key_middleware_test.go
package http

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/runner"
)

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// stubKeys honours its argument: only the exact plaintext authenticates.
type stubKeys struct {
	plaintext string
	revoked   string
	key       runner.RunnerKey
	calls     int
}

func (s *stubKeys) Authenticate(_ context.Context, plaintext string) (runner.RunnerKey, error) {
	s.calls++
	switch plaintext {
	case s.plaintext:
		return s.key, nil
	case s.revoked:
		return runner.RunnerKey{}, runner.ErrKeyRevoked
	default:
		return runner.RunnerKey{}, runner.ErrKeyNotFound
	}
}

func (s *stubKeys) ProveMachine(context.Context, runner.RunnerKey, []byte, ed25519.PublicKey, []byte) (runner.RunnerKey, error) {
	return runner.RunnerKey{}, runner.ErrWrongMachine
}

const (
	goodKey    = "ark_GOODgoodGOODgoodGOODgoodGOODgoodGOODgoodGOO"
	revokedKey = "ark_REVOKEDrevokedREVOKEDrevokedREVOKEDrevoked"
)

func newStubKeys() *stubKeys {
	return &stubKeys{plaintext: goodKey, revoked: revokedKey,
		key: runner.RunnerKey{ID: uuid.New(), Name: "mac-a", RunnerID: "mac-a", CreatedAt: time.Now()}}
}

func TestRunnerKeyMiddleware_Refusals(t *testing.T) {
	keys := newStubKeys()
	logs := &syncBuffer{}
	mw := NewRunnerKeyMiddleware(keys, slog.New(slog.NewTextHandler(logs, nil)))
	reached := false
	h := mw.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusNoContent)
	}))
	cases := []struct {
		name, target, auth, code string
	}{
		{"missing", "/c", "", "RUNNER_KEY_REQUIRED"},
		{"bearer scheme", "/c", "Bearer " + goodKey, "RUNNER_KEY_REQUIRED"},
		{"unknown key", "/c", "Runner ark_nope", "RUNNER_KEY_INVALID"},
		{"revoked key", "/c", "Runner " + revokedKey, "RUNNER_KEY_REVOKED"},
		{"key in query", "/c?key=" + goodKey, "Runner " + goodKey, "RUNNER_KEY_IN_QUERY"},
		{"token param", "/c?access_token=whatever", "Runner " + goodKey, "RUNNER_KEY_IN_QUERY"},
		{"ark value under any name", "/c?x=" + goodKey, "", "RUNNER_KEY_IN_QUERY"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reached = false
			req := httptest.NewRequest(http.MethodGet, tc.target, nil)
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusUnauthorized, rec.Code)
			assert.Contains(t, rec.Body.String(), tc.code)
			assert.Equal(t, RunnerAuthScheme, rec.Header().Get("WWW-Authenticate"))
			assert.False(t, reached)
			assert.NotContains(t, rec.Body.String(), goodKey)
		})
	}
	assert.NotContains(t, logs.String(), goodKey)
	assert.NotContains(t, logs.String(), revokedKey)
	assert.NotContains(t, logs.String(), runner.HashRunnerKey(goodKey))
	assert.Contains(t, logs.String(), "RUNNER_KEY_IN_QUERY", "refusals are logged by code")

	req := httptest.NewRequest(http.MethodGet, "/c", nil)
	req.Header.Set("Authorization", "Runner "+goodKey)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.True(t, reached)
}

func TestRunnerKeyMiddleware_PassesAccessHeadersUntouched(t *testing.T) {
	keys := newStubKeys()
	var seen http.Header
	var got runner.RunnerKey
	h := NewRunnerKeyMiddleware(keys, nil).Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		got, _ = RunnerKeyFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/c", nil)
	req.Header.Set("Authorization", "Runner "+goodKey)
	req.Header.Set("CF-Access-Client-Id", "client.access")
	req.Header.Set("CF-Access-Client-Secret", "cf-secret-value")
	req.Header.Set("Cf-Access-Jwt-Assertion", "eyJhbGciOi.jwt.sig")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, "client.access", seen.Get("CF-Access-Client-Id"))
	assert.Equal(t, "cf-secret-value", seen.Get("CF-Access-Client-Secret"))
	assert.Equal(t, "eyJhbGciOi.jwt.sig", seen.Get("Cf-Access-Jwt-Assertion"))
	assert.Equal(t, runner.RunnerID("mac-a"), got.RunnerID)
	assert.Equal(t, 1, keys.calls)
}

func TestRunnerKeyFromContext_Absent(t *testing.T) {
	_, ok := RunnerKeyFromContext(context.Background())
	assert.False(t, ok)
	k := runner.RunnerKey{RunnerID: "x"}
	got, ok := RunnerKeyFromContext(WithRunnerKey(context.Background(), k))
	assert.True(t, ok)
	assert.Equal(t, k.RunnerID, got.RunnerID)
}
