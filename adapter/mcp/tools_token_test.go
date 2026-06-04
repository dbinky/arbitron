package mcp

import (
	"context"
	"strings"
	"sync"
	"testing"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
)

// memTokenRepo is an in-memory identity.TokenRepository enforcing owner-scoping.
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

func tokenToolServer(svc *identity.TokenService) *Server {
	return NewServerWithDeps("test", "0.0.0", &Deps{Tokens: svc})
}

func userCtx(uid uuid.UUID) context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{
		UserID: uid, Username: "u", Role: identity.RoleUser,
	})
}

func toolText(t *testing.T, res *gomcp.CallToolResult) string {
	t.Helper()
	require.NotNil(t, res)
	require.NotEmpty(t, res.Content)
	tc, ok := res.Content[0].(gomcp.TextContent)
	require.True(t, ok)
	return tc.Text
}

func callReq(args map[string]any) gomcp.CallToolRequest {
	req := gomcp.CallToolRequest{}
	req.Params.Arguments = args
	return req
}

// Happy path: create returns plaintext once; list returns metadata with no
// secret/hash; revoke deletes.
func TestTokenTools_HappyPath(t *testing.T) {
	repo := newMemTokenRepo()
	srv := tokenToolServer(identity.NewTokenService(repo))
	uid := uuid.New()
	ctx := userCtx(uid)

	res, err := tokenCreateHandler(srv)(ctx, callReq(map[string]any{"label": "ci"}))
	require.NoError(t, err)
	require.False(t, res.IsError, toolText(t, res))
	body := toolText(t, res)
	assert.Contains(t, body, "arb_")
	assert.Contains(t, body, `"plaintext"`)
	// Create is the highest-risk surface: it carries the plaintext, so any
	// accidental hash inclusion here is maximally dangerous.
	assert.NotContains(t, body, "token_hash", "create response must not include token_hash")

	listRes, err := tokenListHandler(srv)(ctx, callReq(nil))
	require.NoError(t, err)
	listBody := toolText(t, listRes)
	assert.NotContains(t, listBody, "token_hash")
	assert.NotContains(t, listBody, "plaintext")
	assert.Contains(t, listBody, "ci")

	// Revoke the created token.
	var id uuid.UUID
	for k := range repo.toks {
		id = k
	}
	revRes, err := tokenRevokeHandler(srv)(ctx, callReq(map[string]any{"id": id.String()}))
	require.NoError(t, err)
	require.False(t, revRes.IsError, toolText(t, revRes))
	assert.Empty(t, repo.toks)
}

// Input validation: missing label / missing / invalid id.
func TestTokenTools_Validation(t *testing.T) {
	srv := tokenToolServer(identity.NewTokenService(newMemTokenRepo()))
	ctx := userCtx(uuid.New())

	res, _ := tokenCreateHandler(srv)(ctx, callReq(map[string]any{"label": ""}))
	assert.True(t, res.IsError)

	res, _ = tokenRevokeHandler(srv)(ctx, callReq(map[string]any{}))
	assert.True(t, res.IsError)

	res, _ = tokenRevokeHandler(srv)(ctx, callReq(map[string]any{"id": "not-a-uuid"}))
	assert.True(t, res.IsError)
}

// Error/failure: revoking an unknown id yields a "token not found" tool error.
func TestTokenTools_RevokeUnknown(t *testing.T) {
	srv := tokenToolServer(identity.NewTokenService(newMemTokenRepo()))
	ctx := userCtx(uuid.New())

	res, _ := tokenRevokeHandler(srv)(ctx, callReq(map[string]any{"id": uuid.New().String()}))
	require.True(t, res.IsError)
	assert.Contains(t, toolText(t, res), "token not found")
}

// Boundary: list with no tokens returns [].
func TestTokenTools_ListEmpty(t *testing.T) {
	srv := tokenToolServer(identity.NewTokenService(newMemTokenRepo()))
	ctx := userCtx(uuid.New())

	res, _ := tokenListHandler(srv)(ctx, callReq(nil))
	assert.Equal(t, "[]", strings.TrimSpace(toolText(t, res)))
}

// Isolation/security: nil Tokens dep reports unavailable; list never leaks
// secret material across users.
func TestTokenTools_Isolation(t *testing.T) {
	// nil dep.
	nilSrv := NewServerWithDeps("test", "0.0.0", &Deps{})
	res, _ := tokenListHandler(nilSrv)(context.Background(), callReq(nil))
	require.True(t, res.IsError)
	assert.Contains(t, toolText(t, res), "token service not available")

	repo := newMemTokenRepo()
	svc := identity.NewTokenService(repo)
	srv := tokenToolServer(svc)
	aUID := uuid.New()
	bUID := uuid.New()

	createRes, _ := tokenCreateHandler(srv)(userCtx(aUID), callReq(map[string]any{"label": "a"}))
	require.False(t, createRes.IsError)

	// B lists: never sees A's token (and no hash/plaintext).
	bList, _ := tokenListHandler(srv)(userCtx(bUID), callReq(nil))
	bBody := toolText(t, bList)
	assert.Equal(t, "[]", strings.TrimSpace(bBody))
	assert.NotContains(t, bBody, "token_hash")
}
