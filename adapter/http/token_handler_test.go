package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withChiURLParamTok seeds a chi {key} route param on the request, mirroring how
// the router populates it before reaching the handler.
func withChiURLParamTok(r *http.Request, key, value string) *http.Request {
	rctx := chi.RouteContext(r.Context())
	if rctx == nil {
		rctx = chi.NewRouteContext()
	}
	rctx.URLParams.Add(key, value)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

// memTokenRepo is an in-memory identity.TokenRepository for handler tests. It
// enforces owner-scoping on Delete exactly like the SQLite adapter.
type memTokenRepo struct {
	mu   sync.Mutex
	toks map[uuid.UUID]identity.Token
}

func newMemTokenRepo() *memTokenRepo { return &memTokenRepo{toks: map[uuid.UUID]identity.Token{}} }

func (m *memTokenRepo) Create(_ context.Context, t *identity.Token) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.toks[t.ID] = *t
	return nil
}

func (m *memTokenRepo) ListByUser(_ context.Context, userID uuid.UUID) ([]identity.Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []identity.Token{}
	for _, t := range m.toks {
		if t.UserID == userID {
			out = append(out, t)
		}
	}
	return out, nil
}

func (m *memTokenRepo) GetByHash(_ context.Context, hash string) (*identity.Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.toks {
		if t.TokenHash == hash {
			tt := t
			return &tt, nil
		}
	}
	return nil, identity.ErrNotFound
}

func (m *memTokenRepo) Delete(_ context.Context, id, userID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.toks[id]
	if !ok || t.UserID != userID {
		return identity.ErrNotFound
	}
	delete(m.toks, id)
	return nil
}

func (m *memTokenRepo) TouchLastUsed(_ context.Context, _ uuid.UUID) error { return nil }

// tokenReq builds a request with the given identity injected (as the middleware
// would) and a chi URL param for {id} when revoking.
func tokenReq(method, target, body string, uid uuid.UUID, idParam string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	r = r.WithContext(identity.WithIdentity(r.Context(), identity.Identity{
		UserID: uid, Username: "u", Role: identity.RoleUser,
	}))
	if idParam != "" {
		r = withChiURLParamTok(r, "id", idParam)
	}
	return r
}

func newTokenHandler() (*TokenHandler, *memTokenRepo) {
	repo := newMemTokenRepo()
	return NewTokenHandler(identity.NewTokenService(repo)), repo
}

// Happy path: create returns plaintext once; list never echoes plaintext/hash.
func TestTokenHandler_CreateThenList_HappyPath(t *testing.T) {
	h, _ := newTokenHandler()
	uid := uuid.New()

	w := httptest.NewRecorder()
	h.Create(w, tokenReq(http.MethodPost, "/tokens", `{"label":"ci"}`, uid, ""))
	require.Equal(t, http.StatusCreated, w.Code)

	var created createTokenResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	assert.True(t, strings.HasPrefix(created.Plaintext, "arb_"))
	assert.NotEmpty(t, created.Token.Prefix)
	assert.Equal(t, "ci", created.Token.Label)
	plaintext := created.Plaintext
	// Create is the highest-risk surface: it already carries the plaintext, so
	// accidentally including the hash here would be maximally dangerous.
	assert.NotContains(t, w.Body.String(), "token_hash", "create response must not include token_hash")

	w2 := httptest.NewRecorder()
	h.List(w2, tokenReq(http.MethodGet, "/tokens", "", uid, ""))
	require.Equal(t, http.StatusOK, w2.Code)
	body := w2.Body.String()
	assert.NotContains(t, body, "token_hash", "list must not include token_hash")
	assert.NotContains(t, body, plaintext, "list must not echo the plaintext")

	var list []tokenView
	require.NoError(t, json.Unmarshal([]byte(body), &list))
	require.Len(t, list, 1)
	assert.Equal(t, "ci", list[0].Label)
}

// Input validation: empty label, malformed JSON, bad UUID.
func TestTokenHandler_Validation(t *testing.T) {
	h, _ := newTokenHandler()
	uid := uuid.New()

	w := httptest.NewRecorder()
	h.Create(w, tokenReq(http.MethodPost, "/tokens", `{"label":""}`, uid, ""))
	assert.Equal(t, http.StatusBadRequest, w.Code)

	w = httptest.NewRecorder()
	h.Create(w, tokenReq(http.MethodPost, "/tokens", `{not json`, uid, ""))
	assert.Equal(t, http.StatusBadRequest, w.Code)

	w = httptest.NewRecorder()
	h.Revoke(w, tokenReq(http.MethodDelete, "/tokens/not-a-uuid", "", uid, "not-a-uuid"))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// Happy path: owner revokes their own token → 204, token gone from repo and list.
func TestTokenHandler_Revoke_HappyPath(t *testing.T) {
	h, repo := newTokenHandler()
	uid := uuid.New()

	// Create a token to revoke.
	wc := httptest.NewRecorder()
	h.Create(wc, tokenReq(http.MethodPost, "/tokens", `{"label":"laptop"}`, uid, ""))
	require.Equal(t, http.StatusCreated, wc.Code)
	var created createTokenResponse
	require.NoError(t, json.Unmarshal(wc.Body.Bytes(), &created))
	tokID := created.Token.ID

	// Revoke it — must return 204 and remove it from the repo.
	wr := httptest.NewRecorder()
	h.Revoke(wr, tokenReq(http.MethodDelete, "/tokens/"+tokID, "", uid, tokID))
	assert.Equal(t, http.StatusNoContent, wr.Code)
	assert.Empty(t, repo.toks, "revoked token must not remain in the repo")

	// Subsequent list is empty.
	wl := httptest.NewRecorder()
	h.List(wl, tokenReq(http.MethodGet, "/tokens", "", uid, ""))
	require.Equal(t, http.StatusOK, wl.Code)
	assert.Equal(t, "[]", strings.TrimSpace(wl.Body.String()))
}

// Error/failure: revoking an absent token id returns 404.
func TestTokenHandler_Revoke_NotFound(t *testing.T) {
	h, _ := newTokenHandler()
	uid := uuid.New()
	missing := uuid.New().String()

	w := httptest.NewRecorder()
	h.Revoke(w, tokenReq(http.MethodDelete, "/tokens/"+missing, "", uid, missing))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// Boundary: list with no tokens returns 200 [].
func TestTokenHandler_List_Empty(t *testing.T) {
	h, _ := newTokenHandler()
	w := httptest.NewRecorder()
	h.List(w, tokenReq(http.MethodGet, "/tokens", "", uuid.New(), ""))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "[]", strings.TrimSpace(w.Body.String()))
}

// Isolation/security: user B cannot revoke user A's token; both absent and
// not-owned are 404 (no existence oracle). Plaintext appears only on create.
func TestTokenHandler_Isolation(t *testing.T) {
	h, repo := newTokenHandler()
	aUID := uuid.New()
	bUID := uuid.New()

	// A creates a token.
	w := httptest.NewRecorder()
	h.Create(w, tokenReq(http.MethodPost, "/tokens", `{"label":"a-tok"}`, aUID, ""))
	require.Equal(t, http.StatusCreated, w.Code)
	var created createTokenResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	aTokenID := created.Token.ID

	// B tries to revoke A's token id → 404.
	w2 := httptest.NewRecorder()
	h.Revoke(w2, tokenReq(http.MethodDelete, "/tokens/"+aTokenID, "", bUID, aTokenID))
	assert.Equal(t, http.StatusNotFound, w2.Code)

	// A's token still exists.
	parsed, _ := uuid.Parse(aTokenID)
	_, ok := repo.toks[parsed]
	assert.True(t, ok, "A's token must survive B's revoke attempt")

	// B's list does not include A's token.
	w3 := httptest.NewRecorder()
	h.List(w3, tokenReq(http.MethodGet, "/tokens", "", bUID, ""))
	require.Equal(t, http.StatusOK, w3.Code)
	assert.Equal(t, "[]", strings.TrimSpace(w3.Body.String()))
}
